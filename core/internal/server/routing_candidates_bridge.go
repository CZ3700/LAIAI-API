package server

// These bridges are deliberately not wired to production routes or admission.
// Candidate facts alone do not establish pricing, capability or send permission.
import (
	"context"
	"errors"
	"time"

	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/relay"
	"github.com/laiai/laiai-next/core/internal/routinggroup"
	"gorm.io/gorm"
)

type routingCandidateSubjectBridge struct{}
type routingPublishedChannelBridge struct{}

func routingCandidateBridgeError(err error) error {
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
func (routingCandidateSubjectBridge) ReadCandidateSubject(ctx context.Context, tx *gorm.DB, ref routinggroup.CandidateSubjectRef, at time.Time) (routinggroup.CandidateSubjectFacts, error) {
	f, err := identity.ReadRoutingCandidateSubjectTx(ctx, tx, ref.OrgID, ref.KeyID, at)
	if err != nil {
		return routinggroup.CandidateSubjectFacts{}, routingCandidateBridgeError(err)
	}
	var groupID *uint64
	if f.RoutingGroupID != nil {
		value := *f.RoutingGroupID
		groupID = &value
	}
	return routinggroup.CandidateSubjectFacts{Found: f.Found, OrgID: f.OrgID, KeyID: f.KeyID, KeyActive: f.KeyActive, KeyExpired: f.KeyExpired, PrincipalActive: f.PrincipalActive, Mode: f.Mode, RoutingGroupID: groupID, KeyPolicyVersion: f.KeyPolicyVersion, ModelLimits: f.ModelLimits, KeyScopes: f.KeyScopes, FeatureImages: f.FeatureImages, FeatureVideo: f.FeatureVideo}, nil
}
func (routingPublishedChannelBridge) ReadPublishedChannels(ctx context.Context, tx *gorm.DB, ids []uint64) ([]routinggroup.PublishedChannelFacts, error) {
	rows, err := relay.ReadRoutingPublishedChannelsTx(ctx, tx, ids)
	if err != nil {
		return nil, routingCandidateBridgeError(err)
	}
	out := make([]routinggroup.PublishedChannelFacts, 0, len(rows))
	for _, f := range rows {
		out = append(out, routinggroup.PublishedChannelFacts{ID: f.ID, OwnerOrgID: f.OwnerOrgID, Enabled: f.Enabled, Models: append([]string{}, f.Models...)})
	}
	return out, nil
}
