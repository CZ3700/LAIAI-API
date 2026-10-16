package video

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

type RequestLogFilter struct {
	RequestID    string     `json:"request_id"`
	TaskID       string     `json:"task_id"`
	Model        string     `json:"model"`
	Operation    string     `json:"operation"`
	TaskStatus   string     `json:"task_status"`
	BillingState string     `json:"billing_state"`
	APIKeyID     uint64     `json:"api_key_id"`
	HTTPStatus   int        `json:"http_status"`
	From         *time.Time `json:"from"`
	To           *time.Time `json:"to"`
}

type RequestLogRow struct {
	RequestID            string     `json:"request_id"`
	TraceID              string     `json:"trace_id"`
	OrgID                uint64     `json:"org_id"`
	UserID               *uint64    `json:"user_id"`
	APIKeyID             *uint64    `json:"api_key_id"`
	Method               string     `json:"method"`
	Route                string     `json:"route"`
	Operation            string     `json:"operation"`
	Model                string     `json:"model"`
	TaskID               string     `json:"task_id"`
	OperationID          string     `json:"operation_id"`
	OriginRequestID      string     `json:"origin_request_id"`
	ReplayState          string     `json:"replay_state"`
	StartedAt            time.Time  `json:"started_at"`
	FinishedAt           *time.Time `json:"finished_at"`
	DurationMS           *int64     `json:"duration_ms" gorm:"-"`
	HTTPStatus           int        `json:"http_status"`
	Outcome              string     `json:"outcome"`
	SafeErrorCode        string     `json:"safe_error_code"`
	TaskStatus           string     `json:"task_status"`
	BillingState         string     `json:"billing_state"`
	ArtifactStatus       string     `json:"artifact_status"`
	DeletedAt            *time.Time `json:"deleted_at"`
	TaskNetChargedAmount *string    `json:"task_net_charged_amount"`
}

type RequestLogPage struct {
	Data       []RequestLogRow `json:"data"`
	NextCursor *string         `json:"next_cursor"`
	AsOf       time.Time       `json:"as_of"`
}

const requestOperationSQL = `COALESCE(NULLIF(x.operation,''),CASE WHEN r.route='/v1/video/generations' AND r.method='POST' THEN 'create_task' WHEN r.route LIKE '%/cancel' THEN 'cancel_task' WHEN r.route LIKE '%/generations/%' AND r.method='DELETE' THEN 'delete_task' WHEN r.route LIKE '%/generations/%' THEN 'get_task' ELSE r.operation END)`
const requestModelSQL = `COALESCE(NULLIF(x.public_model,''),t.model,'')`

func requestLogQuery(db *gorm.DB, org uint64) *gorm.DB {
	return db.Table("api_request_logs AS r").
		Joins("LEFT JOIN video_request_contexts x ON x.request_id=r.request_id AND x.org_id=r.org_id").
		Joins("LEFT JOIN video_task_states origin ON origin.create_request_id=r.request_id AND origin.org_id=r.org_id").
		Joins("LEFT JOIN tasks t ON t.org_id=r.org_id AND t.provider=? AND t.task_id=COALESCE(NULLIF(x.task_id,''),CASE WHEN r.resource_type='task' THEN NULLIF(r.resource_id,'') ELSE NULL END,origin.task_id)", Provider).
		Joins("LEFT JOIN video_task_states st ON st.task_id=t.task_id AND st.org_id=t.org_id").
		Where("r.org_id = ?", org)
}

const requestSelectSQL = `r.request_id,r.trace_id,r.org_id,r.user_id,r.api_key_id,r.method,r.route,` + requestOperationSQL + ` AS operation,` + requestModelSQL + ` AS model,COALESCE(t.task_id,'') AS task_id,COALESCE(x.operation_id,'') AS operation_id,COALESCE(x.origin_request_id,origin.create_request_id,'') AS origin_request_id,COALESCE(x.replay_state,'unknown') AS replay_state,r.started_at,r.finished_at,r.http_status,r.outcome,r.safe_error_code,COALESCE(t.status,'') AS task_status,COALESCE(st.billing_state,'') AS billing_state,COALESCE(st.artifact_status,'') AS artifact_status,st.deleted_at,COALESCE((SELECT a.net_usd FROM video_adjustment_applications a WHERE a.org_id=t.org_id AND a.task_id=t.task_id ORDER BY a.id DESC LIMIT 1),(SELECT b.captured_usd FROM video_billing_entries b WHERE b.org_id=t.org_id AND b.task_id=t.task_id AND b.state IN ('settled','released') ORDER BY b.id DESC LIMIT 1)) AS task_net_charged_amount`

