package routinggroup

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
)

type candidateTestFacts struct {
	t        *testing.T
	subject  CandidateSubjectFacts
	channels []PublishedChannelFacts
	err      error
	pool     gorm.ConnPool
	calls    int
}

func (f *candidateTestFacts) check(ctx context.Context, tx *gorm.DB) {
	f.t.Helper()
	f.calls++
	if f.pool != nil && tx.Statement.ConnPool != f.pool {
		f.t.Fatal("facts escaped transaction")
	}
	if ctx != tx.Statement.Context {
		f.t.Fatal("facts lost common context")
	}
	if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > time.Second {
		f.t.Fatal("missing common deadline")
	}
}
func (f *candidateTestFacts) ReadCandidateSubject(ctx context.Context, tx *gorm.DB, _ CandidateSubjectRef, _ time.Time) (CandidateSubjectFacts, error) {
	f.check(ctx, tx)
	return f.subject, f.err
}
func (f *candidateTestFacts) ReadPublishedChannels(ctx context.Context, tx *gorm.DB, _ []uint64) ([]PublishedChannelFacts, error) {
	f.check(ctx, tx)
	return f.channels, f.err
}
func candidateFixture(t *testing.T) (*gorm.DB, *CandidateResolver, *candidateTestFacts, CandidateSubjectRef, time.Time) {
	t.Helper()
	s, _ := serviceFixture(t)
	g := enabledGroup(t, s, 0, "candidates")
	if _, _, err := s.SetGrant(context.Background(), GrantInput{OrgID: 1, GroupID: g.ID, Status: Enabled, Audit: audit}); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	for _, id := range []int{10, 11, 12, 13, 14} {
		if err := s.DB.Exec("INSERT INTO channel_group_bindings(group_id,channel_id,created_at,updated_at) VALUES(?,?,?,?)", g.ID, id, at, at).Error; err != nil {
			t.Fatal(err)
		}
	}
	f := &candidateTestFacts{t: t, subject: CandidateSubjectFacts{Found: true, OrgID: 1, KeyID: 8, KeyActive: true, PrincipalActive: true, Mode: "inherit_group", RoutingGroupID: &g.ID, KeyPolicyVersion: 3, KeyScopes: "", FeatureImages: false, FeatureVideo: false}, channels: []PublishedChannelFacts{
		{ID: 10, Enabled: true, Models: []string{"Alpha", "beta", "图片"}},
		{ID: 11, Enabled: true, Models: []string{"beta", "gamma"}},
		{ID: 12, OwnerOrgID: 2, Enabled: true, Models: []string{"foreign"}},
		{ID: 13, Enabled: false, Models: []string{"disabled"}},
	}}
	return s.DB, &CandidateResolver{Subjects: f, Channels: f}, f, CandidateSubjectRef{OrgID: 1, KeyID: 8}, at
}

func TestCandidatesMembershipOracleAndOrthogonalFacts(t *testing.T) {
	db, r, f, ref, at := candidateFixture(t)
	before := adminReadFingerprint(t, db)
	tx := db.Begin()
	f.pool = tx.Statement.ConnPool
	dirty := tx.Table("private_wrong").Select("credential_enc").Where("id=999").Limit(1)
	got, err := r.ListModelCandidatesTx(context.Background(), dirty, ref, ModelCandidatePage{}, at)
	want := []ModelCandidate{{"Alpha", []uint64{10}}, {"beta", []uint64{10, 11}}, {"gamma", []uint64{11}}, {"图片", []uint64{10}}}
	if err != nil || !reflect.DeepEqual(got.Items, want) || got.Observation.Assurance != "group_membership_only" || got.ContextFacts.FeatureVideo || got.ContextFacts.KeyScopes != "" {
		t.Fatal("candidate facts are not full permission", got, err)
	}
	for _, name := range []string{"alpha", "foreign", "disabled", "upstream-only", "missing"} {
		one, err := r.ResolveModelCandidatesTx(context.Background(), tx, ref, name, at)
		if err != nil || one.Candidate.ChannelIDs == nil || len(one.Candidate.ChannelIDs) != 0 {
			t.Fatal(name, one, err)
		}
	}
	if err := tx.Rollback().Error; err != nil {
		t.Fatal(err)
	}
	if after := adminReadFingerprint(t, db); after != before {
		t.Fatal("read changed policy data")
	}
}
func TestCandidatesSubjectModesAndDenials(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*CandidateSubjectFacts)
		want   error
	}{
		{"legacy", func(s *CandidateSubjectFacts) { s.Mode = "legacy"; s.RoutingGroupID = nil }, ErrCandidatesLegacyMode},
		{"legacy_with_group", func(s *CandidateSubjectFacts) { s.Mode = "legacy" }, ErrCandidatesInvalidFacts},
		{"unknown", func(s *CandidateSubjectFacts) { s.Mode = "inherit_group " }, ErrCandidatesInvalidFacts},
		{"zero_version", func(s *CandidateSubjectFacts) { s.KeyPolicyVersion = 0 }, ErrCandidatesInvalidFacts},
		{"empty_subset", func(s *CandidateSubjectFacts) { s.Mode = "subset"; s.ModelLimits = " , " }, ErrCandidatesInvalidFacts},
		{"inherit_limits", func(s *CandidateSubjectFacts) { s.ModelLimits = "beta" }, ErrCandidatesInvalidFacts},
		{"cross_org", func(s *CandidateSubjectFacts) { s.OrgID = 2 }, ErrCandidatesDenied},
		{"missing", func(s *CandidateSubjectFacts) { s.Found = false }, ErrCandidatesDenied},
		{"revoked", func(s *CandidateSubjectFacts) { s.KeyActive = false }, ErrCandidatesDenied},
		{"expired", func(s *CandidateSubjectFacts) { s.KeyExpired = true }, ErrCandidatesDenied},
		{"inactive_member", func(s *CandidateSubjectFacts) { s.PrincipalActive = false }, ErrCandidatesDenied},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			db, r, f, ref, at := candidateFixture(t)
			test.mutate(&f.subject)
			tx := db.Begin()
			defer tx.Rollback()
			got, err := r.ListModelCandidatesTx(context.Background(), tx, ref, ModelCandidatePage{}, at)
			if !errors.Is(err, test.want) || !reflect.DeepEqual(got, ModelCandidatePageResult{}) || f.calls != 1 {
				t.Fatal(got, err, f.calls)
			}
		})
	}
	t.Run("subset_intersection", func(t *testing.T) {
		db, r, f, ref, at := candidateFixture(t)
		f.subject.Mode = "subset"
		f.subject.ModelLimits = " beta , foreign,missing "
		tx := db.Begin()
		defer tx.Rollback()
		got, err := r.ListModelCandidatesTx(context.Background(), tx, ref, ModelCandidatePage{}, at)
		if err != nil || !reflect.DeepEqual(got.Items, []ModelCandidate{{"beta", []uint64{10, 11}}}) {
			t.Fatal(got, err)
		}
	})
}

