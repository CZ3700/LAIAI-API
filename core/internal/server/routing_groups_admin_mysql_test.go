package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/migrate"
	"github.com/laiai/laiai-next/core/internal/relay"
	"github.com/laiai/laiai-next/core/internal/routinggroup"
	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/shopspring/decimal"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// This suite never accepts an application database. Every test owns one random
// schema and cleanup drops only the exact schema successfully created here.
func routingAdminMySQLDatabase(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("ARK_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("ARK_TEST_MYSQL_DSN=ark_test required for real routing administration acceptance")
	}
	config, err := mysqlDriver.ParseDSN(dsn)
	if err != nil || config.DBName != "ark_test" {
		t.Fatal("routing administration tests require the isolated ark_test database")
	}
	config.ParseTime = true
	config.Timeout, config.ReadTimeout, config.WriteTimeout = 5*time.Second, 5*time.Second, 5*time.Second
	config.Logger = log.New(io.Discard, "", 0)
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal("cannot allocate private test schema")
	}
	name := "ark_test_rgadmin_" + hex.EncodeToString(nonce[:])
	if !regexp.MustCompile(`^ark_test_rgadmin_[0-9a-f]{24}$`).MatchString(name) {
		t.Fatal("invalid private test schema")
	}
	// DDL keeps its existing 10s context budget. Its socket must not cut that
	// budget short; the private business pool below retains all 5s timeouts.
	adminConfig := config.Clone()
	adminConfig.ReadTimeout = 15 * time.Second
	adminConnector, err := mysqlDriver.NewConnector(adminConfig)
	if err != nil {
		t.Fatal("cannot open test schema administration connection")
	}
	// Unlike a FormatDSN round trip, the connector retains the Discard logger.
	admin := sql.OpenDB(adminConnector)
	var pool *sql.DB
	created := false
	t.Cleanup(func() {
		if pool != nil {
			before := pool.Stats()
			t.Logf("private schema pool before close: in_use=%d open=%d idle=%d", before.InUse, before.OpenConnections, before.Idle)
			started := time.Now()
			closeErr := pool.Close()
			after := pool.Stats()
			t.Logf("private schema pool after close: in_use=%d open=%d idle=%d elapsed_ms=%d result=%s",
				after.InUse, after.OpenConnections, after.Idle,
				time.Since(started).Milliseconds(), routingAdminCleanupErrorClass(closeErr))
			if closeErr != nil {
				t.Errorf("cannot close private routing test pool: %s", routingAdminCleanupErrorClass(closeErr))
			}
		}
		if created {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			started := time.Now()
			_, dropErr := admin.ExecContext(ctx, "DROP DATABASE `"+name+"`")
			t.Logf("private schema drop: elapsed_ms=%d result=%s", time.Since(started).Milliseconds(), routingAdminCleanupErrorClass(dropErr))
			cancel()
			if dropErr != nil {
				t.Errorf("cannot remove this invocation's private routing test schema: %s", routingAdminCleanupErrorClass(dropErr))
			}
		}
		_ = admin.Close()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE `"+name+"` CHARACTER SET utf8mb4 COLLATE utf8mb4_bin"); err != nil {
		t.Fatal("cannot create private routing test schema; dedicated test user needs CREATE/DROP DATABASE")
	}
	created = true
	config.DBName = name
	db, err := gorm.Open(mysql.Open(config.FormatDSN()), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal("cannot connect to private routing test schema")
	}
	pool, err = db.DB()
	if err != nil {
		t.Fatal("cannot obtain private routing test pool")
	}
	pool.SetMaxOpenConns(12)
	pool.SetMaxIdleConns(12)
	if _, err := migrate.Up(db, os.DirFS("../../../migrations")); err != nil {
		t.Fatal("tracked migrations failed in private routing schema")
	}
	return db
}

// Diagnostic output must never contain a driver error, DSN, address or SQL.
func routingAdminCleanupErrorClass(err error) string {
	if err == nil {
		return "ok"
	}
	if errors.Is(err, context.Canceled) {
		return "context_canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "context_deadline"
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "network_timeout"
	}
	return "database_error"
}

