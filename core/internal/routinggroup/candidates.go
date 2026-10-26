package routinggroup

// This read-only candidate layer is not admission or a sending permit. No
// production route consumes it, and grouped API keys remain disabled.
import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var (
	ErrCandidatesInvalid      = errors.New("routinggroup: invalid candidate query")
	ErrCandidatesDenied       = errors.New("routinggroup: candidate subject denied")
	ErrCandidatesLegacyMode   = errors.New("routinggroup: legacy candidates not handled")
	ErrCandidatesInvalidFacts = errors.New("routinggroup: invalid candidate facts")
	ErrCandidatesLimit        = errors.New("routinggroup: candidate limit exceeded")
	ErrCandidatesUnavailable  = errors.New("routinggroup: candidates unavailable")
)

const candidateAssurance = "group_membership_only"

type CandidateSubjectRef struct{ OrgID, KeyID uint64 }
type CandidateResolver struct {
	Subjects CandidateSubjectReader
	Channels PublishedChannelReader
}
type CandidateSubjectReader interface {
	ReadCandidateSubject(context.Context, *gorm.DB, CandidateSubjectRef, time.Time) (CandidateSubjectFacts, error)
}
type PublishedChannelReader interface {
	ReadPublishedChannels(context.Context, *gorm.DB, []uint64) ([]PublishedChannelFacts, error)
}
type CandidateSubjectFacts struct {
	Found                                  bool
	OrgID, KeyID                           uint64
	KeyActive, KeyExpired, PrincipalActive bool
	Mode                                   string
	RoutingGroupID                         *uint64
	KeyPolicyVersion                       uint64
	ModelLimits, KeyScopes                 string
	FeatureImages, FeatureVideo            bool
}
type PublishedChannelFacts struct {
	ID, OwnerOrgID uint64
	Enabled        bool
	Models         []string
}
type CandidateObservation struct {
	OrgID, KeyID, GroupID                              uint64
	KeyPolicyVersion, GroupPolicyVersion, GrantVersion uint64
	ModelAccessMode                                    string
	CheckedAt                                          time.Time
	Assurance                                          string
}
type CandidateContextFacts struct {
	KeyScopes                   string
	FeatureImages, FeatureVideo bool
}
type ModelCandidate struct {
	Model      string
	ChannelIDs []uint64
}
type ModelCandidates struct {
	Observation  CandidateObservation
	ContextFacts CandidateContextFacts
	Candidate    ModelCandidate
}
type ModelCandidatePage struct {
	AfterModel string
	Limit      int
}
type ModelCandidatePageResult struct {
	Observation    CandidateObservation
	ContextFacts   CandidateContextFacts
	Items          []ModelCandidate
	NextAfterModel string
	HasMore        bool
}

type candidateContextError struct{ cause error }

func (e *candidateContextError) Error() string        { return ErrCandidatesUnavailable.Error() }
func (e *candidateContextError) Unwrap() error        { return e.cause }
func (e *candidateContextError) Is(target error) bool { return target == ErrCandidatesUnavailable }

func candidatesFailure(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return &candidateContextError{context.Canceled}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &candidateContextError{context.DeadlineExceeded}
	}
	for _, known := range []error{ErrCandidatesInvalid, ErrCandidatesDenied, ErrCandidatesLegacyMode, ErrCandidatesInvalidFacts, ErrCandidatesLimit, ErrCandidatesUnavailable} {
		if errors.Is(err, known) {
			return known
		}
	}
	return ErrCandidatesUnavailable
}

func candidateTxValid(ctx context.Context, tx *gorm.DB) error {
	if ctx == nil || tx == nil || tx.Config == nil || tx.Statement == nil || tx.DryRun || tx.Dialector == nil {
		return ErrCandidatesInvalid
	}
	if _, ok := tx.Statement.ConnPool.(gorm.TxCommitter); !ok {
		return ErrCandidatesInvalid
	}
	if name := tx.Dialector.Name(); name != "mysql" && name != "sqlite" {
		return ErrCandidatesInvalid
	}
	if err := ctx.Err(); err != nil {
		return candidatesFailure(err)
	}
	if tx.Error != nil {
		return candidatesFailure(tx.Error)
	}
	return nil
}

