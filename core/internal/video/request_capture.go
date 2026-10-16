package video

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/task"
	"gorm.io/gorm"
)

var requestModelName = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)

func requestOperation(method, route string) string {
	switch {
	case route == "/v1/video/generations" && method == http.MethodPost:
		return "create_task"
	case route == "/v1/video/generations":
		return "list_tasks"
	case strings.HasSuffix(route, "/cancel"):
		return "cancel_task"
	case strings.HasSuffix(route, "/content"):
		return "read_content"
	case strings.HasSuffix(route, "/billing"):
		return "read_billing"
	case strings.HasSuffix(route, "/events"):
		return "read_events"
	case strings.HasSuffix(route, "/estimates"):
		return "estimate"
	case strings.HasSuffix(route, "/models"):
		return "list_models"
	case strings.Contains(route, "/operations/"):
		return "read_operation"
	case strings.Contains(route, "/generations/") && method == http.MethodDelete:
		return "delete_task"
	case strings.Contains(route, "/generations/"):
		return "get_task"
	default:
		return "asset_operation"
	}
}

// captureReceivedRequest is called only after key authentication and scope/IP
// checks. The bounded buffer is restored for the strict business decoder.
func (s *Service) captureReceivedRequest(c *gin.Context) error {
	org := auditRequestOrg(c)
	if org == 0 {
		return ErrForbidden
	}
	rid := c.GetString("video_request_id")
	meta := invocation.VideoRequestContext{OrgID: org, RequestID: rid, Operation: requestOperation(c.Request.Method, c.FullPath()), CreatedAt: s.Now().UTC()}
	var payload *invocation.PreparedVideoPayload
	var raw []byte
	if c.Request.Body != nil && !c.GetBool("video_skip_request_body") {
		var err error
		raw, err = io.ReadAll(io.LimitReader(c.Request.Body, ark.DefaultMaxRequestBytes+1))
		if err != nil {
			return &ark.Error{Code: "invalid_request", HTTPStatus: 400}
		}
		c.Request.Body = io.NopCloser(bytes.NewReader(raw))
		if int64(len(raw)) > ark.DefaultMaxRequestBytes {
			return &ark.Error{Code: "invalid_request", HTTPStatus: 413}
		}
	}
	if len(bytes.TrimSpace(raw)) > 0 && json.Valid(raw) {
		var shape struct {
			Model string `json:"model"`
		}
		if json.Unmarshal(raw, &shape) == nil && requestModelName.MatchString(shape.Model) {
			meta.PublicModel = shape.Model
		}
		var policy Policy
		err := s.DB.Where("org_id = ?", org).Take(&policy).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		days := policy.RetentionDays
		if days < 1 || days > 30 {
			if policy.CaptureHistory {
				return ErrCapability
			}
			days = 1
		}
		payload, err = s.Evidence.Prepare(invocation.VideoPayloadInput{OrgID: org, RequestID: rid, Direction: invocation.VideoClientRequest, Purpose: invocation.VideoPurposeHistory, SchemaVersion: 1, Body: raw, CaptureEnabled: policy.CaptureHistory, ExpiresAt: s.Now().UTC().Add(time.Duration(days) * 24 * time.Hour), SecretValues: []string{strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")}, CaptureRepresentation: "received_json_redacted"})
		if err != nil {
			return err
		}
	}
	return s.DB.Transaction(func(tx *gorm.DB) error {
		if err := invocation.PutVideoRequestContextTx(tx, meta); err != nil {
			return err
		}
		if payload == nil {
			return nil
		}
		ref, err := s.Evidence.StageTx(tx, payload)
		if err != nil {
			return err
		}
		return s.Evidence.LinkRequestPayloadTx(tx, org, rid, ref)
	})
}

func (s *Service) linkCreateRequestTx(tx *gorm.DB, org uint64, rid string, t task.Task, st State, operationID string, replay bool) error {
	state := "new"
	if replay {
		state = "replayed"
	}
	return invocation.PutVideoRequestContextTx(tx, invocation.VideoRequestContext{RequestID: rid, OrgID: org, Operation: "create_task", PublicModel: t.Model, TaskID: t.TaskID, OperationID: operationID, IdempotencyRecordID: &st.IdempotencyRecordID, ReplayState: state, OriginRequestID: st.CreateRequestID, CreatedAt: s.Now().UTC()})
}

func (s *Service) finishRequestContextTx(tx *gorm.DB, c *gin.Context) error {
	org := auditRequestOrg(c)
	if org == 0 {
		return nil
	}
	rid := c.GetString("video_request_id")
	var old invocation.VideoRequestContext
	err := tx.Where("org_id = ? AND request_id = ?", org, rid).Take(&old).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if err != nil {
		old = invocation.VideoRequestContext{RequestID: rid, OrgID: org, Operation: requestOperation(c.Request.Method, c.FullPath()), CreatedAt: s.Now().UTC()}
	}
	kind, id := requestResource(c)
	if kind == "task" {
		var t task.Task
		if err := tx.Where("org_id = ? AND task_id = ? AND provider = ?", org, id, Provider).Take(&t).Error; err == nil {
			old.TaskID, old.PublicModel = t.TaskID, t.Model
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
	}
	if opID := c.GetString("video_operation_id"); opID != "" {
		old.OperationID = opID
	}
	old.CreatedAt = s.Now().UTC()
	return invocation.PutVideoRequestContextTx(tx, old)
}

// Direct application calls retain their canonical form. HTTP calls have already
// captured the actual received JSON, so that evidence must never be overwritten.
func (s *Service) ensureClientHistoryTx(tx *gorm.DB, org uint64, rid string, canonical []byte, policy Policy, now time.Time) error {
	var existing invocation.VideoPayload
	err := tx.Where("org_id = ? AND request_id = ? AND direction = ? AND purpose = ? AND attempt_id = ?", org, rid, invocation.VideoClientRequest, invocation.VideoPurposeHistory, "").Take(&existing).Error
	if err == nil {
		return s.Evidence.LinkRequestPayloadTx(tx, org, rid, existing.PayloadID)
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	p, err := s.Evidence.Prepare(invocation.VideoPayloadInput{OrgID: org, RequestID: rid, Direction: invocation.VideoClientRequest, Purpose: invocation.VideoPurposeHistory, SchemaVersion: 1, Body: canonical, ExpiresAt: now.Add(time.Duration(policy.RetentionDays) * 24 * time.Hour), CaptureEnabled: policy.CaptureHistory, CaptureRepresentation: "legacy_canonical_json"})
	if err != nil {
		return err
	}
	ref, err := s.Evidence.StageTx(tx, p)
	if err != nil {
		return err
	}
	return s.Evidence.LinkRequestPayloadTx(tx, org, rid, ref)
}
