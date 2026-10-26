package routinggroup

import (
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

func validCandidateModel(value string) bool {
	if value == "" || len(value) > 512 || !utf8.ValidString(value) || utf8.RuneCountInString(value) > 128 || strings.TrimSpace(value) != value {
		return false
	}
	for _, c := range value {
		if unicode.IsControl(c) {
			return false
		}
	}
	return true
}

func candidateSubset(csv string) (map[string]bool, error) {
	if len(csv) > 32<<10 {
		return nil, ErrCandidatesLimit
	}
	out := make(map[string]bool)
	for _, entry := range strings.Split(csv, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if !validCandidateModel(entry) {
			return nil, ErrCandidatesInvalidFacts
		}
		out[entry] = true
	}
	return out, nil
}

func validateCandidateSubject(ref CandidateSubjectRef, s CandidateSubjectFacts) error {
	if !s.Found || s.OrgID != ref.OrgID || s.KeyID != ref.KeyID || !s.KeyActive || s.KeyExpired || !s.PrincipalActive {
		return ErrCandidatesDenied
	}
	if s.KeyPolicyVersion == 0 {
		return ErrCandidatesInvalidFacts
	}
	if len(s.ModelLimits) > 32<<10 || len(s.KeyScopes) > 32<<10 {
		return ErrCandidatesLimit
	}
	if !utf8.ValidString(s.ModelLimits) || !utf8.ValidString(s.KeyScopes) {
		return ErrCandidatesInvalidFacts
	}
	switch s.Mode {
	case "legacy":
		if s.RoutingGroupID != nil {
			return ErrCandidatesInvalidFacts
		}
		return ErrCandidatesLegacyMode
	case "inherit_group":
		if s.RoutingGroupID == nil || *s.RoutingGroupID == 0 || strings.TrimSpace(s.ModelLimits) != "" {
			return ErrCandidatesInvalidFacts
		}
	case "subset":
		if s.RoutingGroupID == nil || *s.RoutingGroupID == 0 {
			return ErrCandidatesInvalidFacts
		}
		subset, err := candidateSubset(s.ModelLimits)
		if err != nil {
			return err
		}
		if len(subset) == 0 {
			return ErrCandidatesInvalidFacts
		}
	default:
		return ErrCandidatesInvalidFacts
	}
	return nil
}

func validatePublishedCandidateFacts(ids []uint64, facts []PublishedChannelFacts) error {
	if len(facts) > len(ids) {
		return ErrCandidatesInvalidFacts
	}
	allowed := make(map[uint64]bool, len(ids))
	seen := make(map[uint64]bool, len(facts))
	for _, id := range ids {
		allowed[id] = true
	}
	modelBytes := 0
	for _, f := range facts {
		if !allowed[f.ID] || seen[f.ID] {
			return ErrCandidatesInvalidFacts
		}
		seen[f.ID] = true
		// Reader output is sorted/deduplicated. Validate it again at the trust
		// boundary so an accidental host bridge cannot widen a candidate set.
		channelBytes := 0
		for i, model := range f.Models {
			if !validCandidateModel(model) || i > 0 && model <= f.Models[i-1] {
				return ErrCandidatesInvalidFacts
			}
			channelBytes += len(model)
			if i > 0 {
				channelBytes++
			}
		}
		if channelBytes > 64<<10 {
			return ErrCandidatesLimit
		}
		modelBytes += channelBytes
	}
	if modelBytes > 1<<20 {
		return ErrCandidatesLimit
	}
	return nil
}

func (s candidateSnapshot) usable(f PublishedChannelFacts) bool {
	return f.Enabled && f.OwnerOrgID == s.group.OwnerOrgID
}
func (s candidateSnapshot) subset() map[string]bool {
	if s.subject.Mode != "subset" {
		return nil
	}
	values, _ := candidateSubset(s.subject.ModelLimits) // already validated
	return values
}
func resolveCandidateModel(s candidateSnapshot, model string) ModelCandidate {
	out := ModelCandidate{Model: model, ChannelIDs: []uint64{}}
	if subset := s.subset(); subset != nil && !subset[model] {
		return out
	}
	for _, f := range s.channels {
		if !s.usable(f) {
			continue
		}
		index := sort.SearchStrings(f.Models, model)
		if index < len(f.Models) && f.Models[index] == model {
			out.ChannelIDs = append(out.ChannelIDs, f.ID)
		}
	}
	sort.Slice(out.ChannelIDs, func(i, j int) bool { return out.ChannelIDs[i] < out.ChannelIDs[j] })
	return out
}
func listCandidateModels(s candidateSnapshot) ([]ModelCandidate, error) {
	byModel := make(map[string][]uint64)
	subset := s.subset()
	for _, f := range s.channels {
		if !s.usable(f) {
			continue
		}
		for _, model := range f.Models {
			if subset != nil && !subset[model] {
				continue
			}
			byModel[model] = append(byModel[model], f.ID)
			if len(byModel) > 1000 {
				return nil, ErrCandidatesLimit
			}
		}
	}
	items := make([]ModelCandidate, 0, len(byModel))
	for model, ids := range byModel {
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		items = append(items, ModelCandidate{Model: model, ChannelIDs: ids})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Model < items[j].Model })
	return items, nil
}
