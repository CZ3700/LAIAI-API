package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/routinggroup"
)

func (s *Server) registerRoutingGroupsOrgAdmin(parent *gin.RouterGroup) {
	g := parent.Group("/orgs/:id", requireNamedRoutingAdmin)
	g.GET("/routing-groups", s.routingAdminOrgGrants)
	g.PUT("/routing-groups/:group_id", s.routingAdminOrgSetGrant)
	g.GET("/routing-default", s.routingAdminOrgDefault)
	g.PUT("/routing-default", s.routingAdminOrgSetDefault)
	g.POST("/routing-groups/:group_id/impact-preview", s.routingAdminOrgGrantPreview)
	g.POST("/routing-default/impact-preview", s.routingAdminOrgDefaultPreview)
}

type routingAdminGrantDTO struct {
	ID           uint64 `json:"id"`
	OrgID        uint64 `json:"org_id"`
	GroupID      uint64 `json:"group_id"`
	Status       string `json:"status"`
	GrantVersion uint64 `json:"grant_version"`
	IsDefault    bool   `json:"is_default"`
}

func routingAdminGrant(grant routinggroup.Grant) routingAdminGrantDTO {
	return routingAdminGrantDTO{grant.ID, grant.OrgID, grant.GroupID, grant.Status, grant.GrantVersion, grant.DefaultSlot != nil && *grant.DefaultSlot == 1}
}

type routingAdminOrgGroupDTO struct {
	ID            uint64 `json:"id"`
	OwnerOrgID    uint64 `json:"owner_org_id"`
	Slug          string `json:"slug"`
	Name          string `json:"name"`
	Status        string `json:"status"`
	PolicyVersion uint64 `json:"policy_version"`
}

func routingAdminOrgGroup(group *routinggroup.Group) *routingAdminOrgGroupDTO {
	if group == nil {
		return nil
	}
	return &routingAdminOrgGroupDTO{group.ID, group.OwnerOrgID, group.Slug, group.Name, group.Status, group.PolicyVersion}
}

type routingAdminDefaultDTO struct {
	GroupID            uint64   `json:"group_id"`
	GrantVersion       uint64   `json:"grant_version"`
	GroupPolicyVersion *uint64  `json:"group_policy_version"`
	DefaultValid       bool     `json:"default_valid"`
	InvalidReasons     []string `json:"invalid_reasons"`
}

func routingAdminDefault(value *routinggroup.DefaultView) *routingAdminDefaultDTO {
	if value == nil {
		return nil
	}
	reasons := append([]string{}, value.InvalidReasons...)
	return &routingAdminDefaultDTO{value.GroupID, value.GrantVersion, value.GroupPolicyVersion, value.Valid, reasons}
}

type routingAdminOrgGrantDTO struct {
	Grant              routingAdminGrantDTO     `json:"grant"`
	Group              *routingAdminOrgGroupDTO `json:"group"`
	GroupExists        bool                     `json:"group_exists"`
	OwnershipValid     bool                     `json:"ownership_valid"`
	ConfigurationValid bool                     `json:"configuration_valid"`
}

func routingAdminOrganization(org routinggroup.Organization) gin.H {
	return gin.H{"id": org.ID, "active": org.Active}
}

func (s *Server) routingAdminOrgGrants(c *gin.Context) {
	values, err := routingAdminQuery(c, "status", "cursor", "limit")
	if err != nil {
		routingAdminFailure(c, err)
		return
	}
	org, err := routingAdminUint(c.Param("id"), false)
	if err != nil {
		routingAdminFailure(c, err)
		return
	}
	page, err := routingAdminPage(values)
	if err != nil {
		routingAdminFailure(c, err)
		return
	}
	filter := routinggroup.OrgGrantFilter{Status: values.Get("status")}
	if values.Has("status") && filter.Status != routinggroup.Enabled && filter.Status != routinggroup.Revoked {
		routingAdminFailure(c, errRoutingAdminInput)
		return
	}
	var result routinggroup.OrgGrantPage
	err = s.routingAdminRead(c, func(ctx context.Context) error {
		var err error
		result, err = s.routingAdminReader().ListOrgGrants(ctx, org, filter, page)
		return err
	})
	if err != nil {
		routingAdminFailure(c, err)
		return
	}
	items := make([]routingAdminOrgGrantDTO, 0, len(result.Items))
	for _, row := range result.Items {
		items = append(items, routingAdminOrgGrantDTO{routingAdminGrant(row.Grant), routingAdminOrgGroup(row.Group), row.GroupExists, row.OwnershipValid, row.ConfigurationValid})
	}
	routingAdminSuccess(c, http.StatusOK, gin.H{"organization": routingAdminOrganization(result.Organization), "items": items, "next_cursor": result.NextCursor, "has_more": result.HasMore, "current_default": routingAdminDefault(result.Default)})
}

