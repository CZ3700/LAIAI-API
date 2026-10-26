package routinggroup

import (
	"context"
	"time"

	"gorm.io/gorm"
)

// MembershipObservation describes one bounded observation in the caller's
// transaction. It is not reusable authorization and must not be cached or used
// across transactions. Its zero value rejects every accessor.
type MembershipObservation struct {
	valid       bool
	observation CandidateObservation
	facts       CandidateContextFacts
	modelLimits string
	models      map[string][]uint64
}

type ModelMembership struct {
	Mode          string
	Model         string
	ModelIncluded bool
	// Legacy always returns a non-nil empty slice; this never means all channels.
	ChannelIDs []uint64
}

// ReadMembershipTx uses the existing subject and grouped readers once, without
// starting or finishing a transaction. A legacy observation does not read any
// group or channel. A grouped observation bounds its complete model set, while
// the existing single-model candidate API keeps its original narrower work.
func (r *CandidateResolver) ReadMembershipTx(ctx context.Context, tx *gorm.DB, ref CandidateSubjectRef, at time.Time) (MembershipObservation, error) {
	ctx, cancel, tx, err := r.candidateStart(ctx, tx, ref, at)
	if err != nil {
		return MembershipObservation{}, err
	}
	defer cancel()
	snapshot, err := r.readMembershipSnapshot(ctx, tx, ref, at)
	if err != nil {
		return MembershipObservation{}, err
	}
	out := MembershipObservation{observation: snapshot.observation(at), facts: snapshot.contextFacts()}
	if snapshot.subject.Mode == "legacy" {
		out.observation.Assurance = "legacy_model_membership"
		out.modelLimits = snapshot.subject.ModelLimits
	} else {
		items, err := listCandidateModels(snapshot)
		if contextErr := ctx.Err(); contextErr != nil {
			return MembershipObservation{}, candidatesFailure(contextErr)
		}
		if err != nil {
			return MembershipObservation{}, err
		}
		out.models = make(map[string][]uint64, len(items))
		for _, item := range items {
			out.models[item.Model] = item.ChannelIDs
		}
	}
	if err := ctx.Err(); err != nil {
		return MembershipObservation{}, candidatesFailure(err)
	}
	out.valid = true
	return out, nil
}

// ResolveModel is a pure lookup. Legacy keeps its exact historical CSV
// semantics, including names outside the grouped candidate grammar.
func (o MembershipObservation) ResolveModel(model string) (ModelMembership, error) {
	if !o.valid {
		return ModelMembership{}, ErrCandidatesInvalid
	}
	out := ModelMembership{Mode: o.observation.ModelAccessMode, Model: model, ChannelIDs: []uint64{}}
	if out.Mode == "legacy" {
		out.ModelIncluded = LegacyModelAllowed(o.modelLimits, model)
		return out, nil
	}
	if !validCandidateModel(model) {
		return ModelMembership{}, ErrCandidatesInvalid
	}
	out.ChannelIDs = append(out.ChannelIDs, o.models[model]...)
	out.ModelIncluded = len(out.ChannelIDs) != 0
	return out, nil
}

func (o MembershipObservation) ContextFacts() (CandidateContextFacts, error) {
	if !o.valid {
		return CandidateContextFacts{}, ErrCandidatesInvalid
	}
	return o.facts, nil
}

func (o MembershipObservation) Observation() (CandidateObservation, error) {
	if !o.valid {
		return CandidateObservation{}, ErrCandidatesInvalid
	}
	return o.observation, nil
}
