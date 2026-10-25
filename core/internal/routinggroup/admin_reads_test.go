package routinggroup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type adminReadDependencies struct {
	t        *testing.T
	orgs     map[uint64]bool
	channels map[uint64]AdminChannel
	fail     error
	batches  [][]uint64
}

func (f *adminReadDependencies) checkTx(ctx context.Context, tx *gorm.DB) {
	f.t.Helper()
	if _, ok := tx.Statement.ConnPool.(gorm.TxCommitter); !ok {
		f.t.Fatal("reader did not receive the original transaction")
	}
	if ctx != tx.Statement.Context {
		f.t.Fatal("reader lost shared transaction context")
	}
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > time.Second {
		f.t.Fatal("reader did not receive bounded shared deadline")
	}
}
func (f *adminReadDependencies) ReadOrganizations(ctx context.Context, tx *gorm.DB, ids []uint64) ([]Organization, error) {
	f.checkTx(ctx, tx)
	if f.fail != nil {
		return nil, f.fail
	}
	rows := []Organization{}
	for _, id := range ids {
		if active, ok := f.orgs[id]; ok {
			rows = append(rows, Organization{ID: id, Active: active})
		}
	}
	return rows, nil
}
func (f *adminReadDependencies) ReadChannels(ctx context.Context, tx *gorm.DB, ids []uint64) ([]AdminChannel, error) {
	f.checkTx(ctx, tx)
	if len(ids) > 100 {
		f.t.Fatal("unbounded channel summary batch")
	}
	for i, id := range ids {
		if id == 0 || i > 0 && ids[i-1] >= id {
			f.t.Fatal("channel IDs not positive ascending distinct")
		}
	}
	f.batches = append(f.batches, append([]uint64(nil), ids...))
	if f.fail != nil {
		return nil, f.fail
	}
	rows := []AdminChannel{}
	for _, id := range ids {
		if row, ok := f.channels[id]; ok {
			rows = append(rows, row)
		}
	}
	return rows, nil
}
func adminReadFixture(t *testing.T) (*Service, *AdminReadService, *adminReadDependencies) {
	t.Helper()
	writes, deps := serviceFixture(t)
	reads := &adminReadDependencies{t: t, orgs: deps.orgs, channels: map[uint64]AdminChannel{}}
	for id, owner := range deps.channels {
		reads.channels[id] = AdminChannel{ID: id, OwnerOrgID: owner, Name: fmt.Sprintf("channel-%d", id), Provider: "synthetic", Status: 1}
	}
	return writes, &AdminReadService{DB: writes.DB, Organizations: reads, Channels: reads}, reads
}
func adminReadFingerprint(t *testing.T, db *gorm.DB) string {
	t.Helper()
	var groups []Group
	var bindings []Binding
	var grants []Grant
	var events []Event
	for _, entry := range []struct {
		rows  any
		order string
	}{{&groups, "id"}, {&bindings, "group_id,channel_id"}, {&grants, "id"}, {&events, "id"}} {
		if err := db.Order(entry.order).Find(entry.rows).Error; err != nil {
			t.Fatal(err)
		}
	}
	raw, err := json.Marshal([]any{groups, bindings, grants, events})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
func stringPointer(v string) *string { return &v }

func TestAdminReadGroupFilterSeekAndStrictBounds(t *testing.T) {
	writes, reads, _ := adminReadFixture(t)
	ctx := context.Background()
	var want []uint64
	for i := 0; i < 25; i++ {
		g, err := writes.Create(ctx, CreateInput{Slug: fmt.Sprintf("platform-%d", i), Name: "synthetic", Audit: audit})
		if err != nil {
			t.Fatal(err)
		}
		want = append(want, g.ID)
	}
	private := enabledGroup(t, writes, 1, "private")
	zero := uint64(0)
	first, err := reads.ListGroups(ctx, GroupFilter{OwnerOrgID: &zero, Status: Draft}, Page{})
	if err != nil || len(first.Items) != 20 || !first.HasMore || first.NextCursor != want[19] {
		t.Fatalf("first seek page incorrect: %+v %v", first, err)
	}
	second, err := reads.ListGroups(ctx, GroupFilter{OwnerOrgID: &zero, Status: Draft}, Page{Cursor: first.NextCursor})
	if err != nil || len(second.Items) != 5 || second.HasMore || second.NextCursor != 0 {
		t.Fatalf("second seek page incorrect: %+v %v", second, err)
	}
	for i, g := range append(first.Items, second.Items...) {
		if g.ID != want[i] || g.OwnerOrgID != 0 {
			t.Fatal("seek duplicated, omitted or broadened owner")
		}
	}
	one := uint64(1)
	owned, err := reads.ListGroups(ctx, GroupFilter{OwnerOrgID: &one, Status: Enabled}, Page{Limit: 100})
	if err != nil || len(owned.Items) != 1 || owned.Items[0].ID != private.ID {
		t.Fatal("private owner filter", err)
	}
	all, err := reads.ListGroups(ctx, GroupFilter{}, Page{Limit: 100})
	if err != nil || len(all.Items) != 26 {
		t.Fatal("explicit admin global list", err)
	}
	for _, tc := range []struct {
		filter GroupFilter
		page   Page
	}{{GroupFilter{}, Page{Limit: -1}}, {GroupFilter{}, Page{Limit: 101}}, {GroupFilter{Status: "enabled "}, Page{}}, {GroupFilter{Status: "ENABLED"}, Page{}}} {
		if _, err := reads.ListGroups(ctx, tc.filter, tc.page); !errors.Is(err, ErrInvalid) {
			t.Fatal("invalid list input was accepted", err)
		}
	}
	if _, err := reads.GetGroup(ctx, 0); !errors.Is(err, ErrInvalid) {
		t.Fatal("zero group accepted")
	}
	if _, err := reads.GetGroup(ctx, 999999); !errors.Is(err, ErrNotFound) {
		t.Fatal("missing group not distinguished", err)
	}
}

func TestAdminReadBindingsAreBoundedSafeAndKeepDanglingRows(t *testing.T) {
	writes, reads, deps := adminReadFixture(t)
	g := enabledGroup(t, writes, 0, "group")
	rows := make([]Binding, 101)
	for i := range rows {
		id := uint64(i + 1)
		rows[i] = Binding{GroupID: g.ID, ChannelID: id}
		deps.channels[id] = AdminChannel{ID: id, Name: "synthetic", Provider: "provider", Status: 1}
	}
	if err := writes.DB.CreateInBatches(rows, 100).Error; err != nil {
		t.Fatal(err)
	}
	disabled := deps.channels[2]
	disabled.Status = 0
	deps.channels[2] = disabled
	delete(deps.channels, 3)
	foreign := deps.channels[4]
	foreign.OwnerOrgID = 7
	deps.channels[4] = foreign
	page, err := reads.ListBindings(context.Background(), g.ID, Page{Limit: 100})
	if err != nil || len(page.Items) != 100 || !page.HasMore || page.NextCursor != 100 {
		t.Fatal("bounded binding page", err)
	}
	if len(deps.batches) != 1 || len(deps.batches[0]) != 100 {
		t.Fatal("channel summary used N+1 or included probe row")
	}
	if page.Items[1].ChannelEnabled || !page.Items[1].ChannelExists || page.Items[2].Channel != nil || page.Items[2].ChannelExists || page.Items[3].OwnershipValid {
		t.Fatal("dangling, disabled or foreign channel appeared valid")
	}
	last, err := reads.ListBindings(context.Background(), g.ID, Page{Cursor: 100, Limit: 100})
	if err != nil || len(last.Items) != 1 || last.HasMore || last.Items[0].Binding.ChannelID != 101 {
		t.Fatal("binding continuation", err)
	}
	deps.fail = errors.New("SELECT private credential field synthetic-secret")
	if out, err := reads.ListBindings(context.Background(), g.ID, Page{}); !errors.Is(err, ErrUnavailable) || out.Items != nil || strings.Contains(err.Error(), "synthetic-secret") {
		t.Fatal("dependency error leaked or returned partial page", err)
	}
}

func TestAdminPreviewSamplesHaveHonestLowerBoundsAndNoWrites(t *testing.T) {
	writes, reads, _ := adminReadFixture(t)
	g := enabledGroup(t, writes, 0, "large")
	var bindings []Binding
	var grants []Grant
	for i := 1; i <= 1002; i++ {
		bindings = append(bindings, Binding{GroupID: g.ID, ChannelID: uint64(i)})
		grant := Grant{GroupID: g.ID, OrgID: uint64(i), Status: Revoked, GrantVersion: 1}
		if i%2 == 0 {
			grant.Status = Enabled
		}
		if i%10 == 0 {
			one := uint8(1)
			grant.DefaultSlot = &one
		}
		grants = append(grants, grant)
	}
	if err := writes.DB.CreateInBatches(bindings, 100).Error; err != nil {
		t.Fatal(err)
	}
	if err := writes.DB.CreateInBatches(grants, 100).Error; err != nil {
		t.Fatal(err)
	}
	before := adminReadFingerprint(t, writes.DB)
	out, err := reads.Preview(context.Background(), g.ID, PreviewInput{ExpectedVersion: g.PolicyVersion, Change: PreviewChange{Kind: "update", Name: stringPointer("renamed")}})
	if err != nil {
		t.Fatal(err)
	}
	if out.Counts.Bindings.ObservedCount != 1000 || out.Counts.Grants.ObservedCount != 1000 || out.Counts.EnabledGrants.ObservedCount != 500 || out.Counts.DefaultPointers.ObservedCount != 100 {
		t.Fatalf("wrong independent first-1000 distribution: %+v", out.Counts)
	}
	for _, count := range []ObservedCount{out.Counts.Bindings, out.Counts.Grants, out.Counts.EnabledGrants, out.Counts.DefaultPointers} {
		if count.Exact || !count.LowerBound || count.Limit != 1000 {
			t.Fatal("truncated sample claimed exactness")
		}
	}
	if !out.Changed || out.ProposedGroup.Name != "renamed" || out.ProposedGroup.PolicyVersion != g.PolicyVersion || out.ObservedGroupVersion != g.PolicyVersion || *out.Change.Status != g.Status {
		t.Fatal("preview omitted normalized change or fabricated committed version")
	}
	if after := adminReadFingerprint(t, writes.DB); after != before {
		t.Fatal("preview wrote configuration, versions or events")
	}
	// Exactly 1000 is complete; the grant sample can independently remain
	// truncated. The probe row must not make every relation share one flag.
	if err := writes.DB.Where("group_id = ? AND channel_id > ?", g.ID, 1000).Delete(&Binding{}).Error; err != nil {
		t.Fatal(err)
	}
	boundary, err := reads.Preview(context.Background(), g.ID, PreviewInput{ExpectedVersion: g.PolicyVersion, Change: PreviewChange{Kind: "update", Name: &g.Name}})
	if err != nil || !boundary.Counts.Bindings.Exact || boundary.Counts.Bindings.LowerBound || boundary.Counts.Bindings.ObservedCount != 1000 || boundary.Counts.Grants.Exact {
		t.Fatal("exact-limit boundary or independent truncation flag", err)
	}
	small := enabledGroup(t, writes, 0, "small")
	value, err := reads.Preview(context.Background(), small.ID, PreviewInput{ExpectedVersion: small.PolicyVersion, Change: PreviewChange{Kind: "update", Name: &small.Name}})
	if err != nil || value.Changed || !value.Counts.Bindings.Exact || value.Counts.Bindings.LowerBound || value.Counts.Bindings.ObservedCount != 0 || !value.Counts.Grants.Exact {
		t.Fatal("empty exact preview", err)
	}
}

func TestAdminReadsClearPollutedCallerQueriesAndKeepBoundedSelects(t *testing.T) {
	writes, reads, _ := adminReadFixture(t)
	g := enabledGroup(t, writes, 0, "group")
	other := enabledGroup(t, writes, 0, "other")
	if err := writes.DB.Create(&Binding{GroupID: g.ID, ChannelID: 10}).Error; err != nil {
		t.Fatal(err)
	}
	reads.DB = writes.DB.Where("id = ?", other.ID).Select("id").Order("id DESC").Limit(1)
	group, err := reads.GetGroup(context.Background(), g.ID)
	if err != nil || group.ID != g.ID || group.Name != g.Name {
		t.Fatal("caller predicate or projection leaked", err)
	}
	var selects []string
	name := "test:routing-admin-query-shape"
	if err := writes.DB.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) { selects = append(selects, tx.Statement.SQL.String()) }); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writes.DB.Callback().Query().Remove(name) })
	result, err := reads.Preview(context.Background(), g.ID, PreviewInput{ExpectedVersion: g.PolicyVersion, Change: PreviewChange{Kind: "unbind", ChannelID: 10}})
	if err != nil || !result.Changed || result.Counts.Bindings.ObservedCount != 1 {
		t.Fatal("multi-query shape leaked predicates", err)
	}
	// Fake host descriptors use no SQL; production bridge adds one finite
	// channel-summary SELECT. These are the four domain-owned SELECTs.
	if len(selects) != 4 {
		t.Fatal("unbounded domain SELECT count", len(selects))
	}
	for _, statement := range selects {
		if !strings.HasPrefix(statement, "SELECT ") || strings.Contains(strings.ToUpper(statement), "COUNT(") {
			t.Fatal("preview used a write or unbounded aggregate")
		}
	}
}

