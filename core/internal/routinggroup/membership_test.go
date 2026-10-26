package routinggroup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
)

func TestMembershipLegacyPreservesPredicateAndOldSentinel(t *testing.T) {
	db, r, f, ref, at := candidateFixture(t)
	f.subject.Mode, f.subject.RoutingGroupID = "legacy", nil
	f.subject.KeyScopes = "video:write"
	tx := db.Begin()
	defer tx.Rollback()
	f.pool = tx.Statement.ConnPool
	queries := 0
	const callback = "membership_legacy_no_group_read"
	if err := db.Callback().Query().Before("gorm:query").Register(callback, func(q *gorm.DB) {
		queries++
		q.AddError(errors.New("unexpected legacy group query"))
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Callback().Query().Remove(callback) })
	for _, tc := range []struct {
		limits, model string
		included      bool
	}{
		{"", "", true},
		{" \t\n", "malformed\x00name", true},
		{"", strings.Repeat("m", 129), true},
		{" Alpha , beta , 图片 ", "Alpha", true},
		{" Alpha , beta , 图片 ", "alpha", false},
		{" Alpha , beta , 图片 ", " beta", false},
		{" Alpha , beta , 图片 ", "图片", true},
		{",", "", true},
		{",", "model", false},
		{"*", "model", false},
		{"*", "*", true},
		{"bad\x00name", "bad\x00name", true},
	} {
		f.subject.ModelLimits = tc.limits
		observation, err := r.ReadMembershipTx(context.Background(), tx, ref, at)
		if err != nil {
			t.Fatal(err)
		}
		got, err := observation.ResolveModel(tc.model)
		if err != nil || got.Mode != "legacy" || got.Model != tc.model || got.ModelIncluded != tc.included || got.ChannelIDs == nil || len(got.ChannelIDs) != 0 {
			t.Fatal("legacy predicate changed", err, got.ModelIncluded)
		}
		observed, err := observation.Observation()
		if err != nil || observed.Assurance != "legacy_model_membership" || observed.GroupID != 0 || observed.GroupPolicyVersion != 0 || observed.GrantVersion != 0 || observed.OrgID != ref.OrgID || observed.KeyID != ref.KeyID || observed.KeyPolicyVersion != 3 || observed.CheckedAt != at.UTC() {
			t.Fatal("legacy observation widened", observed, err)
		}
		facts, err := observation.ContextFacts()
		if err != nil || facts.KeyScopes != "video:write" || facts.FeatureVideo || facts.FeatureImages {
			t.Fatal("membership became an action decision", err)
		}
	}
	before := f.calls
	if _, err := r.ResolveModelCandidatesTx(context.Background(), tx, ref, "beta", at); err != ErrCandidatesLegacyMode {
		t.Fatal("old resolve changed legacy contract", err)
	}
	if _, err := r.ListModelCandidatesTx(context.Background(), tx, ref, ModelCandidatePage{}, at); err != ErrCandidatesLegacyMode {
		t.Fatal("old list changed legacy contract", err)
	}
	if f.calls != before+2 || queries != 0 {
		t.Fatal("legacy performed unnecessary reads", f.calls-before, queries)
	}
}