func finishRequestRow(r *RequestLogRow) {
	if r.FinishedAt != nil {
		v := r.FinishedAt.Sub(r.StartedAt).Milliseconds()
		r.DurationMS = &v
	}
	if r.TaskNetChargedAmount != nil {
		if d, e := decimal.NewFromString(*r.TaskNetChargedAmount); e == nil {
			v := d.StringFixed(8)
			r.TaskNetChargedAmount = &v
		}
	}
}

func (s *Service) ListRequestLogs(ctx context.Context, org uint64, f RequestLogFilter, cursor string, limit int) (RequestLogPage, error) {
	out := RequestLogPage{Data: []RequestLogRow{}, AsOf: s.Now().UTC()}
	if org == 0 || limit < 1 || limit > 100 || f.HTTPStatus < 0 || f.HTTPStatus > 599 {
		return out, ErrInvalid
	}
	for _, v := range []string{f.RequestID, f.TaskID, f.Model, f.Operation, f.TaskStatus, f.BillingState} {
		if v != "" && !requestModelName.MatchString(v) {
			return out, ErrInvalid
		}
	}
	if f.From != nil && f.To != nil && !f.To.After(*f.From) {
		return out, ErrInvalid
	}
	filter, _ := json.Marshal(f)
	scope := "request-logs/" + string(filter)
	value, e := s.uncursor(org, scope, cursor)
	if e != nil {
		return out, e
	}
	q := requestLogQuery(s.DB.WithContext(ctx), org)
	for _, v := range []struct{ expr, val string }{{"r.request_id", f.RequestID}, {"t.task_id", f.TaskID}, {requestModelSQL, f.Model}, {requestOperationSQL, f.Operation}, {"t.status", f.TaskStatus}, {"st.billing_state", f.BillingState}} {
		if v.val != "" {
			q = q.Where(v.expr+" = ?", v.val)
		}
	}
	if f.APIKeyID > 0 {
		q = q.Where("r.api_key_id = ?", f.APIKeyID)
	}
	if f.HTTPStatus > 0 {
		q = q.Where("r.http_status = ?", f.HTTPStatus)
	}
	if f.From != nil {
		q = q.Where("r.started_at >= ?", f.From.UTC())
	}
	if f.To != nil {
		q = q.Where("r.started_at < ?", f.To.UTC())
	}
	if value != "" {
		parts := strings.Split(value, "|")
		if len(parts) != 2 {
			return out, ErrInvalid
		}
		ms, e := strconv.ParseInt(parts[0], 10, 64)
		if e != nil {
			return out, ErrInvalid
		}
		tm := time.UnixMilli(ms).UTC()
		q = q.Where("(r.started_at < ? OR (r.started_at = ? AND r.request_id < ?))", tm, tm, parts[1])
	}
	if e = q.Select(requestSelectSQL).Order("r.started_at DESC,r.request_id DESC").Limit(limit + 1).Scan(&out.Data).Error; e != nil {
		return out, e
	}
	if len(out.Data) > limit {
		last := out.Data[limit-1]
		v := s.cursor(org, scope, fmt.Sprintf("%d|%s", last.StartedAt.UnixMilli(), last.RequestID))
		out.NextCursor = &v
		out.Data = out.Data[:limit]
	}
	for i := range out.Data {
		finishRequestRow(&out.Data[i])
	}
	return out, nil
}

func requestLogByID(db *gorm.DB, org uint64, rid string) (RequestLogRow, error) {
	var r RequestLogRow
	if org == 0 || len(rid) > 64 || !requestModelName.MatchString(rid) {
		return r, ErrNotFound
	}
	q := requestLogQuery(db, org).Where("r.request_id = ?", rid).Select(requestSelectSQL).Limit(1).Scan(&r)
	if q.Error != nil {
		return r, q.Error
	}
	if q.RowsAffected != 1 {
		return r, ErrNotFound
	}
	finishRequestRow(&r)
	return r, nil
}