func (s *Server) routingAdminOrgDefault(c *gin.Context) {
	if _, err := routingAdminQuery(c); err != nil {
		routingAdminFailure(c, err)
		return
	}
	org, err := routingAdminUint(c.Param("id"), false)
	if err != nil {
		routingAdminFailure(c, err)
		return
	}
	var result routinggroup.OrgDefaultView
	err = s.routingAdminRead(c, func(ctx context.Context) error {
		var err error
		result, err = s.routingAdminReader().GetOrgDefault(ctx, org)
		return err
	})
	if err != nil {
		routingAdminFailure(c, err)
		return
	}
	routingAdminSuccess(c, http.StatusOK, gin.H{"organization": routingAdminOrganization(result.Organization), "current_default": routingAdminDefault(result.Default)})
}

func routingAdminOrgGrantInput(c *gin.Context) (routinggroup.GrantInput, error) {
	var out routinggroup.GrantInput
	if _, err := routingAdminQuery(c); err != nil {
		return out, err
	}
	org, err := routingAdminUint(c.Param("id"), false)
	if err != nil {
		return out, err
	}
	group, err := routingAdminUint(c.Param("group_id"), false)
	if err != nil {
		return out, err
	}
	var input struct {
		ExpectedVersion *uint64 `json:"expected_grant_version"`
		Status          string  `json:"status"`
	}
	if err := routingAdminJSON(c, &input, "expected_grant_version", "status"); err != nil || input.ExpectedVersion == nil || input.Status != routinggroup.Enabled && input.Status != routinggroup.Revoked {
		return out, errRoutingAdminInput
	}
	return routinggroup.GrantInput{OrgID: org, GroupID: group, ExpectedVersion: *input.ExpectedVersion, Status: input.Status}, nil
}

func (s *Server) routingAdminOrgSetGrant(c *gin.Context) {
	input, err := routingAdminOrgGrantInput(c)
	if err != nil {
		routingAdminFailure(c, err)
		return
	}
	input.Audit = routingAdminAudit(c)
	grant, changed, err := s.routingAdminWriter().SetGrant(c.Request.Context(), input)
	if err != nil {
		routingAdminFailure(c, err)
		return
	}
	routingAdminSuccess(c, http.StatusOK, gin.H{"org_id": input.OrgID, "grant": routingAdminGrant(grant), "changed": changed})
}

// RawMessage presence distinguishes an absent member from an explicitly null
// current pointer. Only after every field is validated do we construct the
// existing domain command; no read fills in missing client CAS facts.
func routingAdminOrgDefaultInput(c *gin.Context) (routinggroup.DefaultInput, error) {
	var out routinggroup.DefaultInput
	if _, err := routingAdminQuery(c); err != nil {
		return out, err
	}
	org, err := routingAdminUint(c.Param("id"), false)
	if err != nil {
		return out, err
	}
	var values map[string]json.RawMessage
	if err := routingAdminJSONNullable(c, &values, map[string]bool{"/target_group_id": true, "/expected_current_default": true}, "target_group_id", "expected_current_default", "expected_target_grant_version"); err != nil {
		return out, err
	}
	target, hasTarget := values["target_group_id"]
	current, hasCurrent := values["expected_current_default"]
	if !hasTarget || !hasCurrent {
		return out, errRoutingAdminInput
	}
	out.OrgID = org
	if !bytes.Equal(bytes.TrimSpace(target), []byte("null")) {
		var id uint64
		if json.Unmarshal(target, &id) != nil || id == 0 {
			return routinggroup.DefaultInput{}, errRoutingAdminInput
		}
		out.GroupID = &id
	}
	if !bytes.Equal(bytes.TrimSpace(current), []byte("null")) {
		var observed struct {
			GroupID      *uint64 `json:"group_id"`
			GrantVersion *uint64 `json:"grant_version"`
		}
		if routingAdminObject(current, &observed, "group_id", "grant_version") != nil || observed.GroupID == nil || *observed.GroupID == 0 || observed.GrantVersion == nil || *observed.GrantVersion == 0 {
			return routinggroup.DefaultInput{}, errRoutingAdminInput
		}
		out.ExpectedGroupID, out.ExpectedCurrentVersion = observed.GroupID, *observed.GrantVersion
	}
	version, hasVersion := values["expected_target_grant_version"]
	if out.GroupID == nil {
		if hasVersion {
			return routinggroup.DefaultInput{}, errRoutingAdminInput
		}
	} else if !hasVersion || json.Unmarshal(version, &out.ExpectedTargetVersion) != nil || out.ExpectedTargetVersion == 0 {
		return routinggroup.DefaultInput{}, errRoutingAdminInput
	}
	return out, nil
}

func (s *Server) routingAdminOrgSetDefault(c *gin.Context) {
	input, err := routingAdminOrgDefaultInput(c)
	if err != nil {
		routingAdminFailure(c, err)
		return
	}
	input.Audit = routingAdminAudit(c)
	changed, err := s.routingAdminWriter().SetDefault(c.Request.Context(), input)
	if err != nil {
		routingAdminFailure(c, err)
		return
	}
	// This is the committed command's target, not a claim about a later read.
	routingAdminSuccess(c, http.StatusOK, gin.H{"org_id": input.OrgID, "applied_target_group_id": input.GroupID, "changed": changed})
}