func TestCandidatesPrivateGroupNeverUsesPlatformOrOtherOrg(t *testing.T) {
	db, r, f, ref, at := candidateFixture(t)
	tx := db.Begin()
	defer tx.Rollback()
	if err := tx.Exec("UPDATE routing_groups SET owner_org_id=1").Error; err != nil {
		t.Fatal(err)
	}
	f.channels = []PublishedChannelFacts{
		{ID: 10, Enabled: true, Models: []string{"platform"}},
		{ID: 11, OwnerOrgID: 1, Enabled: true, Models: []string{"own"}},
		{ID: 12, OwnerOrgID: 2, Enabled: true, Models: []string{"foreign"}},
	}
	got, err := r.ListModelCandidatesTx(context.Background(), tx, ref, ModelCandidatePage{}, at)
	if err != nil || !reflect.DeepEqual(got.Items, []ModelCandidate{{Model: "own", ChannelIDs: []uint64{11}}}) {
		t.Fatal(got, err)
	}
}
func TestCandidatesPolicyAndBridgeFaultsNeverReturnPartial(t *testing.T) {
	for _, change := range []string{"group_disabled", "grant_revoked", "group_version", "grant_version", "missing_grant", "bad_channel", "duplicate_channel", "database_error", "bridge_error"} {
		t.Run(change, func(t *testing.T) {
			db, r, f, ref, at := candidateFixture(t)
			tx := db.Begin()
			defer tx.Rollback()
			want := ErrCandidatesDenied
			switch change {
			case "group_disabled":
				tx.Exec("UPDATE routing_groups SET status='disabled'")
			case "grant_revoked":
				tx.Exec("UPDATE org_group_grants SET status='revoked'")
			case "group_version":
				tx.Exec("UPDATE routing_groups SET policy_version=0")
				want = ErrCandidatesInvalidFacts
			case "grant_version":
				tx.Exec("UPDATE org_group_grants SET grant_version=0")
				want = ErrCandidatesInvalidFacts
			case "missing_grant":
				tx.Exec("DELETE FROM org_group_grants")
			case "bad_channel":
				f.channels = append(f.channels, PublishedChannelFacts{ID: 999})
				want = ErrCandidatesInvalidFacts
			case "duplicate_channel":
				f.channels = append(f.channels, f.channels[0])
				want = ErrCandidatesInvalidFacts
			case "database_error":
				tx.Exec("DROP TABLE channel_group_bindings")
				want = ErrCandidatesUnavailable
			case "bridge_error":
				f.err = fmt.Errorf("private database payload: %w", ErrCandidatesUnavailable)
				want = ErrCandidatesUnavailable
			}
			got, err := r.ListModelCandidatesTx(context.Background(), tx, ref, ModelCandidatePage{}, at)
			if !errors.Is(err, want) || !reflect.DeepEqual(got, ModelCandidatePageResult{}) || strings.Contains(err.Error(), "private") {
				t.Fatal(change, got, err)
			}
		})
	}
}
func TestCandidatesBoundedPaginationAndSingleModel(t *testing.T) {
	db, r, f, ref, at := candidateFixture(t)
	models := make([]string, 237)
	for i := range models {
		models[i] = fmt.Sprintf("m-%03d", i)
	}
	f.channels = []PublishedChannelFacts{{ID: 10, Enabled: true, Models: models}}
	tx := db.Begin()
	defer tx.Rollback()
	for _, size := range []int{20, 50, 100} {
		all := []string{}
		cursor := ""
		for {
			page, err := r.ListModelCandidatesTx(context.Background(), tx, ref, ModelCandidatePage{AfterModel: cursor, Limit: size}, at)
			if err != nil {
				t.Fatal(err)
			}
			for _, item := range page.Items {
				all = append(all, item.Model)
			}
			if !page.HasMore {
				if page.NextAfterModel != "" {
					t.Fatal("terminal cursor")
				}
				break
			}
			cursor = page.NextAfterModel
		}
		if !reflect.DeepEqual(all, models) {
			t.Fatal("cross-page omission", size, len(all))
		}
	}
	for i := 237; i <= 1000; i++ {
		f.channels[0].Models = append(f.channels[0].Models, fmt.Sprintf("m-%04d", i))
	}
	sort.Strings(f.channels[0].Models)
	if _, err := r.ListModelCandidatesTx(context.Background(), tx, ref, ModelCandidatePage{}, at); !errors.Is(err, ErrCandidatesLimit) {
		t.Fatal("directory bound", err)
	}
	if got, err := r.ResolveModelCandidatesTx(context.Background(), tx, ref, "m-000", at); err != nil || !reflect.DeepEqual(got.Candidate.ChannelIDs, []uint64{10}) {
		t.Fatal("single model built unneeded full directory", got, err)
	}
}
func TestCandidatesBindingLimitAndInvalidCalls(t *testing.T) {
	db, r, f, ref, at := candidateFixture(t)
	tx := db.Begin()
	defer tx.Rollback()
	for _, model := range []string{"", " model", strings.Repeat("a", 129), "bad\x00model"} {
		if _, err := r.ResolveModelCandidatesTx(context.Background(), tx, ref, model, at); !errors.Is(err, ErrCandidatesInvalid) {
			t.Fatal(err)
		}
	}
	for _, bad := range []*gorm.DB{nil, {}, db, tx.Session(&gorm.Session{DryRun: true})} {
		if _, err := r.ResolveModelCandidatesTx(context.Background(), bad, ref, "model", at); !errors.Is(err, ErrCandidatesInvalid) {
			t.Fatal(err)
		}
	}
	if f.calls != 0 {
		t.Fatal("invalid call read facts")
	}
	for i := 15; i <= 110; i++ {
		if err := tx.Exec("INSERT INTO channel_group_bindings(group_id,channel_id,created_at,updated_at) VALUES(1,?,?,?)", i, at, at).Error; err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.ListModelCandidatesTx(context.Background(), tx, ref, ModelCandidatePage{}, at); !errors.Is(err, ErrCandidatesLimit) {
		t.Fatal("101 bindings accepted", err)
	}
}
func TestCandidatesCancellationAndDeadlineAreSafe(t *testing.T) {
	db, r, f, ref, at := candidateFixture(t)
	tx := db.Begin()
	defer tx.Rollback()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.ListModelCandidatesTx(ctx, tx, ref, ModelCandidatePage{}, at); !errors.Is(err, context.Canceled) || !errors.Is(err, ErrCandidatesUnavailable) || f.calls != 0 {
		t.Fatal(err, f.calls)
	}
	if err := db.Callback().Query().Before("gorm:query").Register("candidate_timeout", func(q *gorm.DB) { <-q.Statement.Context.Done() }); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	got, err := r.ListModelCandidatesTx(context.Background(), tx, ref, ModelCandidatePage{}, at)
	if !errors.Is(err, context.DeadlineExceeded) || !reflect.DeepEqual(got, ModelCandidatePageResult{}) || time.Since(start) > time.Second {
		t.Fatal(got, err, time.Since(start))
	}
}

func TestCandidatesMissingPolicyAfterCancellationIsNotDenied(t *testing.T) {
	for _, table := range []string{"routing_groups", "org_group_grants"} {
		t.Run(table, func(t *testing.T) {
			db, r, _, ref, at := candidateFixture(t)
			tx := db.Begin()
			defer tx.Rollback()
			if err := tx.Exec("DELETE FROM " + table).Error; err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if err := db.Callback().Query().After("gorm:query").Register("candidate_missing_policy_cancel", func(q *gorm.DB) {
				if q.Statement.Table == table {
					cancel()
				}
			}); err != nil {
				t.Fatal(err)
			}
			got, err := r.ListModelCandidatesTx(ctx, tx, ref, ModelCandidatePage{}, at)
			if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrCandidatesUnavailable) || errors.Is(err, ErrCandidatesDenied) || !reflect.DeepEqual(got, ModelCandidatePageResult{}) {
				t.Fatal("cancel became policy denial", got, err)
			}
		})
	}
}
