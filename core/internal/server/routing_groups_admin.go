package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/routinggroup"
)

var errRoutingAdminBusy = errors.New("routing group read capacity exceeded")

func (s *Server) registerRoutingGroupsAdmin(parent *gin.RouterGroup) {
	g := parent.Group("/routing-groups", requireNamedRoutingAdmin)
	g.GET("", s.routingAdminList)
	g.POST("", s.routingAdminCreate)
	g.GET("/:id", s.routingAdminGet)
	g.PATCH("/:id", s.routingAdminPatch)
	g.GET("/:id/channels", s.routingAdminBindings)
	g.PUT("/:id/channels/:channel_id", s.routingAdminBind)
	g.DELETE("/:id/channels/:channel_id", s.routingAdminUnbind)
	g.POST("/:id/impact-preview", s.routingAdminPreview)
	s.registerRoutingGroupsOrgAdmin(parent)
}

type routingAdminGroupDTO struct {
	ID            uint64    `json:"id"`
	OwnerOrgID    uint64    `json:"owner_org_id"`
	Slug          string    `json:"slug"`
	Name          string    `json:"name"`
	Description   string    `json:"description"`
	Status        string    `json:"status"`
	PolicyVersion uint64    `json:"policy_version"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func routingAdminGroup(g routinggroup.Group) routingAdminGroupDTO {
	return routingAdminGroupDTO{g.ID, g.OwnerOrgID, g.Slug, g.Name, g.Description, g.Status, g.PolicyVersion, g.CreatedAt, g.UpdatedAt}
}

type routingAdminChannelDTO struct {
	ID         uint64 `json:"id"`
	OwnerOrgID uint64 `json:"owner_org_id"`
	Name       string `json:"name"`
	Provider   string `json:"provider"`
	Status     int8   `json:"status"`
}

func routingAdminChannel(channel *routinggroup.AdminChannel) *routingAdminChannelDTO {
	if channel == nil {
		return nil
	}
	return &routingAdminChannelDTO{channel.ID, channel.OwnerOrgID, channel.Name, channel.Provider, channel.Status}
}

type routingAdminBindingDTO struct {
	GroupID        uint64                  `json:"group_id"`
	ChannelID      uint64                  `json:"channel_id"`
	CreatedAt      time.Time               `json:"created_at"`
	Channel        *routingAdminChannelDTO `json:"channel"`
	ChannelExists  bool                    `json:"channel_exists"`
	OwnershipValid bool                    `json:"ownership_valid"`
	ChannelEnabled bool                    `json:"channel_enabled"`
}

func routingAdminSuccess(c *gin.Context, status int, body gin.H) {
	body["grouped_key_issuance_enabled"] = false
	c.JSON(status, body)
}

func routingAdminFailure(c *gin.Context, err error) {
	status, code, message := http.StatusServiceUnavailable, "routing_group_unavailable", "routing group service unavailable"
	switch {
	case errors.Is(err, errRoutingAdminBusy):
		status, code, message = http.StatusTooManyRequests, "routing_group_capacity_exceeded", "routing group read capacity exceeded"
		c.Header("Retry-After", "1")
	case errors.Is(err, errRoutingAdminInput), errors.Is(err, routinggroup.ErrInvalid):
		status, code, message = http.StatusBadRequest, "invalid_request", "invalid routing group request"
	case errors.Is(err, routinggroup.ErrConflict):
		status, code, message = http.StatusConflict, "policy_version_conflict", "routing group policy version conflict"
	case errors.Is(err, routinggroup.ErrExists):
		status, code, message = http.StatusConflict, "routing_group_exists", "routing group already exists"
	case errors.Is(err, routinggroup.ErrNotFound):
		status, code, message = http.StatusNotFound, "not_found", "routing group resource not found"
	case errors.Is(err, routinggroup.ErrOwnership), errors.Is(err, routinggroup.ErrInactive):
		status, code, message = http.StatusForbidden, "routing_group_access_denied", "routing group resource access denied"
	}
	c.AbortWithStatusJSON(status, gin.H{"error": message, "code": code})
}

func routingAdminPage(values url.Values) (routinggroup.Page, error) {
	p := routinggroup.Page{Limit: 20}
	var err error
	if raw, exists := values["cursor"]; exists {
		p.Cursor, err = routingAdminUint(raw[0], true)
		if err != nil {
			return p, err
		}
	}
	if raw, exists := values["limit"]; exists {
		limit, e := routingAdminUint(raw[0], false)
		if e != nil || limit > 100 {
			return p, errRoutingAdminInput
		}
		p.Limit = int(limit)
	}
	return p, nil
}

func (s *Server) routingAdminList(c *gin.Context) {
	values, err := routingAdminQuery(c, "owner_org_id", "status", "cursor", "limit")
	if err != nil {
		routingAdminFailure(c, err)
		return
	}
	page, err := routingAdminPage(values)
	if err != nil {
		routingAdminFailure(c, err)
		return
	}
	filter := routinggroup.GroupFilter{Status: values.Get("status")}
	if raw, ok := values["owner_org_id"]; ok {
		owner, e := routingAdminUint(raw[0], true)
		if e != nil {
			routingAdminFailure(c, e)
			return
		}
		filter.OwnerOrgID = &owner
	}
	if _, ok := values["status"]; ok && filter.Status != routinggroup.Draft && filter.Status != routinggroup.Enabled && filter.Status != routinggroup.Disabled {
		routingAdminFailure(c, errRoutingAdminInput)
		return
	}
	var result routinggroup.GroupPage
	err = s.routingAdminRead(c, func(ctx context.Context) error {
		var e error
		result, e = s.routingAdminReader().ListGroups(ctx, filter, page)
		return e
	})
	if err != nil {
		routingAdminFailure(c, err)
		return
	}
	items := make([]routingAdminGroupDTO, 0, len(result.Items))
	for _, row := range result.Items {
		items = append(items, routingAdminGroup(row))
	}
	routingAdminSuccess(c, http.StatusOK, gin.H{"items": items, "next_cursor": result.NextCursor, "has_more": result.HasMore})
}

func (s *Server) routingAdminGetGroup(c *gin.Context, id uint64) (routinggroup.Group, error) {
	var group routinggroup.Group
	err := s.routingAdminRead(c, func(ctx context.Context) error {
		var e error
		group, e = s.routingAdminReader().GetGroup(ctx, id)
		return e
	})
	return group, err
}

func (s *Server) routingAdminGet(c *gin.Context) {
	if _, err := routingAdminQuery(c); err != nil {
		routingAdminFailure(c, err)
		return
	}
	id, err := routingAdminUint(c.Param("id"), false)
	if err != nil {
		routingAdminFailure(c, err)
		return
	}
	group, err := s.routingAdminGetGroup(c, id)
	if err != nil {
		routingAdminFailure(c, err)
		return
	}
	routingAdminSuccess(c, http.StatusOK, gin.H{"group": routingAdminGroup(group)})
}

func routingAdminAudit(c *gin.Context) routinggroup.Audit {
	// Never accept X-Request-ID as audit identity; it is user-controlled.
	return routinggroup.Audit{ActorUserID: routingAdminActor(c), RequestID: c.GetString("routing_admin_request_id")}
}

func (s *Server) routingAdminCreate(c *gin.Context) {
	if _, err := routingAdminQuery(c); err != nil {
		routingAdminFailure(c, err)
		return
	}
	var input struct {
		OwnerOrgID  *uint64 `json:"owner_org_id"`
		Slug        *string `json:"slug"`
		Name        *string `json:"name"`
		Description string  `json:"description"`
	}
	err := routingAdminJSON(c, &input, "owner_org_id", "slug", "name", "description")
	if err != nil || input.OwnerOrgID == nil || input.Slug == nil || input.Name == nil {
		routingAdminFailure(c, errRoutingAdminInput)
		return
	}
	group, err := s.routingAdminWriter().Create(c.Request.Context(), routinggroup.CreateInput{OwnerOrgID: *input.OwnerOrgID, Slug: *input.Slug, Name: *input.Name, Description: input.Description, Audit: routingAdminAudit(c)})
	if err != nil {
		routingAdminFailure(c, err)
		return
	}
	routingAdminSuccess(c, http.StatusCreated, gin.H{"group": routingAdminGroup(group)})
}

type routingAdminPatchInput struct {
	ExpectedVersion *uint64 `json:"expected_version"`
	Name            *string `json:"name"`
	Description     *string `json:"description"`
	Status          *string `json:"status"`
}

func (s *Server) routingAdminPatch(c *gin.Context) {
	if _, err := routingAdminQuery(c); err != nil {
		routingAdminFailure(c, err)
		return
	}
	id, err := routingAdminUint(c.Param("id"), false)
	if err != nil {
		routingAdminFailure(c, err)
		return
	}
	var input routingAdminPatchInput
	err = routingAdminJSON(c, &input, "expected_version", "name", "description", "status")
	if err != nil || input.ExpectedVersion == nil || *input.ExpectedVersion == 0 || input.Name == nil && input.Description == nil && input.Status == nil {
		routingAdminFailure(c, errRoutingAdminInput)
		return
	}
	group, err := s.routingAdminGetGroup(c, id)
	if err != nil {
		routingAdminFailure(c, err)
		return
	}
	// A future client version must not match a concurrent write after this
	// snapshot and thereby restore stale fields omitted from the PATCH.
	if group.PolicyVersion != *input.ExpectedVersion {
		routingAdminFailure(c, routinggroup.ErrConflict)
		return
	}
	if input.Name != nil {
		group.Name = *input.Name
	}
	if input.Description != nil {
		group.Description = *input.Description
	}
	if input.Status != nil {
		group.Status = *input.Status
	}
	group, changed, err := s.routingAdminWriter().Update(c.Request.Context(), routinggroup.UpdateInput{OwnerOrgID: group.OwnerOrgID, GroupID: group.ID, ExpectedVersion: *input.ExpectedVersion, Name: group.Name, Description: group.Description, Status: group.Status, Audit: routingAdminAudit(c)})
	if err != nil {
		routingAdminFailure(c, err)
		return
	}
	routingAdminSuccess(c, http.StatusOK, gin.H{"group": routingAdminGroup(group), "changed": changed})
}

func (s *Server) routingAdminBindings(c *gin.Context) {
	values, err := routingAdminQuery(c, "cursor", "limit")
	if err != nil {
		routingAdminFailure(c, err)
		return
	}
	id, err := routingAdminUint(c.Param("id"), false)
	if err != nil {
		routingAdminFailure(c, err)
		return
	}
	page, err := routingAdminPage(values)
	if err != nil {
		routingAdminFailure(c, err)
		return
	}
	var result routinggroup.BindingPage
	err = s.routingAdminRead(c, func(ctx context.Context) error {
		var e error
		result, e = s.routingAdminReader().ListBindings(ctx, id, page)
		return e
	})
	if err != nil {
		routingAdminFailure(c, err)
		return
	}
	items := make([]routingAdminBindingDTO, 0, len(result.Items))
	for _, row := range result.Items {
		items = append(items, routingAdminBindingDTO{row.Binding.GroupID, row.Binding.ChannelID, row.Binding.CreatedAt, routingAdminChannel(row.Channel), row.ChannelExists, row.OwnershipValid, row.ChannelEnabled})
	}
	routingAdminSuccess(c, http.StatusOK, gin.H{"group": routingAdminGroup(result.Group), "items": items, "next_cursor": result.NextCursor, "has_more": result.HasMore})
}

func (s *Server) routingAdminBind(c *gin.Context)   { s.routingAdminSetBinding(c, true) }
func (s *Server) routingAdminUnbind(c *gin.Context) { s.routingAdminSetBinding(c, false) }

func (s *Server) routingAdminSetBinding(c *gin.Context, bound bool) {
	if _, err := routingAdminQuery(c); err != nil {
		routingAdminFailure(c, err)
		return
	}
	id, err := routingAdminUint(c.Param("id"), false)
	if err != nil {
		routingAdminFailure(c, err)
		return
	}
	channel, err := routingAdminUint(c.Param("channel_id"), false)
	if err != nil {
		routingAdminFailure(c, err)
		return
	}
	var input struct {
		ExpectedVersion *uint64 `json:"expected_group_version"`
	}
	err = routingAdminJSON(c, &input, "expected_group_version")
	if err != nil || input.ExpectedVersion == nil || *input.ExpectedVersion == 0 {
		routingAdminFailure(c, errRoutingAdminInput)
		return
	}
	group, err := s.routingAdminGetGroup(c, id)
	if err != nil {
		routingAdminFailure(c, err)
		return
	}
	group, changed, err := s.routingAdminWriter().SetBinding(c.Request.Context(), routinggroup.BindingInput{OwnerOrgID: group.OwnerOrgID, GroupID: id, ChannelID: channel, ExpectedVersion: *input.ExpectedVersion, Bound: bound, Audit: routingAdminAudit(c)})
	if err != nil {
		routingAdminFailure(c, err)
		return
	}
	routingAdminSuccess(c, http.StatusOK, gin.H{"group": routingAdminGroup(group), "channel_id": channel, "bound": bound, "changed": changed})
}

func (s *Server) routingAdminPreview(c *gin.Context) {
	if _, err := routingAdminQuery(c); err != nil {
		routingAdminFailure(c, err)
		return
	}
	id, err := routingAdminUint(c.Param("id"), false)
	if err != nil {
		routingAdminFailure(c, err)
		return
	}
	var input struct {
		ExpectedVersion *uint64         `json:"expected_version"`
		Change          json.RawMessage `json:"change"`
	}
	if err := routingAdminJSON(c, &input, "expected_version", "change"); err != nil || input.ExpectedVersion == nil || *input.ExpectedVersion == 0 {
		routingAdminFailure(c, errRoutingAdminInput)
		return
	}
	var change struct {
		Kind        string  `json:"kind"`
		Name        *string `json:"name"`
		Description *string `json:"description"`
		Status      *string `json:"status"`
		ChannelID   *uint64 `json:"channel_id"`
	}
	if err := routingAdminObject(input.Change, &change, "kind", "name", "description", "status", "channel_id"); err != nil {
		routingAdminFailure(c, err)
		return
	}
	var channelID uint64
	switch change.Kind {
	case "update":
		if change.ChannelID != nil || change.Name == nil && change.Description == nil && change.Status == nil {
			routingAdminFailure(c, errRoutingAdminInput)
			return
		}
	case "bind", "unbind":
		if change.ChannelID == nil || *change.ChannelID == 0 || change.Name != nil || change.Description != nil || change.Status != nil {
			routingAdminFailure(c, errRoutingAdminInput)
			return
		}
		channelID = *change.ChannelID
	default:
		routingAdminFailure(c, errRoutingAdminInput)
		return
	}
	var result routinggroup.GroupPreview
	err = s.routingAdminRead(c, func(ctx context.Context) error {
		var e error
		result, e = s.routingAdminReader().Preview(ctx, id, routinggroup.PreviewInput{ExpectedVersion: *input.ExpectedVersion, Change: routinggroup.PreviewChange{Kind: change.Kind, Name: change.Name, Description: change.Description, Status: change.Status, ChannelID: channelID}})
		return e
	})
	if err != nil {
		routingAdminFailure(c, err)
		return
	}
	normalized := gin.H{"kind": result.Change.Kind}
	if result.Change.Kind == "update" {
		normalized["name"] = result.Change.Name
		normalized["description"] = result.Change.Description
		normalized["status"] = result.Change.Status
	} else {
		normalized["channel_id"] = result.Change.ChannelID
	}
	count := func(value routinggroup.ObservedCount) gin.H {
		return gin.H{"observed_count": value.ObservedCount, "exact": value.Exact, "lower_bound": value.LowerBound, "limit": value.Limit}
	}
	routingAdminSuccess(c, http.StatusOK, gin.H{"preview_kind": "structure_only", "observed_group_version": result.ObservedGroupVersion, "group": routingAdminGroup(result.Group), "proposed_group": routingAdminGroup(result.ProposedGroup), "change": normalized, "changed": result.Changed, "channel": routingAdminChannel(result.Channel), "was_bound": result.WasBound, "will_be_bound": result.WillBeBound, "effective_model_impact_available": false, "counts": gin.H{"bindings": count(result.Counts.Bindings), "grants": count(result.Counts.Grants), "enabled_grants": count(result.Counts.EnabledGrants), "default_pointers": count(result.Counts.DefaultPointers)}})
}
