package server

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/routinggroup"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

// These tests reuse the randomly named, tracked-migration private MySQL schema
// from B1. They never register a candidate HTTP route or enable grouped keys.
type routingCandidateFixture struct {
	*routingAdminHTTPFixture
	resolver              *routinggroup.CandidateResolver
	ref                   routinggroup.CandidateSubjectRef
	group, owner, version uint64
	plain                 string
	at                    time.Time
}

func newRoutingCandidateFixture(t *testing.T, owner uint64) *routingCandidateFixture {
	t.Helper()
	f := newRoutingAdminMySQLFixture(t)
	routingAdminSeedPreservedFacts(t, f.db)
	group := routingOrgMySQLGroup(t, f, owner, "candidate-fixture")
	routingOrgMySQLGrant(t, f, 1, group, 0, routinggroup.Enabled)
	user := uint64(3)
	plain, key, err := identity.IssueKey(f.db, identity.IssueKeyInput{OrgID: 1, UserID: &user, Name: "candidate-key", PolicyAudit: routinggroup.Audit{ActorUserID: 1, RequestID: "candidate-key-fixture"}})
	if err != nil {
		t.Fatal("cannot issue legacy control key")
	}
	if _, err := identity.VerifyKey(f.db, plain, time.Now()); err != nil || !key.AllowsModel("arbitrary-legacy-model") {
		t.Fatal("legacy control did not work before grouped fixture")
	}
	// Only a test fixture writes a grouped row, using the real 0129 CHECKs.
	routingAdminSQL(t, f.db, "UPDATE api_keys SET model_access_mode='inherit_group',routing_group_id=?,policy_version=7 WHERE id=?", group, key.ID)
	routingAdminSQL(t, f.db, "INSERT INTO org_entitlements(org_id,feature_images,feature_video) VALUES(1,0,0) ON DUPLICATE KEY UPDATE feature_images=0,feature_video=0")
	c := &routingCandidateFixture{routingAdminHTTPFixture: f, resolver: &routinggroup.CandidateResolver{Subjects: routingCandidateSubjectBridge{}, Channels: routingPublishedChannelBridge{}}, ref: routinggroup.CandidateSubjectRef{OrgID: 1, KeyID: key.ID}, group: group, owner: owner, version: 2, plain: plain, at: time.Now().UTC().Truncate(time.Millisecond)}
	t.Cleanup(func() { c.assertGroupedClosed(t) })
	return c
}

func (f *routingCandidateFixture) assertGroupedClosed(t *testing.T) {
	t.Helper()
	before := routingAdminFingerprints(t, f.db, "api_keys", "routing_policy_events")
	var key identity.APIKey
	if err := f.db.Where("id=?", f.ref.KeyID).Take(&key).Error; err != nil {
		t.Fatal("cannot read grouped gate control")
	}
	if key.AllowsModel("alpha") {
		t.Error("candidate reader enabled AllowsModel")
	}
	if _, err := identity.VerifyKey(f.db, f.plain, f.at); err == nil {
		t.Error("candidate reader enabled grouped VerifyKey")
	}
	for _, mode := range []string{"inherit_group", "subset"} {
		limits := ""
		if mode == "subset" {
			limits = "alpha"
		}
		plain, key, err := identity.IssueKey(f.db, identity.IssueKeyInput{OrgID: 1, Name: "must-not-issue", RoutingGroupID: &f.group, ModelAccessMode: &mode, ModelLimits: limits})
		if err == nil || plain != "" || key != nil {
			t.Error("grouped IssueKey opened by candidate implementation")
		}
	}
	if !reflect.DeepEqual(before, routingAdminFingerprints(t, f.db, "api_keys", "routing_policy_events")) {
		t.Error("grouped gate checks changed Key/event facts")
	}
}

func (f *routingCandidateFixture) channel(t *testing.T, id uint64, models string) {
	t.Helper()
	routingAdminSQL(t, f.db, "INSERT INTO channels(id,name,provider,base_url,credential_enc,models,owner_org_id) VALUES(?,'synthetic-private-name','test','https://never-send.invalid','never-expose-credential',?,?)", id, models, f.owner)
	group, changed, err := f.s.routingAdminWriter().SetBinding(context.Background(), routinggroup.BindingInput{OwnerOrgID: f.owner, GroupID: f.group, ChannelID: id, ExpectedVersion: f.version, Bound: true, Audit: routinggroup.Audit{ActorUserID: 1, RequestID: fmt.Sprintf("candidate-bind-%d", id)}})
	if err != nil || !changed {
		t.Fatal("cannot bind fixture channel through configuration domain")
	}
	f.version = group.PolicyVersion
}