func candidateSelect(ctx context.Context, tx *gorm.DB, columns string) (*gorm.DB, context.CancelFunc, error) {
	child, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	if err := child.Err(); err != nil {
		cancel()
		return nil, func() {}, err
	}
	if tx.Dialector.Name() == "mysql" {
		deadline, _ := child.Deadline()
		ms := time.Until(deadline).Milliseconds()
		if ms < 1 {
			cancel()
			return nil, func() {}, context.DeadlineExceeded
		}
		columns = fmt.Sprintf("/*+ MAX_EXECUTION_TIME(%d) */ %s", min(ms, int64(200)), columns)
	}
	return tx.Session(&gorm.Session{NewDB: true, Context: child, Logger: logger.Discard}).Select(columns), cancel, nil
}

type candidateSnapshot struct {
	subject  CandidateSubjectFacts
	group    Group
	grant    Grant
	channels []PublishedChannelFacts
}

func (r *CandidateResolver) readCandidateSnapshot(ctx context.Context, tx *gorm.DB, ref CandidateSubjectRef, at time.Time) (candidateSnapshot, error) {
	var out candidateSnapshot
	subject, err := r.Subjects.ReadCandidateSubject(ctx, tx, ref, at)
	if err != nil {
		return out, candidatesFailure(err)
	}
	if err = validateCandidateSubject(ref, subject); err != nil {
		return out, err
	}
	out.subject = subject
	q, cancel, err := candidateSelect(ctx, tx, "id, owner_org_id, status, policy_version")
	if err != nil {
		return candidateSnapshot{}, candidatesFailure(err)
	}
	err = q.Where("id = ? AND (owner_org_id = 0 OR owner_org_id = ?)", *subject.RoutingGroupID, ref.OrgID).Take(&out.group).Error
	if contextErr := q.Statement.Context.Err(); contextErr != nil {
		err = contextErr
	}
	cancel()
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return candidateSnapshot{}, ErrCandidatesDenied
	}
	if err != nil {
		return candidateSnapshot{}, candidatesFailure(err)
	}
	if out.group.ID != *subject.RoutingGroupID || (out.group.OwnerOrgID != 0 && out.group.OwnerOrgID != ref.OrgID) {
		return candidateSnapshot{}, ErrCandidatesInvalidFacts
	}
	if out.group.PolicyVersion == 0 || !validStatus(out.group.Status) {
		return candidateSnapshot{}, ErrCandidatesInvalidFacts
	}
	if out.group.Status != Enabled {
		return candidateSnapshot{}, ErrCandidatesDenied
	}
	q, cancel, err = candidateSelect(ctx, tx, "id, org_id, group_id, status, grant_version")
	if err != nil {
		return candidateSnapshot{}, candidatesFailure(err)
	}
	err = q.Where("org_id = ? AND group_id = ?", ref.OrgID, out.group.ID).Take(&out.grant).Error
	if contextErr := q.Statement.Context.Err(); contextErr != nil {
		err = contextErr
	}
	cancel()
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return candidateSnapshot{}, ErrCandidatesDenied
	}
	if err != nil {
		return candidateSnapshot{}, candidatesFailure(err)
	}
	if out.grant.OrgID != ref.OrgID || out.grant.GroupID != out.group.ID || out.grant.GrantVersion == 0 || (out.grant.Status != Enabled && out.grant.Status != Revoked) {
		return candidateSnapshot{}, ErrCandidatesInvalidFacts
	}
	if out.grant.Status != Enabled {
		return candidateSnapshot{}, ErrCandidatesDenied
	}
	q, cancel, err = candidateSelect(ctx, tx, "group_id, channel_id")
	if err != nil {
		return candidateSnapshot{}, candidatesFailure(err)
	}
	var bindings []Binding
	err = q.Where("group_id = ?", out.group.ID).Order("channel_id ASC").Limit(101).Find(&bindings).Error
	if contextErr := q.Statement.Context.Err(); contextErr != nil {
		err = contextErr
	}
	cancel()
	if err != nil {
		return candidateSnapshot{}, candidatesFailure(err)
	}
	if len(bindings) > 100 {
		return candidateSnapshot{}, ErrCandidatesLimit
	}
	ids := make([]uint64, 0, len(bindings))
	for _, b := range bindings {
		if b.GroupID != out.group.ID || b.ChannelID == 0 || len(ids) > 0 && b.ChannelID <= ids[len(ids)-1] {
			return candidateSnapshot{}, ErrCandidatesInvalidFacts
		}
		ids = append(ids, b.ChannelID)
	}
	out.channels, err = r.Channels.ReadPublishedChannels(ctx, tx, ids)
	if err != nil {
		return candidateSnapshot{}, candidatesFailure(err)
	}
	if err = validatePublishedCandidateFacts(ids, out.channels); err != nil {
		return candidateSnapshot{}, err
	}
	if err = ctx.Err(); err != nil {
		return candidateSnapshot{}, candidatesFailure(err)
	}
	return out, nil
}