type HistoryBodyInfo struct {
	Availability   string     `json:"availability"`
	Representation string     `json:"representation"`
	ExpiresAt      *time.Time `json:"expires_at"`
	Bytes          int64      `json:"bytes"`
}
type RequestLogTask struct {
	TaskView
	DeletedAt *time.Time `json:"deleted_at"`
}
type RequestBillingEntry struct {
	ID        string    `json:"entry_id"`
	Type      string    `json:"type"`
	Amount    string    `json:"amount"`
	CreatedAt time.Time `json:"created_at"`
}
type RequestBilling struct {
	Currency               string                `json:"currency"`
	State                  string                `json:"state"`
	ReservedAmount         string                `json:"reserved_amount"`
	SettledAmount          *string               `json:"settled_amount"`
	ReleasedAmount         string                `json:"released_amount"`
	NetChargedAmount       *string               `json:"net_charged_amount"`
	PricingVersion         string                `json:"pricing_version"`
	BillingRuleVersion     string                `json:"billing_rule_version"`
	UnitPriceUSD           string                `json:"unit_price_usd_per_1m"`
	EstimatedTokens        int64                 `json:"estimated_tokens"`
	ActualUsage            *UsageView            `json:"actual_usage"`
	InputTokensExplanation string                `json:"input_tokens_explanation"`
	Metering               *RequestMetering      `json:"metering"`
	Entries                []RequestBillingEntry `json:"entries"`
}
type RequestLogDetail struct {
	Request RequestLogRow              `json:"request"`
	Task    *RequestLogTask            `json:"task"`
	Billing *RequestBilling            `json:"billing"`
	Bodies  map[string]HistoryBodyInfo `json:"bodies"`
	AsOf    time.Time                  `json:"as_of"`
}

func (s *Service) RequestLogDetail(ctx context.Context, org uint64, rid string, internal bool) (RequestLogDetail, error) {
	out := RequestLogDetail{AsOf: s.Now().UTC(), Bodies: map[string]HistoryBodyInfo{}}
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var e error
		out.Request, e = requestLogByID(tx, org, rid)
		if e != nil {
			return e
		}
		for _, section := range []string{invocation.VideoClientRequest, invocation.VideoClientResponse, invocation.VideoEffectiveRequest} {
			if section == invocation.VideoEffectiveRequest && !internal {
				out.Bodies[section] = HistoryBodyInfo{Availability: "forbidden"}
				continue
			}
			p, e := s.historyPayload(tx, out.Request, section, "")
			if e != nil && !errors.Is(e, gorm.ErrRecordNotFound) {
				return e
			}
			out.Bodies[section] = s.bodyInfo(p, e)
		}
		if out.Request.TaskID == "" {
			return nil
		}
		var t task.Task
		var st State
		if e = tx.Where("org_id = ? AND task_id = ? AND provider = ?", org, out.Request.TaskID, Provider).Take(&t).Error; e != nil {
			return e
		}
		if e = tx.Where("org_id = ? AND task_id = ?", org, t.TaskID).Take(&st).Error; e != nil {
			return e
		}
		v := taskView(t, st)
		if st.DeletedAt != nil {
			v.ContentURL = ""
			v.DeletionState = "deleted"
		}
		out.Task = &RequestLogTask{TaskView: v, DeletedAt: st.DeletedAt}
		out.Billing, e = requestBilling(tx, org, st)
		return e
	})
	return out, err
}

func requestBilling(tx *gorm.DB, org uint64, st State) (*RequestBilling, error) {
	var snap videobilling.Snapshot
	if e := tx.Where("org_id = ? AND task_id = ?", org, st.TaskID).Take(&snap).Error; e != nil {
		return nil, e
	}
	b := &RequestBilling{Currency: "USD", State: st.BillingState, ReservedAmount: snap.HoldUSD.StringFixed(8), ReleasedAmount: "0.00000000", PricingVersion: snap.RuleVersion, BillingRuleVersion: snap.FormulaVersion, UnitPriceUSD: snap.RetailUSDPer1M.Mul(snap.EffectiveRatio).StringFixed(8), EstimatedTokens: snap.EstimatedTokens, ActualUsage: &UsageView{CompletionTokens: st.ObservedCompletionTokens, TotalTokens: st.TotalTokens}, InputTokensExplanation: "not_separately_metered", Entries: []RequestBillingEntry{{ID: fmt.Sprintf("reserve_%d", snap.ID), Type: "reserve", Amount: snap.HoldUSD.StringFixed(8), CreatedAt: snap.CreatedAt}}}
	var parameters TaskParameters
	if e := tx.Select("parameters_json").Where("org_id = ? AND task_id = ?", org, st.TaskID).Take(&parameters).Error; e != nil && !errors.Is(e, gorm.ErrRecordNotFound) {
		return nil, e
	}
	b.Metering = requestMetering(snap, parameters.ParametersJSON)
	var entries []videobilling.BillingEntry
	if e := tx.Where("org_id = ? AND task_id = ?", org, st.TaskID).Order("id").Find(&entries).Error; e != nil {
		return nil, e
	}
	released := decimal.Zero
	for _, x := range entries {
		if x.State == videobilling.Settled || x.State == videobilling.Released {
			v := x.CapturedUSD.StringFixed(8)
			b.SettledAmount = &v
			b.NetChargedAmount = &v
		}
		if x.State == videobilling.Settled {
			b.Entries = append(b.Entries, RequestBillingEntry{ID: fmt.Sprintf("capture_%d", x.ID), Type: "capture", Amount: x.CapturedUSD.StringFixed(8), CreatedAt: x.CreatedAt})
		}
		released = released.Add(x.ReleasedUSD)
		if x.ReleasedUSD.IsPositive() {
			b.Entries = append(b.Entries, RequestBillingEntry{ID: fmt.Sprintf("release_%d", x.ID), Type: "release", Amount: x.ReleasedUSD.StringFixed(8), CreatedAt: x.CreatedAt})
		}
	}
	var adjustments []videobilling.AdjustmentApplication
	if e := tx.Where("org_id = ? AND task_id = ?", org, st.TaskID).Order("id").Find(&adjustments).Error; e != nil {
		return nil, e
	}
	for _, x := range adjustments {
		v := x.NetUSD.StringFixed(8)
		b.NetChargedAmount = &v
		if x.CompletionTokens != nil {
			b.ActualUsage.CompletionTokens = x.CompletionTokens
		}
		if !x.CustomerDeltaUSD.IsZero() {
			kind := "adjustment_debit"
			if x.CustomerDeltaUSD.IsNegative() {
				kind = "adjustment_credit"
			}
			b.Entries = append(b.Entries, RequestBillingEntry{ID: fmt.Sprintf("adjustment_%d", x.ID), Type: kind, Amount: x.CustomerDeltaUSD.Abs().StringFixed(8), CreatedAt: x.CreatedAt})
		}
	}
	b.ReleasedAmount = released.StringFixed(8)
	return b, nil
}