func TestMembershipOneReadAndDefensiveOwnership(t *testing.T) {
	db, r, f, ref, at := candidateFixture(t)
	tx := db.Begin()
	defer tx.Rollback()
	f.pool = tx.Statement.ConnPool
	originalGroup := *f.subject.RoutingGroupID
	queries := 0
	const callback = "membership_single_snapshot"
	if err := db.Callback().Query().After("gorm:query").Register(callback, func(q *gorm.DB) {
		queries++
		// Mutate a bridge-owned pointer during a later SQL read. The observation
		// must already own the subject it validated, including this pointer.
		if q.Statement.Table == "routing_groups" {
			*f.subject.RoutingGroupID = 999
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Callback().Query().Remove(callback) })
	dirty := tx.Table("private_wrong").Select("credential_enc").Where("id = 999").Limit(1)
	o, err := r.ReadMembershipTx(context.Background(), dirty, ref, at)
	if err != nil || queries != 3 || f.calls != 2 {
		t.Fatal("membership did not use one shared transaction read", queries, f.calls, err)
	}
	// These mutations are not a new database observation and cannot change o.
	f.subject.Mode, f.subject.KeyScopes = "legacy", "*"
	f.channels[0].Models[0] = "replacement"
	f.channels[0].ID, f.channels[1].Enabled = 999, false
	for _, want := range []ModelMembership{
		{Mode: "inherit_group", Model: "Alpha", ModelIncluded: true, ChannelIDs: []uint64{10}},
		{Mode: "inherit_group", Model: "beta", ModelIncluded: true, ChannelIDs: []uint64{10, 11}},
		{Mode: "inherit_group", Model: "gamma", ModelIncluded: true, ChannelIDs: []uint64{11}},
		{Mode: "inherit_group", Model: "图片", ModelIncluded: true, ChannelIDs: []uint64{10}},
		{Mode: "inherit_group", Model: "foreign", ChannelIDs: []uint64{}},
		{Mode: "inherit_group", Model: "disabled", ChannelIDs: []uint64{}},
		{Mode: "inherit_group", Model: "alpha", ChannelIDs: []uint64{}},
	} {
		got, err := o.ResolveModel(want.Model)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatal("independent membership oracle", got, want, err)
		}
		if len(got.ChannelIDs) != 0 {
			got.ChannelIDs[0] = 888
			again, err := o.ResolveModel(want.Model)
			if err != nil || !reflect.DeepEqual(again, want) {
				t.Fatal("returned IDs alias the observation", err)
			}
		}
	}
	observed, err := o.Observation()
	if err != nil || observed.GroupID != originalGroup || observed.Assurance != "group_membership_only" || observed.KeyPolicyVersion != 3 || observed.GroupPolicyVersion == 0 || observed.GrantVersion != 1 {
		t.Fatal(observed, err)
	}
	observed.GroupID = 0
	if again, _ := o.Observation(); again.GroupID != originalGroup {
		t.Fatal("returned observation aliases state")
	}
	facts, err := o.ContextFacts()
	if err != nil || facts.KeyScopes != "" {
		t.Fatal("bridge mutated context facts", err)
	}
	facts.KeyScopes = "*"
	if again, _ := o.ContextFacts(); again.KeyScopes != "" {
		t.Fatal("returned context facts alias state")
	}
	if raw, err := json.Marshal(o); err != nil || string(raw) != "{}" {
		t.Fatal("opaque observation exposed internal facts")
	}
	if queries != 3 || f.calls != 2 {
		t.Fatal("pure lookups performed more reads", queries, f.calls)
	}
}

func TestMembershipSubsetAndCompleteSetBoundary(t *testing.T) {
	db, r, f, ref, at := candidateFixture(t)
	tx := db.Begin()
	defer tx.Rollback()
	f.subject.Mode, f.subject.ModelLimits = "subset", " beta, foreign, missing "
	o, err := r.ReadMembershipTx(context.Background(), tx, ref, at)
	if err != nil {
		t.Fatal(err)
	}
	for _, model := range []string{"beta", "foreign", "missing", "Alpha"} {
		got, err := o.ResolveModel(model)
		if err != nil || got.ModelIncluded != (model == "beta") || (model == "beta" && !reflect.DeepEqual(got.ChannelIDs, []uint64{10, 11})) {
			t.Fatal("subset widened", got, err)
		}
	}
	for _, model := range []string{"", " beta", "bad\x00model", strings.Repeat("m", 129)} {
		if got, err := o.ResolveModel(model); err != ErrCandidatesInvalid || !reflect.DeepEqual(got, ModelMembership{}) {
			t.Fatal("grouped accepted invalid model", err)
		}
	}
	f.subject.Mode, f.subject.ModelLimits = "inherit_group", ""
	models := make([]string, 1000)
	for i := range models {
		models[i] = fmt.Sprintf("m-%04d", i)
	}
	f.channels = []PublishedChannelFacts{{ID: 10, Enabled: true, Models: models}}
	if _, err := r.ReadMembershipTx(context.Background(), tx, ref, at); err != nil {
		t.Fatal("exactly 1000 rejected", err)
	}
	f.channels[0].Models = append(f.channels[0].Models, "m-1000")
	if got, err := r.ReadMembershipTx(context.Background(), tx, ref, at); err != ErrCandidatesLimit || !reflect.DeepEqual(got, MembershipObservation{}) {
		t.Fatal("1001 returned a partial observation", err)
	}
	if _, err := r.ListModelCandidatesTx(context.Background(), tx, ref, ModelCandidatePage{}, at); err != ErrCandidatesLimit {
		t.Fatal("old directory limit changed", err)
	}
	got, err := r.ResolveModelCandidatesTx(context.Background(), tx, ref, "m-0000", at)
	if err != nil || !reflect.DeepEqual(got.Candidate.ChannelIDs, []uint64{10}) {
		t.Fatal("old single-model API built an unnecessary directory", err)
	}
}