func (f *routingCandidateFixture) list(ctx context.Context, page routinggroup.ModelCandidatePage) (out routinggroup.ModelCandidatePageResult, err error) {
	err = f.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		out, err = f.resolver.ListModelCandidatesTx(ctx, tx, f.ref, page, f.at)
		return err
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	return
}
func (f *routingCandidateFixture) resolve(ctx context.Context, model string) (out routinggroup.ModelCandidates, err error) {
	err = f.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		out, err = f.resolver.ResolveModelCandidatesTx(ctx, tx, f.ref, model, f.at)
		return err
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	return
}
func candidateItems(t *testing.T, got []routinggroup.ModelCandidate, want []routinggroup.ModelCandidate) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("candidate facts differ: got=%v want=%v", got, want)
	}
}

func TestMySQLRoutingCandidatesOracleAndNoSideEffects(t *testing.T) {
	f := newRoutingCandidateFixture(t, 0)
	f.channel(t, 10, "alpha, Alpha ,中,public,public")
	f.channel(t, 11, "alpha,βeta")
	f.channel(t, 12, "disabled")
	f.channel(t, 13, "dangling")
	f.channel(t, 14, "foreign")
	routingAdminSQL(t, f.db, "UPDATE channels SET status=0 WHERE id=12")
	routingAdminSQL(t, f.db, "DELETE FROM channels WHERE id=13")
	routingAdminSQL(t, f.db, "UPDATE channels SET owner_org_id=2 WHERE id=14")
	routingAdminSQL(t, f.db, "UPDATE channels SET model_mapping='public=upstream-only',detected_models='detected-only' WHERE id=10")
	tables := []string{"api_keys", "routing_policy_events", "routing_groups", "org_group_grants", "channel_group_bindings", "channels", "ledger_accounts", "ledger_journal", "ledger_holds", "ledger_operations", "ledger_movements", "tasks", "video_operations", "video_billing_snapshots", "model_prices"}
	before := routingAdminFingerprints(t, f.db, tables...)
	want := []routinggroup.ModelCandidate{{Model: "Alpha", ChannelIDs: []uint64{10}}, {Model: "alpha", ChannelIDs: []uint64{10, 11}}, {Model: "public", ChannelIDs: []uint64{10}}, {Model: "βeta", ChannelIDs: []uint64{11}}, {Model: "中", ChannelIDs: []uint64{10}}}
	out, err := f.list(context.Background(), routinggroup.ModelCandidatePage{})
	if err != nil {
		t.Fatal("real candidate list failed", err)
	}
	candidateItems(t, out.Items, want)
	if out.Observation.Assurance != "group_membership_only" || out.Observation.OrgID != 1 || out.Observation.KeyID != f.ref.KeyID || out.Observation.GroupID != f.group || out.Observation.KeyPolicyVersion != 7 || out.Observation.GroupPolicyVersion != f.version || out.Observation.GrantVersion != 1 || !out.Observation.CheckedAt.Equal(f.at) {
		t.Fatal("candidate observation misstates its scope/version/assurance")
	}
	if out.ContextFacts.FeatureVideo || out.ContextFacts.FeatureImages || out.ContextFacts.KeyScopes != "" {
		t.Fatal("orthogonal false features/empty scopes were upgraded to permission")
	}
	for _, missing := range []string{"upstream-only", "detected-only", "ALPHA", "disabled", "dangling", "foreign"} {
		row, e := f.resolve(context.Background(), missing)
		if e != nil || row.Candidate.ChannelIDs == nil || len(row.Candidate.ChannelIDs) != 0 {
			t.Fatal("unpublished/foreign model became candidate", missing, e)
		}
	}
	if !reflect.DeepEqual(before, routingAdminFingerprints(t, f.db, tables...)) {
		t.Fatal("read-only candidate resolution changed durable facts")
	}
	routingAdminSQL(t, f.db, "UPDATE api_keys SET model_access_mode='subset',model_limits='public, alpha,not-published' WHERE id=?", f.ref.KeyID)
	out, err = f.list(context.Background(), routinggroup.ModelCandidatePage{Limit: 1})
	if err != nil || !out.HasMore || out.NextAfterModel != "alpha" {
		t.Fatal("subset first page mismatch", err)
	}
	candidateItems(t, out.Items, []routinggroup.ModelCandidate{{Model: "alpha", ChannelIDs: []uint64{10, 11}}})
	out, err = f.list(context.Background(), routinggroup.ModelCandidatePage{AfterModel: "alpha", Limit: 1})
	if err != nil || out.HasMore || out.NextAfterModel != "" {
		t.Fatal("subset last page mismatch", err)
	}
	candidateItems(t, out.Items, []routinggroup.ModelCandidate{{Model: "public", ChannelIDs: []uint64{10}}})
}

type candidateObservedSQL struct {
	table, sql string
	vars       []any
	limit      int
	deadline   time.Duration
}

