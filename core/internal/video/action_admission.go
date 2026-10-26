package video

import (
	"errors"

	"github.com/laiai/laiai-next/core/internal/identity"
)

type officialAction string

const (
	officialModelsRead officialAction = "models_read"
	officialEstimate   officialAction = "estimate"
	officialCreate     officialAction = "create"
)

// These are facts produced by catalog publication or the actual plan/credential
// path. Zero facts never imply publication or usable execution credentials.
type officialActionFacts struct {
	published, credential, policyExists bool
	policy                              Policy
}

func officialActionDecision(subject officialActionSubject, model string, channelID uint64, action officialAction, facts officialActionFacts) CatalogAction {
	member, err := subject.membership.ResolveModel(model)
	bound := err == nil && member.ModelIncluded && channelID != 0
	if bound && member.Mode != "legacy" {
		bound = false
		for _, id := range member.ChannelIDs {
			if id == channelID {
				bound = true
				break
			}
		}
	}
	actions := catalogActions(identity.RoutingCandidateSubject{KeyScopes: subject.facts.KeyScopes, FeatureVideo: subject.facts.FeatureVideo}, facts.policy, facts.policyExists, facts.published && bound, facts.credential)
	switch action {
	case officialModelsRead:
		return actions.ModelsRead
	case officialEstimate:
		return actions.Estimate
	case officialCreate:
		return actions.Create
	default:
		return CatalogAction{ReasonCodes: []string{"publication_unavailable"}}
	}
}

// Admission checks the observed subject before publication/credential work.
// It does not replace the later fixed-channel publication and credential checks.
func requireOfficialActionSubject(subject officialActionSubject, model string, action officialAction) error {
	if !subject.allowsModel(model) {
		return ErrForbidden
	}
	scope := "video:write"
	switch action {
	case officialModelsRead:
		scope = "video:read"
	case officialEstimate:
	case officialCreate:
		if !subject.facts.FeatureVideo {
			return ErrForbidden
		}
	default:
		return ErrForbidden
	}
	if !hasScope(&identity.APIKey{Scopes: subject.facts.KeyScopes}, scope) {
		return ErrForbidden
	}
	return nil
}

func officialAdmissionError(err error) error {
	if errors.Is(err, ErrCatalogDenied) {
		return ErrForbidden
	}
	return err
}