func TestMembershipBindingAndByteBounds(t *testing.T) {
	t.Run("bindings", func(t *testing.T) {
		db, r, _, ref, at := candidateFixture(t)
		tx := db.Begin()
		defer tx.Rollback()
		for id := 15; id <= 109; id++ {
			if err := tx.Exec("INSERT INTO channel_group_bindings(group_id,channel_id,created_at,updated_at) VALUES(1,?,?,?)", id, at, at).Error; err != nil {
				t.Fatal(err)
			}
		}
		if _, err := r.ReadMembershipTx(context.Background(), tx, ref, at); err != nil {
			t.Fatal("100 bindings rejected", err)
		}
		if err := tx.Exec("INSERT INTO channel_group_bindings(group_id,channel_id,created_at,updated_at) VALUES(1,110,?,?)", at, at).Error; err != nil {
			t.Fatal(err)
		}
		if got, err := r.ReadMembershipTx(context.Background(), tx, ref, at); err != ErrCandidatesLimit || !reflect.DeepEqual(got, MembershipObservation{}) {
			t.Fatal("101 bindings accepted", err)
		}
	})
	t.Run("channel_and_total_bytes", func(t *testing.T) {
		db, r, f, ref, at := candidateFixture(t)
		tx := db.Begin()
		defer tx.Rollback()
		models := make([]string, 512)
		for i := range models {
			models[i] = fmt.Sprintf("%04d", i) + strings.Repeat("x", 123)
		}
		models[511] += "x"
		if len(strings.Join(models, ",")) != 65536 {
			t.Fatal("invalid independent 64KiB fixture")
		}
		f.channels = []PublishedChannelFacts{{ID: 10, Enabled: true, Models: models}}
		if _, err := r.ReadMembershipTx(context.Background(), tx, ref, at); err != nil {
			t.Fatal("64KiB rejected", err)
		}
		f.channels[0].Models = append(append([]string{}, models...), "extra")
		if _, err := r.ReadMembershipTx(context.Background(), tx, ref, at); err != ErrCandidatesLimit {
			t.Fatal("channel above 64KiB accepted", err)
		}
		for id := 15; id <= 26; id++ {
			if err := tx.Exec("INSERT INTO channel_group_bindings(group_id,channel_id,created_at,updated_at) VALUES(1,?,?,?)", id, at, at).Error; err != nil {
				t.Fatal(err)
			}
		}
		f.channels = nil
		for id := uint64(10); id <= 25; id++ {
			f.channels = append(f.channels, PublishedChannelFacts{ID: id, Enabled: true, Models: models})
		}
		if _, err := r.ReadMembershipTx(context.Background(), tx, ref, at); err != nil {
			t.Fatal("exact 1MiB rejected", err)
		}
		f.channels = append(f.channels, PublishedChannelFacts{ID: 26, Enabled: true, Models: []string{"z"}})
		if got, err := r.ReadMembershipTx(context.Background(), tx, ref, at); err != ErrCandidatesLimit || !reflect.DeepEqual(got, MembershipObservation{}) {
			t.Fatal("total above 1MiB accepted", err)
		}
	})
}

type membershipHookFacts struct {
	*candidateTestFacts
	afterSubject, afterChannels func()
}