func TestRoutingAdminMySQLCleanupErrorClass(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"success", nil, "ok"},
		{"cancel", fmt.Errorf("synthetic private context: %w", context.Canceled), "context_canceled"},
		{"deadline", fmt.Errorf("synthetic private context: %w", context.DeadlineExceeded), "context_deadline"},
		{"socket", &net.OpError{Op: "read", Err: &net.DNSError{Err: "synthetic private address", IsTimeout: true}}, "network_timeout"},
		{"driver", &mysqlDriver.MySQLError{Number: 1205, Message: "synthetic private SQL"}, "database_error"},
		{"other", errors.New("synthetic private DSN"), "database_error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := routingAdminCleanupErrorClass(tc.err); got != tc.want {
				t.Fatalf("unsafe or incorrect cleanup diagnostic classification: got=%q want=%q", got, tc.want)
			}
		})
	}
}

type routingAdminDenyTransport struct{ calls atomic.Uint64 }

func (r *routingAdminDenyTransport) RoundTrip(*http.Request) (*http.Response, error) {
	r.calls.Add(1)
	return nil, errors.New("unexpected upstream call in routing configuration acceptance")
}

func newRoutingAdminMySQLFixture(t *testing.T) *routingAdminHTTPFixture {
	t.Helper()
	db := routingAdminMySQLDatabase(t)
	for _, org := range []int{1, 2, 3} {
		routingAdminSQL(t, db, "INSERT INTO organizations(id,slug,name) VALUES(?,?,?)", org, fmt.Sprintf("org-%d", org), "Fixture Org")
	}
	tokens := make(map[string]string)
	for i, role := range []string{"superadmin", "superadmin", "user", "admin"} {
		uid := uint64(i + 1)
		email := fmt.Sprintf("routing-%d@example.test", uid)
		routingAdminSQL(t, db, "INSERT INTO users(id,email,email_canonical,role) VALUES(?,?,?,?)", uid, email, email, role)
		routingAdminSQL(t, db, "INSERT INTO org_members(org_id,user_id,role) VALUES(1,?,'owner')", uid)
		token, _, err := identity.IssueSessionToken(testSecret, uid, 1, time.Hour, time.Now())
		if err != nil {
			t.Fatal("cannot issue synthetic test session")
		}
		tokens[[]string{"su", "su2", "user", "admin"}[i]] = token
	}
	for _, ch := range []struct{ id, owner int }{{1, 0}, {2, 1}, {3, 2}} {
		routingAdminSQL(t, db, "INSERT INTO channels(id,name,provider,base_url,credential_enc,models,owner_org_id) VALUES(?,?,'test','https://never-send.invalid','synthetic-encrypted-placeholder','fixture-model',?)", ch.id, fmt.Sprintf("Channel %d", ch.id), ch.owner)
	}
	deny := &routingAdminDenyTransport{}
	client := &http.Client{Transport: deny}
	tasks := &task.Service{DB: db, Client: client}
	engine, host := NewWithServer(db, Options{AuthSecret: testSecret, AdminToken: []byte(testAdminToken), Keyring: testKeyring(t), TaskService: tasks, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	// Relay startup rehydrates metadata in a background reader. Fault-injection
	// callbacks must not mutate the registry used by that unrelated reader.
	// Keep the same physical pool/schema, with an independent GORM registry for
	// actual management routes and their test probes.
	pool, err := db.DB()
	if err != nil {
		t.Fatal("cannot obtain fixture management pool")
	}
	management, err := gorm.Open(mysql.New(mysql.Config{Conn: pool, SkipInitializeWithVersion: true}), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal("cannot create isolated management callback registry")
	}
	host.db = management
	host.relay.Client = client
	host.notify.HTTP = client
	t.Cleanup(func() {
		if deny.calls.Load() != 0 {
			t.Error("configuration API attempted upstream HTTP")
		}
	})
	return &routingAdminHTTPFixture{db: management, engine: engine, s: host, tokens: tokens}
}

func routingAdminSQL(t *testing.T, db *gorm.DB, query string, args ...any) {
	t.Helper()
	if err := db.Exec(query, args...).Error; err != nil {
		t.Fatal("private fixture SQL failed")
	}
}

// Nonempty financial, Task, Key and pricing facts make preservation assertions
// meaningful. Only the actual ledger API creates balances/holds.
func routingAdminSeedPreservedFacts(t *testing.T, db *gorm.DB) {
	t.Helper()
	err := db.Transaction(func(tx *gorm.DB) error {
		account, err := ledger.EnsureAccount(tx, 1, ledger.AccountUserCredit)
		if err != nil {
			return err
		}
		if _, _, err := ledger.Post(tx, ledger.PostInput{IdempotencyKey: "routing-fixture-credit", AccountID: account.ID, Kind: ledger.KindCredit, Delta: decimal.RequireFromString("123.12345678")}); err != nil {
			return err
		}
		hold, _, err := ledger.CreateHold(tx, ledger.HoldInput{IdempotencyKey: "routing-fixture-hold", AccountID: account.ID, Amount: decimal.RequireFromString("1.23456789"), RefType: "task", RefID: "routing-preserved-task"})
		if err != nil {
			return err
		}
		if err := tx.Exec("INSERT INTO api_keys(id,org_id,name,prefix,key_hash) VALUES(1,1,'preserved','test-prefix',?)", strings.Repeat("a", 64)).Error; err != nil {
			return err
		}
		if err := tx.Exec("INSERT INTO tasks(task_id,org_id,api_key_id,model,channel_id,provider,status,hold_id,price_out_usd_per_1m) VALUES('routing-preserved-task',1,1,'fixture-model',1,'fixture','queued',?,'0.123456')", hold.ID).Error; err != nil {
			return err
		}
		return tx.Exec("INSERT INTO model_prices(model,input_usd_per_1m,output_usd_per_1m) VALUES('routing-preserved-model','1.234567','2.345678')").Error
	})
	if err != nil {
		t.Fatal("could not seed independent preservation facts")
	}
}

type routingAdminFingerprint struct {
	Rows int
	Hash string
}

func routingAdminFingerprints(t *testing.T, db *gorm.DB, tables ...string) map[string]routingAdminFingerprint {
	t.Helper()
	out := make(map[string]routingAdminFingerprint, len(tables))
	for _, table := range tables {
		if !regexp.MustCompile(`^[a-z_]+$`).MatchString(table) {
			t.Fatal("invalid fixed fixture table")
		}
		rows, err := db.Table(table).Rows()
		if err != nil {
			t.Fatal("cannot read preservation snapshot")
		}
		columns, err := rows.Columns()
		if err != nil {
			_ = rows.Close()
			t.Fatal("cannot read preservation columns")
		}
		serialized := []string{}
		for rows.Next() {
			values, refs := make([]sql.RawBytes, len(columns)), make([]any, len(columns))
			for i := range refs {
				refs[i] = &values[i]
			}
			if err := rows.Scan(refs...); err != nil {
				_ = rows.Close()
				t.Fatal("cannot scan preservation row")
			}
			row := make([]any, len(values))
			for i, value := range values {
				if value != nil {
					row[i] = string(value)
				}
			}
			encoded, _ := json.Marshal(row)
			serialized = append(serialized, string(encoded))
		}
		rowsErr := rows.Err()
		_ = rows.Close()
		if rowsErr != nil {
			t.Fatal("preservation snapshot did not finish")
		}
		sort.Strings(serialized)
		digest := sha256.Sum256([]byte(strings.Join(serialized, "\n")))
		out[table] = routingAdminFingerprint{len(serialized), hex.EncodeToString(digest[:])}
	}
	return out
}

func TestMySQLRoutingAdminLifecycleAndPreservation(t *testing.T) {
	f := newRoutingAdminMySQLFixture(t)
	routingAdminSeedPreservedFacts(t, f.db)
	tables := []string{"ledger_accounts", "ledger_journal", "ledger_holds", "ledger_operations", "ledger_movements", "tasks", "api_keys", "model_prices", "channels", "channel_credentials", "org_group_grants", "video_billing_snapshots", "video_pricing_templates"}
	before := routingAdminFingerprints(t, f.db, tables...)
	for _, table := range []string{"ledger_accounts", "ledger_journal", "ledger_holds", "tasks", "api_keys", "model_prices", "channels"} {
		if before[table].Rows == 0 {
			t.Fatal("empty preservation positive control", table)
		}
	}
	for _, token := range []string{f.tokens["user"], f.tokens["admin"]} {
		routingAdminTestJSON(t, f.request("POST", "/api/admin/routing-groups", token, `{"owner_org_id":0,"slug":"denied","name":"Denied"}`), 403)
	}
	routingAdminTestJSON(t, routingAdminTestRequest(f.engine, context.Background(), "POST", "/api/admin/routing-groups", "", testAdminToken, `{"owner_org_id":0,"slug":"denied","name":"Denied"}`), 403)
	platform := f.create(t, 0, "platform")
	path := fmt.Sprintf("/api/admin/routing-groups/%d", platform)
	duplicate := routingAdminTestJSON(t, f.request("POST", "/api/admin/routing-groups", f.tokens["su"], `{"owner_org_id":0,"slug":"platform","name":"Duplicate"}`), 409)
	if duplicate["code"] != "routing_group_exists" {
		t.Fatal("duplicate slug was not classified as the narrow resource conflict")
	}
	previewBefore := routingAdminFingerprints(t, f.db, "routing_groups", "routing_policy_events", "channel_group_bindings")
	preview := routingAdminTestJSON(t, f.request("POST", path+"/impact-preview", f.tokens["su"], `{"expected_version":1,"change":{"kind":"update","status":"enabled"}}`), 200)
	if preview["preview_kind"] != "structure_only" || preview["effective_model_impact_available"] != false || !reflect.DeepEqual(previewBefore, routingAdminFingerprints(t, f.db, "routing_groups", "routing_policy_events", "channel_group_bindings")) {
		t.Fatal("preview wrote facts or advertised effective authorization")
	}
	routingAdminTestJSON(t, f.request("PATCH", path, f.tokens["su"], `{"expected_version":1,"status":"enabled"}`), 200)
	routingAdminTestJSON(t, f.request("PUT", path+"/channels/2", f.tokens["su"], `{"expected_group_version":2}`), 403)
	routingAdminTestJSON(t, f.request("POST", path+"/impact-preview", f.tokens["su"], `{"expected_version":2,"change":{"kind":"bind","channel_id":2}}`), 403)
	routingAdminTestJSON(t, f.request("PUT", path+"/channels/1", f.tokens["su"], `{"expected_group_version":2}`), 200)
	listing := routingAdminTestJSON(t, f.request("GET", path+"/channels", f.tokens["su"], ""), 200)
	items := listing["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["channel"].(map[string]any)["name"] != "Channel 1" {
		t.Fatal("real binding projection missing")
	}
	routingAdminTestJSON(t, f.request("PATCH", path, f.tokens["su"], `{"expected_version":3,"status":"disabled"}`), 200)
	routingAdminTestJSON(t, f.request("DELETE", path+"/channels/1", f.tokens["su"], `{"expected_group_version":4}`), 200)
	// Slug uniqueness is owner-scoped, not global.
	private := f.create(t, 1, "platform")
	privatePath := fmt.Sprintf("/api/admin/routing-groups/%d", private)
	routingAdminTestJSON(t, f.request("PUT", privatePath+"/channels/2", f.tokens["su"], `{"expected_group_version":1}`), 200)
	for _, channel := range []int{1, 3} {
		routingAdminTestJSON(t, f.request("PUT", fmt.Sprintf("%s/channels/%d", privatePath, channel), f.tokens["su"], `{"expected_group_version":2}`), 403)
	}
	if !reflect.DeepEqual(before, routingAdminFingerprints(t, f.db, tables...)) {
		t.Fatal("routing administration changed finance, tasks, keys, prices, channels or grants")
	}
	var events []routinggroup.Event
	if err := f.db.Find(&events).Error; err != nil || len(events) != 7 {
		t.Fatal("unexpected configuration event count")
	}
	for _, event := range events {
		if event.ActorUserID != 1 || event.RequestID == "" || event.RequestID == "client-spoofed-request" {
			t.Fatal("configuration event accepted client audit identity")
		}
	}
}

func TestMySQLRoutingAdminConcurrentCASAndEventRollback(t *testing.T) {
	f := newRoutingAdminMySQLFixture(t)
	id := f.create(t, 0, "cas")
	path := fmt.Sprintf("/api/admin/routing-groups/%d", id)
	pool, err := f.db.DB()
	if err != nil {
		t.Fatal(err)
	}
	arrived, release := make(chan struct{}, 2), make(chan struct{})
	var commits atomic.Uint64
	f.s.db = f.db.Session(&gorm.Session{NewDB: true, Context: context.Background()})
	f.s.db.Statement.ConnPool = routingAdminCommitPool{DB: pool, after: func() {
		if commits.Add(1) <= 2 {
			arrived <- struct{}{}
			<-release
		}
	}}
	results := make(chan *httptest.ResponseRecorder, 2)
	var workers sync.WaitGroup
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }); workers.Wait() })
	for _, name := range []string{"first", "second"} {
		workers.Add(1)
		go func(name string) {
			defer workers.Done()
			results <- f.request("PATCH", path, f.tokens["su"], fmt.Sprintf(`{"expected_version":1,"name":%q}`, name))
		}(name)
	}
	for i := 0; i < 2; i++ {
		select {
		case <-arrived:
		case <-time.After(2 * time.Second):
			t.Fatal("both CAS contenders did not finish the same pre-read version")
		}
	}
	releaseOnce.Do(func() { close(release) })
	status := map[int]int{}
	for i := 0; i < 2; i++ {
		select {
		case result := <-results:
			status[result.Code]++
		case <-time.After(6 * time.Second):
			t.Fatal("CAS contender failed to finish")
		}
	}
	workers.Wait()
	f.s.db = f.db
	if status[200] != 1 || status[409] != 1 || routingAdminTableCount(t, f.db, "routing_policy_events") != 2 {
		t.Fatal("concurrent CAS did not produce exactly one command/event", status)
	}
	for _, command := range []string{"patch", "bind"} {
		t.Run(command, func(t *testing.T) {
			groupID := f.create(t, 0, "rollback-"+command)
			// A real unique-key conflict rejects the event after preceding domain
			// writes. The fixture event is retained; rollback must undo the group
			// version/name/binding, not merely surface a 503.
			routingAdminSQL(t, f.db, "INSERT INTO routing_policy_events(aggregate_type,aggregate_id,aggregate_version,event_kind,group_id,actor_user_id,created_at) VALUES('group',?,2,'fixture_collision',?,1,UTC_TIMESTAMP(3))", groupID, groupID)
			before := routingAdminFingerprints(t, f.db, "routing_groups", "routing_policy_events", "channel_group_bindings")
			p := fmt.Sprintf("/api/admin/routing-groups/%d", groupID)
			if command == "patch" {
				routingAdminTestJSON(t, f.request("PATCH", p, f.tokens["su"], `{"expected_version":1,"name":"must-rollback"}`), 503)
			} else {
				routingAdminTestJSON(t, f.request("PUT", p+"/channels/1", f.tokens["su"], `{"expected_group_version":1}`), 503)
			}
			if !reflect.DeepEqual(before, routingAdminFingerprints(t, f.db, "routing_groups", "routing_policy_events", "channel_group_bindings")) {
				t.Fatal("event failure left partial configuration or event writes")
			}
		})
	}
	// Also distinguish a new-group event conflict from a duplicate group slug:
	// both use MySQL 1062, but only the latter is a 409 routing_group_exists.
	var nextID uint64
	if err := f.db.Raw("SELECT auto_increment FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='routing_groups'").Scan(&nextID).Error; err != nil || nextID == 0 {
		t.Fatal("cannot locate isolated next group identity")
	}
	routingAdminSQL(t, f.db, "INSERT INTO routing_policy_events(aggregate_type,aggregate_id,aggregate_version,event_kind,group_id,actor_user_id,created_at) VALUES('group',?,1,'fixture_collision',?,1,UTC_TIMESTAMP(3))", nextID, nextID)
	before := routingAdminFingerprints(t, f.db, "routing_groups", "routing_policy_events", "channel_group_bindings")
	routingAdminTestJSON(t, f.request("POST", "/api/admin/routing-groups", f.tokens["su"], `{"owner_org_id":0,"slug":"create-event-rollback","name":"Must roll back"}`), 503)
	if !reflect.DeepEqual(before, routingAdminFingerprints(t, f.db, "routing_groups", "routing_policy_events", "channel_group_bindings")) {
		t.Fatal("create event failure was misclassified or left a group behind")
	}
}