func (r *CandidateResolver) candidateStart(ctx context.Context, tx *gorm.DB, ref CandidateSubjectRef, at time.Time) (context.Context, context.CancelFunc, *gorm.DB, error) {
	if r == nil || r.Subjects == nil || r.Channels == nil || ref.OrgID == 0 || ref.KeyID == 0 || at.IsZero() {
		return nil, func() {}, nil, ErrCandidatesInvalid
	}
	if err := candidateTxValid(ctx, tx); err != nil {
		return nil, func() {}, nil, err
	}
	child, cancel := context.WithTimeout(ctx, time.Second)
	return child, cancel, tx.Session(&gorm.Session{NewDB: true, Context: child, Logger: logger.Discard}), nil
}

func (s candidateSnapshot) observation(at time.Time) CandidateObservation {
	return CandidateObservation{OrgID: s.subject.OrgID, KeyID: s.subject.KeyID, GroupID: s.group.ID, KeyPolicyVersion: s.subject.KeyPolicyVersion, GroupPolicyVersion: s.group.PolicyVersion, GrantVersion: s.grant.GrantVersion, ModelAccessMode: s.subject.Mode, CheckedAt: at.UTC(), Assurance: candidateAssurance}
}
func (s candidateSnapshot) contextFacts() CandidateContextFacts {
	return CandidateContextFacts{KeyScopes: s.subject.KeyScopes, FeatureImages: s.subject.FeatureImages, FeatureVideo: s.subject.FeatureVideo}
}

func (r *CandidateResolver) ResolveModelCandidatesTx(ctx context.Context, tx *gorm.DB, ref CandidateSubjectRef, model string, at time.Time) (ModelCandidates, error) {
	if !validCandidateModel(model) {
		return ModelCandidates{}, ErrCandidatesInvalid
	}
	ctx, cancel, tx, err := r.candidateStart(ctx, tx, ref, at)
	if err != nil {
		return ModelCandidates{}, err
	}
	defer cancel()
	snapshot, err := r.readCandidateSnapshot(ctx, tx, ref, at)
	if err != nil {
		return ModelCandidates{}, err
	}
	candidate := resolveCandidateModel(snapshot, model)
	if err := ctx.Err(); err != nil {
		return ModelCandidates{}, candidatesFailure(err)
	}
	return ModelCandidates{Observation: snapshot.observation(at), ContextFacts: snapshot.contextFacts(), Candidate: candidate}, nil
}

func (r *CandidateResolver) ListModelCandidatesTx(ctx context.Context, tx *gorm.DB, ref CandidateSubjectRef, page ModelCandidatePage, at time.Time) (ModelCandidatePageResult, error) {
	if page.Limit == 0 {
		page.Limit = 20
	}
	if page.Limit < 1 || page.Limit > 100 || page.AfterModel != "" && !validCandidateModel(page.AfterModel) {
		return ModelCandidatePageResult{}, ErrCandidatesInvalid
	}
	ctx, cancel, tx, err := r.candidateStart(ctx, tx, ref, at)
	if err != nil {
		return ModelCandidatePageResult{}, err
	}
	defer cancel()
	snapshot, err := r.readCandidateSnapshot(ctx, tx, ref, at)
	if err != nil {
		return ModelCandidatePageResult{}, err
	}
	items, err := listCandidateModels(snapshot)
	if err != nil {
		return ModelCandidatePageResult{}, err
	}
	out := ModelCandidatePageResult{Observation: snapshot.observation(at), ContextFacts: snapshot.contextFacts(), Items: make([]ModelCandidate, 0, page.Limit)}
	for _, item := range items {
		if item.Model <= page.AfterModel {
			continue
		}
		if len(out.Items) == page.Limit {
			out.HasMore = true
			out.NextAfterModel = out.Items[len(out.Items)-1].Model
			break
		}
		out.Items = append(out.Items, item)
	}
	if err := ctx.Err(); err != nil {
		return ModelCandidatePageResult{}, candidatesFailure(err)
	}
	return out, nil
}