func TestAdminPreviewMirrorsOwnershipCASAndInactiveCleanup(t *testing.T) {
	writes, reads, deps := adminReadFixture(t)
	ctx := context.Background()
	g := enabledGroup(t, writes, 1, "private")
	if _, _, err := writes.SetBinding(ctx, BindingInput{OwnerOrgID: 1, GroupID: g.ID, ChannelID: 11, ExpectedVersion: g.PolicyVersion, Bound: true, Audit: audit}); err != nil {
		t.Fatal(err)
	}
	var err error
	g, err = reads.GetGroup(ctx, g.ID)
	if err != nil {
		t.Fatal(err)
	}
	deps.orgs[1] = false
	before := adminReadFingerprint(t, writes.DB)
	if _, err := reads.GetGroup(ctx, g.ID); err != nil {
		t.Fatal("inactive owner prevented read", err)
	}
	for _, change := range []PreviewChange{{Kind: "update", Status: stringPointer(Enabled)}, {Kind: "bind", ChannelID: 11}} {
		if _, err := reads.Preview(ctx, g.ID, PreviewInput{ExpectedVersion: g.PolicyVersion, Change: change}); !errors.Is(err, ErrInactive) {
			t.Fatal("inactive owner allowed enabling", err)
		}
	}
	for _, change := range []PreviewChange{{Kind: "update", Status: stringPointer(Disabled)}, {Kind: "unbind", ChannelID: 11}} {
		p, err := reads.Preview(ctx, g.ID, PreviewInput{ExpectedVersion: g.PolicyVersion, Change: change})
		if err != nil || !p.Changed {
			t.Fatal("inactive cleanup was blocked", err)
		}
	}
	deps.orgs[1] = true
	if _, err := reads.Preview(ctx, g.ID, PreviewInput{ExpectedVersion: g.PolicyVersion, Change: PreviewChange{Kind: "bind", ChannelID: 10}}); !errors.Is(err, ErrOwnership) {
		t.Fatal("platform channel entered private group", err)
	}
	delete(deps.channels, 11)
	p, err := reads.Preview(ctx, g.ID, PreviewInput{ExpectedVersion: g.PolicyVersion, Change: PreviewChange{Kind: "unbind", ChannelID: 11}})
	if err != nil || p.Channel != nil || p.WasBound == nil || !*p.WasBound || p.WillBeBound == nil || *p.WillBeBound {
		t.Fatal("dangling cleanup preview", err)
	}
	if _, err := reads.Preview(ctx, g.ID, PreviewInput{ExpectedVersion: g.PolicyVersion, Change: PreviewChange{Kind: "bind", ChannelID: 11}}); !errors.Is(err, ErrNotFound) {
		t.Fatal("missing channel bind preview allowed", err)
	}
	if _, err := reads.Preview(ctx, g.ID, PreviewInput{ExpectedVersion: g.PolicyVersion - 1, Change: PreviewChange{Kind: "update", Name: &g.Name}}); !errors.Is(err, ErrConflict) {
		t.Fatal("stale no-op preview bypassed CAS", err)
	}
	if adminReadFingerprint(t, writes.DB) != before {
		t.Fatal("denied/allowed previews changed tables")
	}
	for _, input := range []PreviewInput{{Change: PreviewChange{Kind: "update", Name: &g.Name}}, {ExpectedVersion: g.PolicyVersion, Change: PreviewChange{Kind: "update"}}, {ExpectedVersion: g.PolicyVersion, Change: PreviewChange{Kind: "bind", ChannelID: 11, Name: &g.Name}}, {ExpectedVersion: g.PolicyVersion, Change: PreviewChange{Kind: "update", ChannelID: 11, Name: &g.Name}}, {ExpectedVersion: g.PolicyVersion, Change: PreviewChange{Kind: "delete"}}} {
		if _, err := reads.Preview(ctx, g.ID, input); !errors.Is(err, ErrInvalid) {
			t.Fatal("invalid change accepted", err)
		}
	}
}