func TestMySQLRoutingAdminHostLocksAreCurrentAndRetained(t *testing.T) {
	f := newRoutingAdminMySQLFixture(t)
	pool, err := f.db.DB()
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"organizations", "channels"} {
		t.Run(table, func(t *testing.T) {
			tx := f.db.Begin(&sql.TxOptions{Isolation: sql.LevelRepeatableRead})
			if tx.Error != nil {
				t.Fatal("cannot begin lock fixture")
			}
			defer tx.Rollback()
			var old int
			if err := tx.Raw("SELECT status FROM " + table + " WHERE id=1").Scan(&old).Error; err != nil || old != 1 {
				t.Fatal("missing snapshot positive control")
			}
			routingAdminSQL(t, f.db, "UPDATE "+table+" SET status=0 WHERE id=1")
			if table == "organizations" {
				rows, err := identity.LockRoutingOrganizations(context.Background(), tx, []uint64{1})
				if err != nil || len(rows) != 1 || rows[0].Active {
					t.Fatal("organization lock used an old repeatable-read snapshot")
				}
			} else {
				rows, err := relay.LockRoutingChannels(context.Background(), tx, []uint64{1})
				if err != nil || len(rows) != 1 || rows[0].Status != 0 {
					t.Fatal("channel lock used an old repeatable-read snapshot")
				}
			}
			// NOWAIT proves a conflicting lock exists, without relying on a sleep
			// or scheduler timing to claim a second connection was blocked.
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			other, err := pool.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal("cannot begin independent lock contender")
			}
			var status int
			err = other.QueryRowContext(ctx, "SELECT status FROM "+table+" WHERE id=1 FOR UPDATE NOWAIT").Scan(&status)
			var denied *mysqlDriver.MySQLError
			if !errors.As(err, &denied) || denied.Number != 3572 {
				_ = other.Rollback()
				t.Fatal("bridge did not retain caller row lock on another connection")
			}
			_ = other.Rollback()
			if err := tx.Rollback().Error; err != nil {
				t.Fatal("cannot release original caller transaction")
			}
			other, err = pool.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal("cannot retry lock after release")
			}
			err = other.QueryRowContext(ctx, "SELECT status FROM "+table+" WHERE id=1 FOR UPDATE NOWAIT").Scan(&status)
			_ = other.Rollback()
			if err != nil || status != 0 {
				t.Fatal("caller rollback did not release bridge row lock")
			}
			routingAdminSQL(t, f.db, "UPDATE "+table+" SET status=1 WHERE id=1")
		})
	}
}