func candidateCapture(t *testing.T, db *gorm.DB, pool gorm.ConnPool) (func() []candidateObservedSQL, func()) {
	t.Helper()
	var mu sync.Mutex
	queries := []candidateObservedSQL{}
	name := "candidate_observe_" + strings.ReplaceAll(t.Name(), "/", "_")
	err := db.Callback().Query().After("gorm:query").Register(name, func(q *gorm.DB) {
		if q.Statement.ConnPool != pool {
			t.Error("candidate query escaped caller transaction")
		}
		deadline, ok := q.Statement.Context.Deadline()
		if !ok || time.Until(deadline) > 201*time.Millisecond {
			t.Error("candidate SELECT lacks <=200ms context")
		}
		n := 0
		if expression, ok := q.Statement.Clauses["LIMIT"].Expression.(clause.Limit); ok && expression.Limit != nil {
			n = *expression.Limit
		}
		mu.Lock()
		queries = append(queries, candidateObservedSQL{q.Statement.Table, q.Statement.SQL.String(), append([]any(nil), q.Statement.Vars...), n, time.Until(deadline)})
		mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	return func() []candidateObservedSQL {
		mu.Lock()
		defer mu.Unlock()
		return append([]candidateObservedSQL(nil), queries...)
	}, func() { _ = db.Callback().Query().Remove(name) }
}

func TestMySQLRoutingCandidatesBindingBoundsAndActualPlans(t *testing.T) {
	f := newRoutingCandidateFixture(t, 0)
	for id := uint64(10); id < 110; id++ {
		f.channel(t, id, "m")
	}
	tx := f.db.Begin(&sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	captured, remove := candidateCapture(t, f.db, tx.Statement.ConnPool)
	out, err := f.resolver.ResolveModelCandidatesTx(context.Background(), tx, f.ref, "m", f.at)
	remove()
	if e := tx.Rollback().Error; e != nil {
		t.Fatal(e)
	}
	if err != nil || len(out.Candidate.ChannelIDs) != 100 {
		t.Fatal("100 bindings must remain a complete successful candidate", err)
	}
	for i, id := range out.Candidate.ChannelIDs {
		if id != uint64(i+10) {
			t.Fatal("independent contiguous channel oracle mismatch")
		}
	}
	queries := captured()
	if len(queries) != 7 {
		t.Fatalf("business SELECT count=%d, want exactly 7 with nonempty facts", len(queries))
	}
	seen := map[string]bool{}
	for _, q := range queries {
		if !regexp.MustCompile(`MAX_EXECUTION_TIME\((?:[1-9][0-9]?|1[0-9]{2}|200)\)`).MatchString(q.sql) || strings.Contains(strings.ToUpper(q.sql), " OFFSET ") {
			t.Fatal("actual SELECT lacks bounded hint or uses OFFSET")
		}
		if q.table == "channel_group_bindings" && q.limit != 101 {
			t.Fatal("binding read did not use actual LIMIT 101")
		}
		for _, forbidden := range []string{"credential_enc", "model_mapping", "detected_models", "key_hash", "allow_ips", "password"} {
			if strings.Contains(q.sql, forbidden) {
				t.Fatal("reader selected forbidden private column", forbidden)
			}
		}
		if q.table != "org_group_grants" && q.table != "channel_group_bindings" {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		var plan []struct {
			Table string
			Type  string
			Key   *string
			Rows  uint64
		}
		e := f.db.WithContext(ctx).Raw("EXPLAIN "+q.sql, q.vars...).Scan(&plan).Error
		cancel()
		if e != nil || len(plan) == 0 {
			t.Fatal("actual candidate SQL could not be explained")
		}
		for _, p := range plan {
			if p.Table == q.table {
				seen[q.table] = true
				if p.Key == nil || p.Type == "ALL" {
					t.Fatal("relation lookup did not use an existing index", q.table, p.Type)
				}
				t.Logf("candidate plan table=%s access=%s key=%s rows=%d", p.Table, p.Type, *p.Key, p.Rows)
			}
		}
	}
	if !seen["org_group_grants"] || !seen["channel_group_bindings"] {
		t.Fatal("missing real relationship plan controls")
	}
	f.channel(t, 110, "m")
	var count atomic.Int32
	name := "candidate_overflow_queries"
	_ = f.db.Callback().Query().After("gorm:query").Register(name, func(*gorm.DB) { count.Add(1) })
	page, e := f.list(context.Background(), routinggroup.ModelCandidatePage{})
	_ = f.db.Callback().Query().Remove(name)
	if !errors.Is(e, routinggroup.ErrCandidatesLimit) || !reflect.DeepEqual(page, routinggroup.ModelCandidatePageResult{}) || count.Load() != 6 {
		t.Fatal("101 bindings returned partial success or queried channels after overflow", e, count.Load())
	}
}

func TestMySQLRoutingCandidatesModelBoundsAndSeek(t *testing.T) {
	f := newRoutingCandidateFixture(t, 0)
	names := make([]string, 1000)
	for i := range names {
		names[i] = fmt.Sprintf("m%04d", i)
	}
	f.channel(t, 10, strings.Join(names, ","))
	for _, limit := range []int{20, 50, 100} {
		out, err := f.list(context.Background(), routinggroup.ModelCandidatePage{Limit: limit})
		if err != nil || len(out.Items) != limit || !out.HasMore || out.NextAfterModel != names[limit-1] {
			t.Fatal("1000-model page boundary failed", limit, err)
		}
		for i, row := range out.Items {
			if row.Model != fmt.Sprintf("m%04d", i) || !reflect.DeepEqual(row.ChannelIDs, []uint64{10}) {
				t.Fatal("fixed ordinal model oracle mismatch")
			}
		}
	}
	last, err := f.list(context.Background(), routinggroup.ModelCandidatePage{AfterModel: "m0989", Limit: 20})
	if err != nil || len(last.Items) != 10 || last.HasMore || last.NextAfterModel != "" || last.Items[0].Model != "m0990" || last.Items[9].Model != "m0999" {
		t.Fatal("tail seek oracle failed", err)
	}
	empty, err := f.list(context.Background(), routinggroup.ModelCandidatePage{AfterModel: "m0999"})
	if err != nil || empty.Items == nil || len(empty.Items) != 0 || empty.HasMore {
		t.Fatal("empty catalog page became error or nil", err)
	}
	routingAdminSQL(t, f.db, "UPDATE channels SET models=? WHERE id=10", strings.Join(append(names, "m1000"), ","))
	failed, err := f.list(context.Background(), routinggroup.ModelCandidatePage{Limit: 20})
	if !errors.Is(err, routinggroup.ErrCandidatesLimit) || !reflect.DeepEqual(failed, routinggroup.ModelCandidatePageResult{}) {
		t.Fatal("1001 catalog models yielded partial success", err)
	}
	one, err := f.resolve(context.Background(), "m1000")
	if err != nil || !reflect.DeepEqual(one.Candidate.ChannelIDs, []uint64{10}) {
		t.Fatal("single-model lookup unnecessarily built/rejected full catalog", err)
	}
}

func TestMySQLRoutingCandidatesFieldAndBatchByteBounds(t *testing.T) {
	f := newRoutingCandidateFixture(t, 0)
	f.channel(t, 10, "m")
	routingAdminSQL(t, f.db, "UPDATE api_keys SET model_access_mode='subset',model_limits=? WHERE id=?", "m"+strings.Repeat(" ", 32767), f.ref.KeyID)
	if _, err := f.resolve(context.Background(), "m"); err != nil {
		t.Fatal("exact model_limits bound rejected", err)
	}
	routingAdminSQL(t, f.db, "UPDATE api_keys SET model_limits=? WHERE id=?", "m"+strings.Repeat(" ", 32768), f.ref.KeyID)
	bad, err := f.resolve(context.Background(), "m")
	if !errors.Is(err, routinggroup.ErrCandidatesLimit) || !reflect.DeepEqual(bad, routinggroup.ModelCandidates{}) {
		t.Fatal("oversized model limits became unlimited/partial", err)
	}
	routingAdminSQL(t, f.db, "UPDATE api_keys SET model_access_mode='inherit_group',model_limits='',scopes=? WHERE id=?", strings.Repeat("s", 255), f.ref.KeyID)
	if out, err := f.resolve(context.Background(), "m"); err != nil || len(out.ContextFacts.KeyScopes) != 255 {
		t.Fatal("real VARCHAR scopes bound failed", err)
	}
	for _, test := range []struct {
		sql  string
		args []any
	}{{"UPDATE api_keys SET scopes=? WHERE id=?", []any{strings.Repeat("s", 256), f.ref.KeyID}}, {"UPDATE channels SET models=? WHERE id=10", []any{strings.Repeat(" ", 65535) + "m"}}} {
		err := f.db.Exec(test.sql, test.args...).Error
		var driver *mysqlDriver.MySQLError
		if !errors.As(err, &driver) || driver.Number != 1406 {
			t.Fatal("tracked schema did not enforce its stricter VARCHAR/TEXT bound")
		}
	}
	// Current TEXT caps at 65535 before the defensive reader's 65536 threshold.
	maximum := "m" + strings.Repeat(" ", 65534)
	routingAdminSQL(t, f.db, "UPDATE channels SET models=? WHERE id=10", maximum)
	if _, err := f.resolve(context.Background(), "m"); err != nil {
		t.Fatal("real TEXT max rejected", err)
	}
	for id := uint64(11); id <= 25; id++ {
		f.channel(t, id, maximum)
	}
	f.channel(t, 26, "m"+strings.Repeat(" ", 15)) // 16*65535 + 16 = 1MiB.
	out, err := f.resolve(context.Background(), "m")
	if err != nil || len(out.Candidate.ChannelIDs) != 17 {
		t.Fatal("exact cumulative 1MiB bound rejected", err)
	}
	routingAdminSQL(t, f.db, "UPDATE channels SET models=? WHERE id=26", "m"+strings.Repeat(" ", 16))
	out, err = f.resolve(context.Background(), "m")
	if !errors.Is(err, routinggroup.ErrCandidatesLimit) || !reflect.DeepEqual(out, routinggroup.ModelCandidates{}) {
		t.Fatal("cumulative 1MiB+1 returned partial facts", err)
	}
	routingAdminSQL(t, f.db, "UPDATE channels SET models='m' WHERE id BETWEEN 11 AND 26")
	maxName := strings.Repeat("😀", 128)
	routingAdminSQL(t, f.db, "UPDATE channels SET models=? WHERE id=10", maxName)
	if one, err := f.resolve(context.Background(), maxName); err != nil || !reflect.DeepEqual(one.Candidate.ChannelIDs, []uint64{10}) {
		t.Fatal("128-rune/512-byte name boundary failed", err)
	}
	for _, invalid := range []string{strings.Repeat("😀", 129), "bad\x00model", "bad\nmodel"} {
		routingAdminSQL(t, f.db, "UPDATE channels SET models=? WHERE id=10", invalid)
		page, e := f.list(context.Background(), routinggroup.ModelCandidatePage{})
		if !errors.Is(e, routinggroup.ErrCandidatesInvalidFacts) || !reflect.DeepEqual(page, routinggroup.ModelCandidatePageResult{}) {
			t.Fatal("invalid public name was silently skipped", e)
		}
	}
}

func TestMySQLRoutingCandidatesSnapshotReadOnlyAndSameTxRollback(t *testing.T) {
	f := newRoutingCandidateFixture(t, 0)
	f.channel(t, 10, "before")
	routingAdminSQL(t, f.db, "INSERT INTO channels(id,name,provider,base_url,credential_enc,models,owner_org_id) VALUES(11,'new','test','https://never-send.invalid','synthetic','new-binding',0)")
	var once atomic.Bool
	var hookErr error
	readOnly := false
	name := "candidate_real_rr"
	if err := f.db.Callback().Query().After("gorm:query").Register(name, func(q *gorm.DB) {
		if q.Statement.Table != "api_keys" || !once.CompareAndSwap(false, true) {
			return
		}
		e := q.Session(&gorm.Session{NewDB: true, Context: q.Statement.Context}).Exec("UPDATE routing_groups SET name='must-not-write' WHERE id=?", f.group).Error
		var driver *mysqlDriver.MySQLError
		readOnly = errors.As(e, &driver) && driver.Number == 1792
		hookErr = f.db.Transaction(func(other *gorm.DB) error {
			if e := other.Exec("UPDATE channels SET models='after' WHERE id=10").Error; e != nil {
				return e
			}
			if e := other.Exec("UPDATE org_group_grants SET grant_version=grant_version+1 WHERE org_id=1 AND group_id=?", f.group).Error; e != nil {
				return e
			}
			if e := other.Exec("UPDATE routing_groups SET policy_version=policy_version+1 WHERE id=?", f.group).Error; e != nil {
				return e
			}
			return other.Create(&routinggroup.Binding{GroupID: f.group, ChannelID: 11, CreatedAt: f.at, UpdatedAt: f.at}).Error
		})
	}); err != nil {
		t.Fatal(err)
	}
	out, err := f.list(context.Background(), routinggroup.ModelCandidatePage{})
	_ = f.db.Callback().Query().Remove(name)
	if err != nil || hookErr != nil || !once.Load() || !readOnly || out.Observation.GroupPolicyVersion != f.version || out.Observation.GrantVersion != 1 {
		t.Fatal("real two-connection read-only RR did not preserve one snapshot", err, hookErr)
	}
	candidateItems(t, out.Items, []routinggroup.ModelCandidate{{Model: "before", ChannelIDs: []uint64{10}}})
	out, err = f.list(context.Background(), routinggroup.ModelCandidatePage{})
	if err != nil || out.Observation.GrantVersion != 2 || out.Observation.GroupPolicyVersion != f.version+1 {
		t.Fatal("new request did not observe committed relationship versions", err)
	}
	candidateItems(t, out.Items, []routinggroup.ModelCandidate{{Model: "after", ChannelIDs: []uint64{10}}, {Model: "new-binding", ChannelIDs: []uint64{11}}})
	before := routingAdminFingerprints(t, f.db, "channels", "org_group_grants", "routing_policy_events")
	tx := f.db.Begin(&sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	routingAdminSQL(t, tx, "UPDATE channels SET models='uncommitted' WHERE id=10")
	routingAdminSQL(t, tx, "UPDATE org_group_grants SET grant_version=grant_version+1 WHERE org_id=1 AND group_id=?", f.group)
	queries, remove := candidateCapture(t, f.db, tx.Statement.ConnPool)
	inside, e := f.resolver.ResolveModelCandidatesTx(context.Background(), tx, f.ref, "uncommitted", f.at)
	remove()
	if e != nil || inside.Observation.GrantVersion != 3 || !reflect.DeepEqual(inside.Candidate.ChannelIDs, []uint64{10}) || len(queries()) != 7 {
		t.Fatal("candidate readers missed same-transaction writes", e)
	}
	if e := tx.Rollback().Error; e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(before, routingAdminFingerprints(t, f.db, "channels", "org_group_grants", "routing_policy_events")) {
		t.Fatal("caller rollback did not restore untouched persistent facts")
	}
}

type candidateLogBuffer struct {
	mu sync.Mutex
	bytes.Buffer
}

func (b *candidateLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.Write(p)
}
func (b *candidateLogBuffer) value() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.String()
}
func (b *candidateLogBuffer) clear() { b.mu.Lock(); defer b.mu.Unlock(); b.Buffer.Reset() }

func TestMySQLRoutingCandidatesDatabaseErrorsAndLogSafety(t *testing.T) {
	f := newRoutingCandidateFixture(t, 0)
	f.channel(t, 10, "private-model-canary")
	buf := &candidateLogBuffer{}
	warn := logger.New(log.New(buf, "", 0), logger.Config{SlowThreshold: time.Nanosecond, LogLevel: logger.Warn, Colorful: false})
	f.db = f.db.Session(&gorm.Session{Logger: warn})
	_ = f.db.Exec("SELECT candidate_log_positive_control_missing_column").Error
	if !strings.Contains(buf.value(), "candidate_log_positive_control_missing_column") {
		t.Fatal("Warn SQL logger positive control did not record normal failure")
	}
	buf.clear()
	for stage := 1; stage <= 7; stage++ {
		t.Run(fmt.Sprintf("select_%d", stage), func(t *testing.T) {
			var n atomic.Int32
			var actualDriver atomic.Bool
			name := fmt.Sprintf("candidate_sql_fault_%d", stage)
			if err := f.db.Callback().Query().Before("gorm:query").Register(name, func(q *gorm.DB) {
				if n.Add(1) == int32(stage) {
					// Parameterized projections already have a SELECT clause (the
					// principal EXISTS is one). Remove it so this changes the SQL
					// actually sent, not an unused Statement.Selects value.
					delete(q.Statement.Clauses, "SELECT")
					q.Statement.Selects = []string{"candidate_private_missing_column"}
				}
			}); err != nil {
				t.Fatal(err)
			}
			after := name + "_after"
			_ = f.db.Callback().Query().After("gorm:query").Register(after, func(q *gorm.DB) {
				var driver *mysqlDriver.MySQLError
				if errors.As(q.Error, &driver) && driver.Number == 1054 {
					actualDriver.Store(true)
				}
			})
			out, err := f.list(context.Background(), routinggroup.ModelCandidatePage{})
			_ = f.db.Callback().Query().Remove(name)
			_ = f.db.Callback().Query().Remove(after)
			if !actualDriver.Load() || !errors.Is(err, routinggroup.ErrCandidatesUnavailable) || !reflect.DeepEqual(out, routinggroup.ModelCandidatePageResult{}) {
				t.Fatal("actual SQL failure became missing/partial success", stage, err)
			}
			if strings.Contains(err.Error(), "candidate_private_missing_column") || strings.Contains(err.Error(), "private-model-canary") || strings.Contains(buf.value(), "candidate_private_missing_column") || strings.Contains(buf.value(), "SELECT") {
				t.Fatal("candidate query leaked driver SQL or private data")
			}
			buf.clear()
		})
	}
}

func TestMySQLRoutingCandidatesPrincipalAndPrivateOwnership(t *testing.T) {
	f := newRoutingCandidateFixture(t, 1)
	f.channel(t, 10, "private")
	if out, err := f.resolve(context.Background(), "private"); err != nil || !reflect.DeepEqual(out.Candidate.ChannelIDs, []uint64{10}) {
		t.Fatal("same-org private group positive control failed", err)
	}
	routingAdminSQL(t, f.db, "UPDATE api_keys SET model_access_mode='legacy',routing_group_id=NULL WHERE id=?", f.ref.KeyID)
	legacy, err := f.list(context.Background(), routinggroup.ModelCandidatePage{})
	if !errors.Is(err, routinggroup.ErrCandidatesLegacyMode) || !reflect.DeepEqual(legacy, routinggroup.ModelCandidatePageResult{}) {
		t.Fatal("legacy key was treated as a grouped candidate/default", err)
	}
	if key, err := identity.VerifyKey(f.db, f.plain, f.at); err != nil || !key.AllowsModel("arbitrary-legacy-model") {
		t.Fatal("candidate query altered existing legacy authorization")
	}
	routingAdminSQL(t, f.db, "UPDATE api_keys SET model_access_mode='inherit_group',routing_group_id=? WHERE id=?", f.group, f.ref.KeyID)
	var foreign routinggroup.ModelCandidatePageResult
	err = f.db.Transaction(func(tx *gorm.DB) error {
		var e error
		foreign, e = f.resolver.ListModelCandidatesTx(context.Background(), tx, routinggroup.CandidateSubjectRef{OrgID: 2, KeyID: f.ref.KeyID}, routinggroup.ModelCandidatePage{}, f.at)
		return e
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if !errors.Is(err, routinggroup.ErrCandidatesDenied) || !reflect.DeepEqual(foreign, routinggroup.ModelCandidatePageResult{}) {
		t.Fatal("cross-org key reference exposed candidate facts", err)
	}
	cases := []struct {
		name, apply, undo string
		args              []any
	}{
		{"org_disabled", "UPDATE organizations SET status=0 WHERE id=1", "UPDATE organizations SET status=1 WHERE id=1", nil},
		{"user_disabled", "UPDATE users SET status=0 WHERE id=3", "UPDATE users SET status=1 WHERE id=3", nil},
		{"membership_removed", "DELETE FROM org_members WHERE org_id=1 AND user_id=3", "INSERT INTO org_members(org_id,user_id,role) VALUES(1,3,'owner')", nil},
		{"key_revoked", "UPDATE api_keys SET status=0 WHERE id=?", "UPDATE api_keys SET status=1 WHERE id=?", []any{f.ref.KeyID}},
		{"group_disabled", "UPDATE routing_groups SET status='disabled' WHERE id=?", "UPDATE routing_groups SET status='enabled' WHERE id=?", []any{f.group}},
		{"grant_revoked", "UPDATE org_group_grants SET status='revoked' WHERE group_id=?", "UPDATE org_group_grants SET status='enabled' WHERE group_id=?", []any{f.group}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			routingAdminSQL(t, f.db, tc.apply, tc.args...)
			defer routingAdminSQL(t, f.db, tc.undo, tc.args...)
			out, err := f.list(context.Background(), routinggroup.ModelCandidatePage{})
			if !errors.Is(err, routinggroup.ErrCandidatesDenied) || !reflect.DeepEqual(out, routinggroup.ModelCandidatePageResult{}) {
				t.Fatal("unavailable principal/group not denied", err)
			}
		})
	}
	routingAdminSQL(t, f.db, "UPDATE channels SET owner_org_id=2 WHERE id=10")
	out, err := f.list(context.Background(), routinggroup.ModelCandidatePage{})
	if err != nil || out.Items == nil || len(out.Items) != 0 {
		t.Fatal("cross-org drift did not yield legal empty channel set", err)
	}
	routingAdminSQL(t, f.db, "UPDATE channels SET owner_org_id=1 WHERE id=10")
	routingAdminSQL(t, f.db, "UPDATE api_keys SET expires_at=? WHERE id=?", f.at, f.ref.KeyID)
	if _, err := f.resolve(context.Background(), "private"); err != nil {
		t.Fatal("changed existing exact expiry-boundary semantics", err)
	}
	f.at = f.at.Add(time.Millisecond)
	if _, err := f.resolve(context.Background(), "private"); !errors.Is(err, routinggroup.ErrCandidatesDenied) {
		t.Fatal("expired subject was accepted", err)
	}
	routingAdminSQL(t, f.db, "UPDATE api_keys SET expires_at=NULL WHERE id=?", f.ref.KeyID)
	routingAdminSQL(t, f.db, "DELETE FROM org_entitlements WHERE org_id=1")
	if out, err := f.resolve(context.Background(), "private"); err != nil || !out.ContextFacts.FeatureImages || !out.ContextFacts.FeatureVideo {
		t.Fatal("missing entitlement did not preserve explicit domain defaults", err)
	}
	other := routingOrgMySQLGroup(t, f.routingAdminHTTPFixture, 2, "foreign-private")
	routingAdminSQL(t, f.db, "UPDATE api_keys SET routing_group_id=? WHERE id=?", other, f.ref.KeyID)
	denied, err := f.list(context.Background(), routinggroup.ModelCandidatePage{})
	if !errors.Is(err, routinggroup.ErrCandidatesDenied) || !reflect.DeepEqual(denied, routinggroup.ModelCandidatePageResult{}) {
		t.Fatal("foreign private group exposed candidate facts", err)
	}
}

func TestMySQLRoutingCandidatesContextAndPollutedCaller(t *testing.T) {
	f := newRoutingCandidateFixture(t, 0)
	f.channel(t, 10, "alpha")
	tx := f.db.Begin(&sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	queries, remove := candidateCapture(t, f.db, tx.Statement.ConnPool)
	polluted := tx.Table("tasks").Where("org_id = ?", 99999).Select("model").Order("no_such_column").Limit(0)
	out, err := f.resolver.ResolveModelCandidatesTx(context.Background(), polluted, f.ref, "alpha", f.at)
	remove()
	if err != nil || !reflect.DeepEqual(out.Candidate.ChannelIDs, []uint64{10}) || len(queries()) != 7 {
		t.Fatal("caller query clauses contaminated new domain reads", err)
	}
	if err := tx.Rollback().Error; err != nil {
		t.Fatal(err)
	}
	var count atomic.Int32
	_ = f.db.Callback().Query().Before("gorm:query").Register("candidate_cancel_count", func(*gorm.DB) { count.Add(1) })
	for _, invalid := range []*gorm.DB{f.db, f.db.Session(&gorm.Session{DryRun: true}), nil} {
		zero, e := f.resolver.ListModelCandidatesTx(context.Background(), invalid, f.ref, routinggroup.ModelCandidatePage{}, f.at)
		if !errors.Is(e, routinggroup.ErrCandidatesInvalid) || !reflect.DeepEqual(zero, routinggroup.ModelCandidatePageResult{}) {
			t.Fatal("nontransaction/dry-run caller was accepted", e)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	tx = f.db.Begin(&sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	zero, e := f.resolver.ListModelCandidatesTx(ctx, tx, f.ref, routinggroup.ModelCandidatePage{}, f.at)
	_ = tx.Rollback().Error
	_ = f.db.Callback().Query().Remove("candidate_cancel_count")
	if !errors.Is(e, context.Canceled) || count.Load() != 0 || !reflect.DeepEqual(zero, routinggroup.ModelCandidatePageResult{}) {
		t.Fatal("cancellation performed SQL or lost safe classification", e, count.Load())
	}
	var delays atomic.Int32
	_ = f.db.Callback().Query().Before("gorm:query").Register("candidate_total_deadline", func(q *gorm.DB) {
		delays.Add(1)
		select {
		case <-time.After(160 * time.Millisecond):
		case <-q.Statement.Context.Done():
			q.AddError(q.Statement.Context.Err())
		}
	})
	started := time.Now()
	zero, e = f.list(context.Background(), routinggroup.ModelCandidatePage{})
	elapsed := time.Since(started)
	_ = f.db.Callback().Query().Remove("candidate_total_deadline")
	if !errors.Is(e, context.DeadlineExceeded) || !errors.Is(e, routinggroup.ErrCandidatesUnavailable) || !reflect.DeepEqual(zero, routinggroup.ModelCandidatePageResult{}) || delays.Load() < 5 || elapsed > 2*time.Second {
		t.Fatal("SELECTs did not share one bounded deadline", e, elapsed, delays.Load())
	}
	if _, err := f.resolve(context.Background(), "alpha"); err != nil {
		t.Fatal("canceled transaction/connection was not released", err)
	}
}

func TestMySQLRoutingCandidatesConcurrentResolversAreIndependent(t *testing.T) {
	f := newRoutingCandidateFixture(t, 0)
	f.channel(t, 10, "alpha")
	other := &routinggroup.CandidateResolver{Subjects: routingCandidateSubjectBridge{}, Channels: routingPublishedChannelBridge{}}
	before := routingAdminFingerprints(t, f.db, "api_keys", "routing_policy_events", "ledger_accounts", "ledger_journal", "ledger_holds", "tasks")
	var wg sync.WaitGroup
	failures := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			resolver := f.resolver
			if i%2 == 1 {
				resolver = other
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			err := f.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				out, e := resolver.ResolveModelCandidatesTx(ctx, tx, f.ref, "alpha", f.at)
				if e != nil {
					return e
				}
				if !reflect.DeepEqual(out.Candidate.ChannelIDs, []uint64{10}) || out.Observation.Assurance != "group_membership_only" {
					return errors.New("independent candidate mismatch")
				}
				return nil
			}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
			if err != nil {
				failures <- err
			}
		}(i)
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Error("concurrent real resolver failed", err)
	}
	if !reflect.DeepEqual(before, routingAdminFingerprints(t, f.db, "api_keys", "routing_policy_events", "ledger_accounts", "ledger_journal", "ledger_holds", "tasks")) {
		t.Fatal("concurrent candidates wrote shared state")
	}
}