func TestAdminReadRejectsNestedTransactionsAndReturnsNoCancelledPartial(t *testing.T) {
	writes, reads, _ := adminReadFixture(t)
	g := enabledGroup(t, writes, 0, "group")
	if err := writes.DB.Transaction(func(tx *gorm.DB) error {
		nested := *reads
		nested.DB = tx
		if _, err := nested.GetGroup(context.Background(), g.ID); !errors.Is(err, ErrUnavailable) {
			t.Fatal("nested caller transaction accepted", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if out, err := reads.GetGroup(ctx, g.ID); !errors.Is(err, context.Canceled) || !errors.Is(err, ErrUnavailable) || out.ID != 0 {
		t.Fatal("cancelled read returned successful/partial object", err)
	}
	name := "test:admin-read-cancel"
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	if err := writes.DB.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "routing_groups" {
			cancel()
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writes.DB.Callback().Query().Remove(name) })
	if out, err := reads.ListBindings(ctx, g.ID, Page{}); !errors.Is(err, context.Canceled) || out.Group.ID != 0 || out.Items != nil {
		t.Fatal("interrupted multi-query returned mixed partial success", err)
	}
}

func TestAdminReadAndWriteSuppressInheritedWarnSQL(t *testing.T) {
	writes, reads, _ := adminReadFixture(t)
	var output bytes.Buffer
	warn := logger.New(log.New(&output, "", 0), logger.Config{SlowThreshold: -1, LogLevel: logger.Warn, Colorful: false})
	writes.DB = writes.DB.Session(&gorm.Session{Logger: warn})
	reads.DB = writes.DB
	g, err := writes.Create(context.Background(), CreateInput{Slug: "warn-test", Name: "sensitive-synthetic-name", Description: "sensitive-synthetic-description", Audit: audit})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reads.GetGroup(context.Background(), g.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := reads.Preview(context.Background(), g.ID, PreviewInput{ExpectedVersion: g.PolicyVersion, Change: PreviewChange{Kind: "update", Name: stringPointer("changed-sensitive-name")}}); err != nil {
		t.Fatal(err)
	}
	if output.Len() != 0 {
		t.Fatal("new management read/write inherited SQL logging")
	}
	var direct Group
	if err := writes.DB.Where("name = ?", "sensitive-synthetic-name").Take(&direct).Error; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "sensitive-synthetic-name") {
		t.Fatal("positive control did not prove inherited Warn logger remained active")
	}
}

type adminMySQLDialect struct{ gorm.Dialector }

func (adminMySQLDialect) Name() string { return "mysql" }

func TestAdminSelectBudgetAndHintFollowRemainingDeadline(t *testing.T) {
	writes, _, _ := adminReadFixture(t)
	for _, budget := range []time.Duration{time.Second, 80 * time.Millisecond} {
		ctx, cancel := context.WithTimeout(context.Background(), budget)
		tx := writes.DB.Session(&gorm.Session{DryRun: true}).WithContext(ctx)
		config := *tx.Config
		config.Dialector = adminMySQLDialect{tx.Dialector}
		copy := *tx
		copy.Config = &config
		q, stop, err := adminSelect(&copy, adminGroupColumns)
		if err != nil {
			t.Fatal(err)
		}
		var rows []Group
		result := q.Model(&Group{}).Find(&rows)
		sql := result.Statement.SQL.String()
		var millis int
		start := strings.Index(sql, "MAX_EXECUTION_TIME(")
		if start < 0 {
			t.Fatal("missing actual MySQL SELECT hint")
		}
		if _, err := fmt.Sscanf(sql[start:], "MAX_EXECUTION_TIME(%d)", &millis); err != nil {
			t.Fatal(err)
		}
		if millis < 1 || millis > 200 || time.Duration(millis)*time.Millisecond > budget {
			t.Fatal("hint exceeded per-query/shared deadline", millis)
		}
		deadline, ok := q.Statement.Context.Deadline()
		if !ok || time.Until(deadline) > adminSelectWindow {
			t.Fatal("SELECT had no bounded context")
		}
		stop()
		cancel()
	}
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if _, stop, err := adminSelect(writes.DB.WithContext(ctx), "id"); !errors.Is(err, context.DeadlineExceeded) {
		stop()
		t.Fatal("expired SELECT became unlimited")
	}
}