func TestMySQLRoutingAdminReadonlySnapshotAndFuturePATCH(t *testing.T) {
	f := newRoutingAdminMySQLFixture(t)
	id := f.create(t, 0, "snapshot")
	path := fmt.Sprintf("/api/admin/routing-groups/%d", id)
	routingAdminTestJSON(t, f.request("PUT", path+"/channels/1", f.tokens["su"], `{"expected_group_version":1}`), 200)
	var once atomic.Bool
	var readOnlyVerified bool
	var hookErr error
	if err := f.db.Callback().Query().After("gorm:query").Register("routing_mysql_snapshot", func(q *gorm.DB) {
		if q.Statement.Table != "routing_groups" || !once.CompareAndSwap(false, true) {
			return
		}
		err := q.Session(&gorm.Session{NewDB: true, Context: q.Statement.Context}).Exec("UPDATE routing_groups SET name='forbidden-read-write' WHERE id=?", id).Error
		var rejected *mysqlDriver.MySQLError
		readOnlyVerified = errors.As(err, &rejected) && rejected.Number == 1792
		// The other committed connection must not change the subsequent batch
		// channel read in the read-only transaction's existing RR snapshot.
		hookErr = f.db.Exec("UPDATE channels SET name='after-snapshot' WHERE id=1").Error
	}); err != nil {
		t.Fatal(err)
	}
	out := routingAdminTestJSON(t, f.request("GET", path+"/channels", f.tokens["su"], ""), 200)
	_ = f.db.Callback().Query().Remove("routing_mysql_snapshot")
	name := out["items"].([]any)[0].(map[string]any)["channel"].(map[string]any)["name"]
	if hookErr != nil || !once.Load() || !readOnlyVerified || name != "Channel 1" {
		t.Fatal("real binding read lacked read-only RR snapshot")
	}
	out = routingAdminTestJSON(t, f.request("GET", path+"/channels", f.tokens["su"], ""), 200)
	if out["items"].([]any)[0].(map[string]any)["channel"].(map[string]any)["name"] != "after-snapshot" {
		t.Fatal("second request did not observe committed channel change")
	}
	pool, err := f.db.DB()
	if err != nil {
		t.Fatal(err)
	}
	var injected atomic.Bool
	var injectionErr error
	f.s.db = f.db.Session(&gorm.Session{NewDB: true, Context: context.Background()})
	f.s.db.Statement.ConnPool = routingAdminCommitPool{DB: pool, after: func() {
		if injected.CompareAndSwap(false, true) {
			injectionErr = f.db.Exec("UPDATE routing_groups SET description='newer-committed-value',policy_version=3 WHERE id=?", id).Error
		}
	}}
	routingAdminTestJSON(t, f.request("PATCH", path, f.tokens["su"], `{"expected_version":3,"name":"stale-merge"}`), 409)
	var group routinggroup.Group
	if err := f.db.First(&group, id).Error; err != nil || !injected.Load() || injectionErr != nil || group.Description != "newer-committed-value" || group.Name != "Group" || group.PolicyVersion != 3 {
		t.Fatal("PATCH overwrote an unspecified field from an older pre-read")
	}
}