func (s *Service) historyPayload(db *gorm.DB, r RequestLogRow, section, attemptID string) (invocation.VideoPayload, error) {
	var p invocation.VideoPayload
	q := db.Where("org_id = ? AND purpose = ? AND direction = ?", r.OrgID, invocation.VideoPurposeHistory, section)
	switch section {
	case invocation.VideoClientRequest, invocation.VideoClientResponse:
		if attemptID != "" {
			return p, ErrInvalid
		}
		q = q.Where("request_id = ? AND attempt_id = ?", r.RequestID, "")
	case invocation.VideoEffectiveRequest:
		if attemptID != "" || r.TaskID == "" {
			return p, gorm.ErrRecordNotFound
		}
		var st State
		if e := db.Select("create_request_id").Where("org_id = ? AND task_id = ?", r.OrgID, r.TaskID).Take(&st).Error; e != nil {
			return p, e
		}
		q = q.Where("request_id = ? AND attempt_id = ?", st.CreateRequestID, "")
	case invocation.VideoUpstreamResponse:
		if attemptID == "" || len(attemptID) > 64 || r.TaskID == "" {
			return p, ErrNotFound
		}
		var a Attempt
		if e := db.Where("org_id = ? AND task_id = ? AND attempt_id = ? AND evidence_ref <> ?", r.OrgID, r.TaskID, attemptID, "").Order("id DESC").Take(&a).Error; e != nil {
			return p, e
		}
		q = q.Where("payload_id = ? AND attempt_id = ?", a.EvidenceRef, attemptID)
	default:
		return p, ErrInvalid
	}
	err := q.Order("schema_version DESC").Take(&p).Error
	return p, err
}

func (s *Service) bodyInfo(p invocation.VideoPayload, err error) HistoryBodyInfo {
	if err != nil {
		return HistoryBodyInfo{Availability: "not_captured"}
	}
	v := HistoryBodyInfo{Availability: "available", Representation: p.CaptureRepresentation, ExpiresAt: &p.ExpiresAt, Bytes: p.PlaintextBytes}
	if p.CaptureStatus == invocation.VideoCaptureDisabled {
		v.Availability = "disabled"
	} else if p.CaptureStatus == invocation.VideoCaptureExpired || !p.ExpiresAt.After(s.Now()) {
		v.Availability = "expired"
	}
	return v
}

type RequestPayloadRead struct {
	RequestID      string          `json:"request_id"`
	Section        string          `json:"section"`
	Representation string          `json:"representation"`
	Redacted       bool            `json:"redacted"`
	Body           json.RawMessage `json:"body"`
	ExpiresAt      time.Time       `json:"expires_at"`
}