type routingAdminDefaultPointerDTO struct {
	GroupID      uint64 `json:"group_id"`
	GrantVersion uint64 `json:"grant_version"`
}

func routingAdminDefaultPointer(value *routinggroup.DefaultPointer) *routingAdminDefaultPointerDTO {
	if value == nil {
		return nil
	}
	return &routingAdminDefaultPointerDTO{value.GroupID, value.GrantVersion}
}

type routingAdminObservedGrantDTO struct {
	GroupID uint64                   `json:"group_id"`
	Exists  bool                     `json:"exists"`
	Grant   *routingAdminGrantDTO    `json:"grant"`
	Group   *routingAdminOrgGroupDTO `json:"group"`
}

type routingAdminAffectedGrantDTO struct {
	GroupID     uint64   `json:"group_id"`
	GrantID     *uint64  `json:"grant_id,omitempty"`
	Actions     []string `json:"actions"`
	FromVersion uint64   `json:"from_version"`
	ToVersion   uint64   `json:"to_version"`
	Status      string   `json:"status"`
	IsDefault   bool     `json:"is_default"`
}

func routingAdminOrgPreviewBody(org uint64, changed bool, change gin.H, current *routinggroup.DefaultPointer, observed []routinggroup.ObservedOrgGrant, affected []routinggroup.AffectedOrgGrant) gin.H {
	observations := make([]routingAdminObservedGrantDTO, 0, len(observed))
	for _, row := range observed {
		var grant *routingAdminGrantDTO
		if row.Grant != nil {
			value := routingAdminGrant(*row.Grant)
			grant = &value
		}
		observations = append(observations, routingAdminObservedGrantDTO{row.GroupID, row.Exists, grant, routingAdminOrgGroup(&row.Group)})
	}
	effects := make([]routingAdminAffectedGrantDTO, 0, len(affected))
	for _, row := range affected {
		effects = append(effects, routingAdminAffectedGrantDTO{row.GroupID, row.GrantID, append([]string{}, row.Actions...), row.FromVersion, row.ToVersion, row.Status, row.IsDefault})
	}
	return gin.H{"preview_kind": "structure_only", "org_id": org, "changed": changed, "change": change, "observed_current_default": routingAdminDefaultPointer(current), "observed_grants": observations, "affected_grants": effects, "effective_model_impact_available": false}
}

func (s *Server) routingAdminOrgGrantPreview(c *gin.Context) {
	input, err := routingAdminOrgGrantInput(c)
	if err != nil {
		routingAdminFailure(c, err)
		return
	}
	var result routinggroup.OrgGrantPreview
	err = s.routingAdminRead(c, func(ctx context.Context) error {
		var err error
		result, err = s.routingAdminReader().PreviewGrant(ctx, routinggroup.GrantPreviewInput{OrgID: input.OrgID, GroupID: input.GroupID, ExpectedVersion: input.ExpectedVersion, Status: input.Status})
		return err
	})
	if err != nil {
		routingAdminFailure(c, err)
		return
	}
	change := gin.H{"expected_grant_version": result.Change.ExpectedVersion, "status": result.Change.Status}
	routingAdminSuccess(c, http.StatusOK, routingAdminOrgPreviewBody(result.Organization.ID, result.Changed, change, result.ObservedCurrentDefault, result.ObservedGrants, result.AffectedGrants))
}

func (s *Server) routingAdminOrgDefaultPreview(c *gin.Context) {
	input, err := routingAdminOrgDefaultInput(c)
	if err != nil {
		routingAdminFailure(c, err)
		return
	}
	var result routinggroup.OrgDefaultPreview
	err = s.routingAdminRead(c, func(ctx context.Context) error {
		var err error
		result, err = s.routingAdminReader().PreviewDefault(ctx, routinggroup.DefaultPreviewInput{OrgID: input.OrgID, GroupID: input.GroupID, ExpectedGroupID: input.ExpectedGroupID, ExpectedCurrentVersion: input.ExpectedCurrentVersion, ExpectedTargetVersion: input.ExpectedTargetVersion})
		return err
	})
	if err != nil {
		routingAdminFailure(c, err)
		return
	}
	var expected *routingAdminDefaultPointerDTO
	if result.Change.ExpectedGroupID != nil {
		expected = &routingAdminDefaultPointerDTO{*result.Change.ExpectedGroupID, result.Change.ExpectedCurrentVersion}
	}
	change := gin.H{"target_group_id": result.Change.GroupID, "expected_current_default": expected}
	if result.Change.GroupID != nil {
		change["expected_target_grant_version"] = result.Change.ExpectedTargetVersion
	}
	routingAdminSuccess(c, http.StatusOK, routingAdminOrgPreviewBody(result.Organization.ID, result.Changed, change, result.ObservedCurrentDefault, result.ObservedGrants, result.AffectedGrants))
}