func TestMySQLRoutingAdminBoundedCountsSeekAndExplain(t *testing.T) {
	f := newRoutingAdminMySQLFixture(t)
	id := f.create(t, 0, "large")
	now := time.Now().UTC().Truncate(time.Millisecond)
	bindings, grants, groups, channels := []map[string]any{}, []map[string]any{}, []map[string]any{}, []map[string]any{}
	for i := 1; i <= 1000; i++ {
		bindings = append(bindings, map[string]any{"group_id": id, "channel_id": i, "created_at": now, "updated_at": now})
		status := "revoked"
		var slot any
		if i%2 == 0 {
			status = "enabled"
		}
		if i%4 == 0 {
			slot = 1
		}
		grants = append(grants, map[string]any{"org_id": i, "group_id": id, "status": status, "grant_version": 1, "default_slot": slot, "created_at": now, "updated_at": now})
		groups = append(groups, map[string]any{"slug": fmt.Sprintf("bulk-%04d", i), "name": "Bulk", "owner_org_id": 0, "status": "draft", "policy_version": 1, "created_at": now, "updated_at": now})
		if i > 3 {
			channels = append(channels, map[string]any{"id": i, "name": "Bulk channel", "provider": "test", "base_url": "https://never-send.invalid", "credential_enc": "synthetic-placeholder", "models": "fixture-model"})
		}
	}
	for _, batch := range []struct {
		table string
		rows  []map[string]any
	}{{"channel_group_bindings", bindings}, {"org_group_grants", grants}, {"routing_groups", groups}, {"channels", channels}} {
		if err := f.db.Table(batch.table).CreateInBatches(batch.rows, 100).Error; err != nil {
			t.Fatal("cannot seed bounded-query fixture", batch.table)
		}
	}
	// Give the optimizer a meaningful cross-group population. The target
	// remains exactly 1000 rows; unrelated grants must never enter its oracle.
	otherGrants := make([]map[string]any, 0, 5000)
	for i := 1; i <= 5000; i++ {
		otherGrants = append(otherGrants, map[string]any{"org_id": i + 10000, "group_id": id + 1, "status": "enabled", "grant_version": 1, "created_at": now, "updated_at": now})
	}
	if err := f.db.Table("org_group_grants").CreateInBatches(otherGrants, 100).Error; err != nil {
		t.Fatal("cannot seed cross-group optimizer population")
	}
	type captured struct {
		table, query string
		args         []any
	}
	var queries []captured
	if err := f.db.Callback().Query().After("gorm:query").Register("routing_mysql_explain", func(q *gorm.DB) {
		switch q.Statement.Table {
		case "routing_groups", "channel_group_bindings", "org_group_grants", "channels":
			queries = append(queries, captured{q.Statement.Table, q.Statement.SQL.String(), append([]any(nil), q.Statement.Vars...)})
		}
	}); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/admin/routing-groups/%d", id)
	preview := func(exact bool) {
		out := routingAdminTestJSON(t, f.request("POST", path+"/impact-preview", f.tokens["su"], `{"expected_version":1,"change":{"kind":"update","status":"enabled"}}`), 200)
		counts := out["counts"].(map[string]any)
		for key, expected := range map[string]float64{"bindings": 1000, "grants": 1000, "enabled_grants": 500, "default_pointers": 250} {
			count := counts[key].(map[string]any)
			if count["observed_count"] != expected || count["exact"] != exact || count["lower_bound"] != !exact || count["limit"] != float64(1000) {
				t.Fatal("bounded relation count claimed false precision", key, count)
			}
		}
	}
	preview(true)
	routingAdminSQL(t, f.db, "INSERT INTO channel_group_bindings(group_id,channel_id,created_at,updated_at) VALUES(?,1001,UTC_TIMESTAMP(3),UTC_TIMESTAMP(3))", id)
	routingAdminSQL(t, f.db, "INSERT INTO org_group_grants(org_id,group_id,status,grant_version,default_slot,created_at,updated_at) VALUES(1001,?,'enabled',1,1,UTC_TIMESTAMP(3),UTC_TIMESTAMP(3))", id)
	preview(false)
	first := routingAdminTestJSON(t, f.request("GET", "/api/admin/routing-groups?owner_org_id=0&status=draft&limit=100", f.tokens["su"], ""), 200)
	if len(first["items"].([]any)) != 100 || first["has_more"] != true {
		t.Fatal("bounded group first page incorrect")
	}
	cursor := uint64(first["next_cursor"].(float64))
	second := routingAdminTestJSON(t, f.request("GET", fmt.Sprintf("/api/admin/routing-groups?owner_org_id=0&status=draft&limit=100&cursor=%d", cursor), f.tokens["su"], ""), 200)
	for _, item := range second["items"].([]any) {
		if uint64(item.(map[string]any)["id"].(float64)) <= cursor {
			t.Fatal("seek group page repeated earlier IDs")
		}
	}
	bindingsPage := routingAdminTestJSON(t, f.request("GET", path+"/channels?limit=100&cursor=100", f.tokens["su"], ""), 200)
	if len(bindingsPage["items"].([]any)) != 100 || bindingsPage["next_cursor"] != float64(200) {
		t.Fatal("binding seek did not return the independent expected range")
	}
	for i, item := range bindingsPage["items"].([]any) {
		if item.(map[string]any)["channel_id"] != float64(i+101) {
			t.Fatal("binding seek page is not ordered or complete")
		}
	}
	_ = f.db.Callback().Query().Remove("routing_mysql_explain")
	covered := map[string]bool{}
	for _, q := range queries {
		if q.query == "" || !strings.Contains(q.query, "MAX_EXECUTION_TIME(") {
			t.Fatal("administration SELECT omitted bounded optimizer hint")
		}
		var plan []struct {
			Table string
			Type  string
			Key   sql.NullString
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		err := f.db.WithContext(ctx).Raw("EXPLAIN "+q.query, q.args...).Scan(&plan).Error
		cancel()
		if err != nil || len(plan) == 0 {
			t.Fatal("actual bounded SELECT could not be explained")
		}
		for _, row := range plan {
			// Enforce the actual point/seek/batch lookups. A wide relationship
			// sample may legitimately choose a scan; record its plan, while the
			// independent 1000/1001 oracle and production deadline bound it.
			indexedLookup := q.table == "channels" || q.table == "channel_group_bindings" || q.table == "routing_groups" && strings.Contains(q.query, "WHERE id = ?")
			if indexedLookup && (!row.Key.Valid || row.Key.String == "" || row.Type == "ALL") {
				t.Fatalf("bounded-query index missing: table=%s type=%s key=%s", row.Table, row.Type, row.Key.String)
			}
			covered[q.table] = true
			t.Logf("actual SELECT table=%s type=%s key=%s", row.Table, row.Type, row.Key.String)
		}
	}
	if len(covered) != 4 {
		t.Fatal("EXPLAIN lacked group/binding/grant/channel positive controls")
	}
}

func TestMySQLRoutingAdminActualQueryDeadlineReleasesGate(t *testing.T) {
	f := newRoutingAdminMySQLFixture(t)
	var entered atomic.Bool
	if err := f.db.Callback().Query().Before("gorm:query").Register("routing_mysql_sleep", func(q *gorm.DB) {
		if q.Statement.Table == "routing_groups" && entered.CompareAndSwap(false, true) {
			var value int
			err := q.Statement.ConnPool.QueryRowContext(q.Statement.Context, "SELECT SLEEP(2)").Scan(&value)
			q.AddError(err)
		}
	}); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	w := f.request("GET", "/api/admin/routing-groups", f.tokens["su"], "")
	_ = f.db.Callback().Query().Remove("routing_mysql_sleep")
	routingAdminTestJSON(t, w, 503)
	if !entered.Load() || time.Since(started) > 1500*time.Millisecond || f.s.routingGroupReads.active != 0 || len(f.s.routingGroupReads.actors) != 0 {
		t.Fatal("real slow SELECT ignored request deadline or leaked admission slots")
	}
	routingAdminTestJSON(t, f.request("GET", "/api/admin/routing-groups", f.tokens["su"], ""), 200)
}