func (s *Service) ReadRequestHistory(ctx context.Context, org uint64, rid, section, attemptID string, access invocation.VideoAccess) (RequestPayloadRead, error) {
	out := RequestPayloadRead{RequestID: rid, Section: section, Redacted: true}
	if section != invocation.VideoClientRequest && section != invocation.VideoClientResponse && access.ActorRole != invocation.VideoRoleRoot {
		return out, ErrForbidden
	}
	if access.ActorRole != invocation.VideoRoleRoot && access.ActorRole != invocation.VideoRoleOrgReader {
		return out, ErrForbidden
	}
	r, e := requestLogByID(s.DB.WithContext(ctx), org, rid)
	if e != nil {
		return out, e
	}
	p, e := s.historyPayload(s.DB.WithContext(ctx), r, section, attemptID)
	if e != nil {
		return out, e
	}
	plain, e := s.Evidence.ReadForHistory(ctx, org, p.PayloadID, access)
	if e != nil {
		return out, e
	}
	out.Body = plain
	out.Representation = p.CaptureRepresentation
	out.ExpiresAt = p.ExpiresAt
	return out, nil
}

type RequestExecutionRow struct {
	ID            string           `json:"id"`
	AttemptID     string           `json:"attempt_id,omitempty"`
	OperationID   string           `json:"operation_id,omitempty"`
	Type          string           `json:"type"`
	RequestID     string           `json:"request_id"`
	FromStatus    string           `json:"from_status,omitempty"`
	ToStatus      string           `json:"to_status,omitempty"`
	BillingState  string           `json:"billing_state,omitempty"`
	HTTPStatus    int              `json:"http_status,omitempty"`
	SafeErrorCode string           `json:"safe_error_code,omitempty"`
	CreatedAt     time.Time        `json:"created_at"`
	ResponseBody  *HistoryBodyInfo `json:"response_body,omitempty"`
}
type RequestExecutionPage struct {
	Data       []RequestExecutionRow `json:"data"`
	NextCursor *string               `json:"next_cursor"`
}

func (s *Service) RequestExecution(ctx context.Context, org uint64, rid, kind, cursor string, limit int, internal bool) (RequestExecutionPage, error) {
	out := RequestExecutionPage{Data: []RequestExecutionRow{}}
	if limit < 1 || limit > 100 || kind != "events" && kind != "attempts" {
		return out, ErrInvalid
	}
	r, e := requestLogByID(s.DB.WithContext(ctx), org, rid)
	if e != nil {
		return out, e
	}
	scope := "request-execution/" + rid + "/" + kind
	v, e := s.uncursor(org, scope, cursor)
	if e != nil {
		return out, e
	}
	var after uint64
	if v != "" {
		after, e = strconv.ParseUint(v, 10, 64)
		if e != nil {
			return out, ErrInvalid
		}
	}
	if r.TaskID == "" {
		return out, nil
	}
	db := s.DB.WithContext(ctx).Where("org_id = ? AND task_id = ?", org, r.TaskID)
	var last uint64
	if kind == "events" {
		var rows []Event
		if e = db.Where("sequence > ?", after).Order("sequence").Limit(limit + 1).Find(&rows).Error; e != nil {
			return out, e
		}
		if len(rows) > limit {
			last = rows[limit-1].Sequence
			rows = rows[:limit]
		}
		for _, x := range rows {
			out.Data = append(out.Data, RequestExecutionRow{ID: x.ID, Type: x.Type, RequestID: x.RequestID, FromStatus: x.FromStatus, ToStatus: x.ToStatus, BillingState: x.BillingState, CreatedAt: x.CreatedAt})
		}
	} else {
		var rows []Attempt
		if e = db.Where("id > ?", after).Order("id").Limit(limit + 1).Find(&rows).Error; e != nil {
			return out, e
		}
		if len(rows) > limit {
			last = rows[limit-1].ID
			rows = rows[:limit]
		}
		for _, x := range rows {
			v := RequestExecutionRow{ID: fmt.Sprint(x.ID), AttemptID: x.AttemptID, OperationID: x.OperationID, Type: x.Phase, RequestID: x.RequestID, HTTPStatus: x.HTTPStatus, SafeErrorCode: x.ErrorCode, CreatedAt: x.ObservedAt}
			if internal && strings.Contains(x.Phase, "response") {
				p, e := s.historyPayload(s.DB.WithContext(ctx), r, invocation.VideoUpstreamResponse, x.AttemptID)
				if e != nil && !errors.Is(e, gorm.ErrRecordNotFound) {
					return out, e
				}
				info := s.bodyInfo(p, e)
				v.ResponseBody = &info
			}
			out.Data = append(out.Data, v)
		}
	}
	if last > 0 {
		next := s.cursor(org, scope, fmt.Sprint(last))
		out.NextCursor = &next
	}
	return out, nil
}
