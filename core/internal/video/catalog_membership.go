package video

import (
	"context"
	"errors"
	"time"

	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/relay"
	"github.com/laiai/laiai-next/core/internal/routinggroup"
	"gorm.io/gorm"
)

type officialActionSubject struct {
	membership routinggroup.MembershipObservation
	facts      routinggroup.CandidateContextFacts
}

func (s officialActionSubject) allowsModel(model string) bool {
	m, err := s.membership.ResolveModel(model)
	return err == nil && m.ModelIncluded
}

type officialCandidateSubjectBridge struct{}
type officialPublishedChannelBridge struct{}

func officialMembershipBridgeError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	switch {
	case errors.Is(err, identity.ErrRoutingCandidateInvalid), errors.Is(err, relay.ErrRoutingPublishedInvalid):
		return routinggroup.ErrCandidatesInvalid
	case errors.Is(err, identity.ErrRoutingCandidateFacts), errors.Is(err, relay.ErrRoutingPublishedFacts):
		return routinggroup.ErrCandidatesInvalidFacts
	case errors.Is(err, identity.ErrRoutingCandidateLimit), errors.Is(err, relay.ErrRoutingPublishedLimit):
		return routinggroup.ErrCandidatesLimit
	default:
		return routinggroup.ErrCandidatesUnavailable
	}
}

func (officialCandidateSubjectBridge) ReadCandidateSubject(ctx context.Context, tx *gorm.DB, ref routinggroup.CandidateSubjectRef, at time.Time) (routinggroup.CandidateSubjectFacts, error) {
	f, err := identity.ReadRoutingCandidateSubjectTx(ctx, tx, ref.OrgID, ref.KeyID, at)
	if err != nil {
		return routinggroup.CandidateSubjectFacts{}, officialMembershipBridgeError(err)
	}
	var groupID *uint64
	if f.RoutingGroupID != nil {
		id := *f.RoutingGroupID
		groupID = &id
	}
	return routinggroup.CandidateSubjectFacts{Found: f.Found, OrgID: f.OrgID, KeyID: f.KeyID, KeyActive: f.KeyActive, KeyExpired: f.KeyExpired, PrincipalActive: f.PrincipalActive, Mode: f.Mode, RoutingGroupID: groupID, KeyPolicyVersion: f.KeyPolicyVersion, ModelLimits: f.ModelLimits, KeyScopes: f.KeyScopes, FeatureImages: f.FeatureImages, FeatureVideo: f.FeatureVideo}, nil
}

func (officialPublishedChannelBridge) ReadPublishedChannels(ctx context.Context, tx *gorm.DB, ids []uint64) ([]routinggroup.PublishedChannelFacts, error) {
	rows, err := relay.ReadRoutingPublishedChannelsTx(ctx, tx, ids)
	if err != nil {
		return nil, officialMembershipBridgeError(err)
	}
	out := make([]routinggroup.PublishedChannelFacts, 0, len(rows))
	for _, row := range rows {
		out = append(out, routinggroup.PublishedChannelFacts{ID: row.ID, OwnerOrgID: row.OwnerOrgID, Enabled: row.Enabled, Models: append([]string{}, row.Models...)})
	}
	return out, nil
}

func (s *Service) readOfficialActionSubjectTx(ctx context.Context, tx *gorm.DB, ref CatalogSubjectRef) (officialActionSubject, error) {
	var zero officialActionSubject
	if s == nil || s.Now == nil {
		return zero, ErrCatalogInvalid
	}
	resolver := routinggroup.CandidateResolver{Subjects: officialCandidateSubjectBridge{}, Channels: officialPublishedChannelBridge{}}
	membership, err := resolver.ReadMembershipTx(ctx, tx, routinggroup.CandidateSubjectRef{OrgID: ref.OrgID, KeyID: ref.KeyID}, s.Now().UTC())
	if err != nil {
		return zero, catalogError(ctx, err)
	}
	observed, err := membership.Observation()
	if err != nil {
		return zero, catalogError(ctx, err)
	}
	// Grouped observations are available to internal composition, but public
	// catalog and admission remain closed until every production entry is ready.
	if observed.ModelAccessMode != "legacy" {
		return zero, ErrCatalogDenied
	}
	facts, err := membership.ContextFacts()
	if err != nil {
		return zero, catalogError(ctx, err)
	}
	return officialActionSubject{membership: membership, facts: facts}, nil
}