func (f *membershipHookFacts) ReadCandidateSubject(ctx context.Context, tx *gorm.DB, ref CandidateSubjectRef, at time.Time) (CandidateSubjectFacts, error) {
	out, err := f.candidateTestFacts.ReadCandidateSubject(ctx, tx, ref, at)
	if f.afterSubject != nil {
		f.afterSubject()
	}
	return out, err
}
func (f *membershipHookFacts) ReadPublishedChannels(ctx context.Context, tx *gorm.DB, ids []uint64) ([]PublishedChannelFacts, error) {
	out, err := f.candidateTestFacts.ReadPublishedChannels(ctx, tx, ids)
	if f.afterChannels != nil {
		f.afterChannels()
	}
	return out, err
}

func TestMembershipRejectsInvalidOrIncompleteObservation(t *testing.T) {
	var zero MembershipObservation
	if got, err := zero.ResolveModel("model"); err != ErrCandidatesInvalid || !reflect.DeepEqual(got, ModelMembership{}) {
		t.Fatal("zero membership allowed", err)
	}
	if got, err := zero.ContextFacts(); err != ErrCandidatesInvalid || got != (CandidateContextFacts{}) {
		t.Fatal("zero facts accepted", err)
	}
	if got, err := zero.Observation(); err != ErrCandidatesInvalid || got != (CandidateObservation{}) {
		t.Fatal("zero observation accepted", err)
	}
	for _, fault := range []string{"invalid_tx", "unknown_mode", "legacy_group", "revoked", "foreign_subject", "invalid_utf8", "subject_limit", "reader_legacy_error", "sql_error", "canceled_before", "canceled_subject", "canceled_channels", "sql_deadline"} {
		t.Run(fault, func(t *testing.T) {
			db, r, f, ref, at := candidateFixture(t)
			tx := db.Begin()
			defer tx.Rollback()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			hooks := &membershipHookFacts{candidateTestFacts: f}
			r.Subjects, r.Channels = hooks, hooks
			want := ErrCandidatesInvalidFacts
			switch fault {
			case "invalid_tx":
				tx = db
				want = ErrCandidatesInvalid
			case "unknown_mode":
				f.subject.Mode = "legacy "
			case "legacy_group":
				f.subject.Mode = "legacy"
			case "revoked":
				f.subject.KeyActive = false
				want = ErrCandidatesDenied
			case "foreign_subject":
				f.subject.OrgID = 2
				want = ErrCandidatesDenied
			case "invalid_utf8":
				f.subject.KeyScopes = "\xff"
			case "subject_limit":
				f.subject.ModelLimits = strings.Repeat("a", 32769)
				want = ErrCandidatesLimit
			case "reader_legacy_error":
				f.err = ErrCandidatesLegacyMode
				want = ErrCandidatesLegacyMode
			case "sql_error":
				if err := tx.Exec("DROP TABLE channel_group_bindings").Error; err != nil {
					t.Fatal(err)
				}
				want = ErrCandidatesUnavailable
			case "canceled_before":
				cancel()
				want = context.Canceled
			case "canceled_subject":
				hooks.afterSubject = cancel
				want = context.Canceled
			case "canceled_channels":
				hooks.afterChannels = cancel
				want = context.Canceled
			case "sql_deadline":
				const callback = "membership_sql_deadline"
				if err := db.Callback().Query().Before("gorm:query").Register(callback, func(q *gorm.DB) { <-q.Statement.Context.Done() }); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { db.Callback().Query().Remove(callback) })
				want = context.DeadlineExceeded
			}
			got, err := r.ReadMembershipTx(ctx, tx, ref, at)
			if !errors.Is(err, want) || !reflect.DeepEqual(got, MembershipObservation{}) {
				t.Fatal("failed read returned an observation", fault, err)
			}
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				if !errors.Is(err, ErrCandidatesUnavailable) {
					t.Fatal("context error lost safe classification", err)
				}
			}
			if strings.Contains(err.Error(), "channel_group_bindings") {
				t.Fatal("database error leaked SQL identity")
			}
		})
	}
}
