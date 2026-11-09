package relay

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/routinggroup"
	"gorm.io/gorm"
)

type relayCandidateSubjectBridge struct{}
type relayPublishedChannelBridge struct{}

func relayCandidateBridgeError(err error) error {
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
	case errors.Is(err, identity.ErrRoutingCandidateInvalid), errors.Is(err, ErrRoutingPublishedInvalid):
		return routinggroup.ErrCandidatesInvalid
	case errors.Is(err, identity.ErrRoutingCandidateFacts), errors.Is(err, ErrRoutingPublishedFacts):
		return routinggroup.ErrCandidatesInvalidFacts
	case errors.Is(err, identity.ErrRoutingCandidateLimit), errors.Is(err, ErrRoutingPublishedLimit):
		return routinggroup.ErrCandidatesLimit
	default:
		return routinggroup.ErrCandidatesUnavailable
	}
}

func (relayCandidateSubjectBridge) ReadCandidateSubject(ctx context.Context, tx *gorm.DB, ref routinggroup.CandidateSubjectRef, at time.Time) (routinggroup.CandidateSubjectFacts, error) {
	f, err := identity.ReadRoutingCandidateSubjectTx(ctx, tx, ref.OrgID, ref.KeyID, at)
	if err != nil {
		return routinggroup.CandidateSubjectFacts{}, relayCandidateBridgeError(err)
	}
	return routinggroup.CandidateSubjectFacts{
		Found: f.Found, OrgID: f.OrgID, KeyID: f.KeyID,
		KeyActive: f.KeyActive, KeyExpired: f.KeyExpired, PrincipalActive: f.PrincipalActive,
		Mode: f.Mode, RoutingGroupID: f.RoutingGroupID, KeyPolicyVersion: f.KeyPolicyVersion,
		ModelLimits: f.ModelLimits, KeyScopes: f.KeyScopes,
		FeatureImages: f.FeatureImages, FeatureVideo: f.FeatureVideo,
	}, nil
}

func (relayPublishedChannelBridge) ReadPublishedChannels(ctx context.Context, tx *gorm.DB, ids []uint64) ([]routinggroup.PublishedChannelFacts, error) {
	rows, err := ReadRoutingPublishedChannelsTx(ctx, tx, ids)
	if err != nil {
		return nil, relayCandidateBridgeError(err)
	}
	out := make([]routinggroup.PublishedChannelFacts, 0, len(rows))
	for _, row := range rows {
		out = append(out, routinggroup.PublishedChannelFacts{
			ID: row.ID, OwnerOrgID: row.OwnerOrgID, Enabled: row.Enabled,
			Models: append([]string(nil), row.Models...),
		})
	}
	return out, nil
}

func relayCandidateResolver() routinggroup.CandidateResolver {
	return routinggroup.CandidateResolver{Subjects: relayCandidateSubjectBridge{}, Channels: relayPublishedChannelBridge{}}
}

// groupedModelChannels makes a fresh, authoritative observation for one model.
// Call before every channel pick. The snapshot channel cache is only a source
// of routing metadata; it never supplies grouped membership or publication.
func (r *Relay) groupedModelChannels(ctx context.Context, key *identity.APIKey, model string) ([]uint64, error) {
	if ctx == nil || r == nil || r.DB == nil || r.Now == nil || key == nil || key.ModelAccessMode != identity.ModelAccessInheritGroup || key.RoutingGroupID == nil || *key.RoutingGroupID == 0 || key.PolicyVersion == 0 || key.ID == 0 || key.OrgID == 0 {
		return nil, routinggroup.ErrCandidatesDenied
	}
	var ids []uint64
	resolver := relayCandidateResolver()
	err := r.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		out, err := resolver.ResolveModelCandidatesTx(ctx, tx, routinggroup.CandidateSubjectRef{OrgID: key.OrgID, KeyID: key.ID}, model, r.Now().UTC())
		if err != nil {
			return err
		}
		if out.Observation.OrgID != key.OrgID || out.Observation.KeyID != key.ID || out.Observation.GroupID != *key.RoutingGroupID || out.Observation.KeyPolicyVersion != key.PolicyVersion || out.Observation.ModelAccessMode != identity.ModelAccessInheritGroup || out.Candidate.Model != model || len(out.Candidate.ChannelIDs) == 0 {
			return routinggroup.ErrCandidatesDenied
		}
		ids = append([]uint64(nil), out.Candidate.ChannelIDs...)
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, err
	}
	return ids, nil
}

func groupedChannelFailure(err error) (int, string) {
	if errors.Is(err, routinggroup.ErrCandidatesDenied) || errors.Is(err, routinggroup.ErrCandidatesInvalid) || errors.Is(err, routinggroup.ErrCandidatesInvalidFacts) || errors.Is(err, routinggroup.ErrCandidatesLimit) {
		return http.StatusForbidden, "this api key is not allowed to use this model"
	}
	return http.StatusServiceUnavailable, "routing group is unavailable"
}

// gateRelayKey is used only by the relay HTTP handlers. Task's exported
// GateKey remains legacy-only until task routing can carry a channel permit.
func (r *Relay) gateRelayKey(ctx context.Context, key *identity.APIKey, clientIP, model string) (int, string, bool) {
	if key == nil {
		return http.StatusUnauthorized, "invalid api key", false
	}
	if key.ModelAccessMode != identity.ModelAccessInheritGroup {
		return r.gateKey(key, clientIP, model)
	}
	if _, err := r.groupedModelChannels(ctx, key, model); err != nil {
		code, message := groupedChannelFailure(err)
		return code, message, false
	}
	return r.GateOfficialVideoKey(key, clientIP, model)
}

func (r *Relay) relayModelAllowed(ctx context.Context, key *identity.APIKey, model string) bool {
	if key == nil {
		return false
	}
	if key.ModelAccessMode != identity.ModelAccessInheritGroup {
		return key.AllowsModel(model)
	}
	_, err := r.groupedModelChannels(ctx, key, model)
	return err == nil
}

func channelsByID(channels []Channel, ids []uint64) []Channel {
	if len(ids) == 0 {
		return nil
	}
	allowed := make(map[uint64]struct{}, len(ids))
	for _, id := range ids {
		allowed[id] = struct{}{}
	}
	out := make([]Channel, 0, len(ids))
	for _, ch := range channels {
		if _, ok := allowed[ch.ID]; ok {
			out = append(out, ch)
		}
	}
	return out
}

// pickRelayChannel rechecks grouped membership immediately before every
// attempt, including breaker retries and fallback models. Legacy stays on its
// original picker path.
func (r *Relay) pickRelayChannel(ctx context.Context, key *identity.APIKey, channels []Channel, model string, attempt int, exclude map[uint64]bool, policy string, pref *providerPref) (*Channel, error) {
	if key == nil {
		return nil, ErrNoChannel
	}
	if key.ModelAccessMode == identity.ModelAccessInheritGroup {
		ids, err := r.groupedModelChannels(ctx, key, model)
		if err != nil {
			return nil, err
		}
		channels = channelsByID(channels, ids)
	}
	return r.pickSnapshotPolicy(channels, model, attempt, exclude, policy, key.OrgID, pref)
}
