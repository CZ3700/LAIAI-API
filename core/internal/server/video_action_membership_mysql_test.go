package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/crypto"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/routinggroup"
	"github.com/laiai/laiai-next/core/internal/video"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type videoActionNoNetwork struct{ calls atomic.Int32 }

func (n *videoActionNoNetwork) RoundTrip(*http.Request) (*http.Response, error) {
	n.calls.Add(1)
	return nil, errors.New("synthetic network forbidden")
}

// A transparent test-only wrapper records the actual caller transaction's
// disposition. Every query and Begin/Commit/Rollback still uses real MySQL.
type videoActionPoolTrace struct{ gorm.ConnPool }

type videoActionTxTrace struct {
	*sql.Tx
	commits, rollbacks int
}

func (p videoActionPoolTrace) BeginTx(ctx context.Context, opts *sql.TxOptions) (gorm.ConnPool, error) {
	tx, err := p.ConnPool.(gorm.TxBeginner).BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &videoActionTxTrace{Tx: tx}, nil
}

func (tx *videoActionTxTrace) Commit() error {
	tx.commits++
	return tx.Tx.Commit()
}

func (tx *videoActionTxTrace) Rollback() error {
	tx.rollbacks++
	return tx.Tx.Rollback()
}

// Reuse the tracked-migration four-model fixture. Only add the real evidence
// store required by the official HTTP audit middleware; never decrypt its
// deliberately synthetic channel credential or turn this into a Create test.
func videoActionHostFixture(t *testing.T) (*videoCatalogMySQLFixture, *gin.Engine) {
	t.Helper()
	f := newVideoCatalogMySQLFixture(t)
	keyring, err := crypto.NewKeyring("test:"+base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{'k'}, 32)), "test")
	if err != nil {
		t.Fatal("cannot create synthetic keyring")
	}
	service, err := video.NewService(f.db, keyring, bytes.Repeat([]byte{'h'}, 32), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal("cannot initialize official HTTP service")
	}
	service.Now = func() time.Time { return f.at }
	service.Assets = f.service.Assets // retain the same three portrait catalog reads
	transport := &videoActionNoNetwork{}
	service.Client = &http.Client{Transport: transport}
	t.Cleanup(func() {
		if transport.calls.Load() != 0 {
			t.Error("discovery attempted an upstream request")
		}
	})
	f.service, f.host.video = service, service
	r := gin.New()
	r.GET("/v1/models", f.host.listPricedModels)
	// These are the production middleware and handler, without a replacement
	// authentication stub or a server-startup worker goroutine.
	r.Group("/v1/video", service.Middleware()).GET("/models", service.HandleModels)
	return f, r
}

func videoActionGET(t *testing.T, r *gin.Engine, token, path string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, request)
	if w.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatal("discovery response lost private no-store")
	}
	return w
}

func videoActionIDs(t *testing.T, w *httptest.ResponseRecorder) []string {
	t.Helper()
	var response struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &response) != nil || response.Data == nil {
		t.Fatalf("discovery failed: status=%d", w.Code)
	}
	ids := make([]string, 0, len(response.Data))
	for _, row := range response.Data {
		ids = append(ids, row.ID)
	}
	sort.Strings(ids)
	return ids
}

func videoActionSubjectRead(q *gorm.DB) string {
	projection := strings.ToLower(strings.Join(q.Statement.Selects, " "))
	switch {
	case q.Statement.Table == "api_keys" && strings.Contains(projection, "model_limits_bytes"):
		return "subject_key"
	case strings.Contains(projection, "as active") && strings.Contains(projection, "o.status"):
		return "subject_principal"
	case q.Statement.Table == "org_entitlements" && strings.Contains(projection, "feature_video"):
		return "subject_entitlements"
	default:
		return ""
	}
}

func videoActionStatementClass(q *gorm.DB) string {
	if kind := videoActionSubjectRead(q); kind != "" {
		return kind
	}
	sqlText := strings.ToLower(strings.ReplaceAll(q.Statement.SQL.String(), "`", ""))
	// Match the actual executed statement, including derived/aggregate readers.
	for _, table := range []string{"video_minimum_rule_cells", "video_minimum_rule_sets", "video_pricing_rules", "video_model_configs", "video_provider_scopes", "video_org_policies", "model_group_ratios", "video_asset_org_routes", "video_asset_shared_defaults", "video_asset_scope_configs", "model_prices", "model_meta"} {
		if strings.Contains(sqlText, table) {
			return table
		}
	}
	if strings.Contains(sqlText, "from organizations") {
		return "billing_group"
	}
	if strings.Contains(sqlText, "from channels") {
		if strings.Contains(sqlText, "has_usable_credential") {
			return "official_channels"
		}
		return "generic_channels"
	}
	return "unexpected_select"
}

// Query and Row callbacks observe GORM Find/Scan and the aggregate Rows path;
// Raw is registered too, so adding an Exec("SELECT ...") cannot evade counting.
func videoActionObserve(t *testing.T, db *gorm.DB, name string, before, after func(*gorm.DB)) func() {
	t.Helper()
	if before != nil {
		if err := db.Callback().Query().Before("gorm:query").Register(name, before); err != nil {
			t.Fatal("cannot install statement fault observer")
		}
	}
	if after != nil {
		if err := db.Callback().Query().After("gorm:query").Register(name+"_after", after); err != nil {
			t.Fatal("cannot observe Query")
		}
		if err := db.Callback().Row().After("gorm:row").Register(name+"_after", after); err != nil {
			t.Fatal("cannot observe Row")
		}
		if err := db.Callback().Raw().After("gorm:raw").Register(name+"_after", after); err != nil {
			t.Fatal("cannot observe Raw")
		}
	}
	removed := false
	remove := func() {
		if removed {
			return
		}
		removed = true
		if before != nil {
			_ = db.Callback().Query().Remove(name)
		}
		if after != nil {
			_ = db.Callback().Query().Remove(name + "_after")
			_ = db.Callback().Row().Remove(name + "_after")
			_ = db.Callback().Raw().Remove(name + "_after")
		}
	}
	t.Cleanup(remove)
	return remove
}

func videoActionMoneyFacts(t *testing.T, db *gorm.DB) map[string]routingAdminFingerprint {
	t.Helper()
	// Official HTTP legitimately adds request-audit/history metadata. Those are
	// excluded, while every financial/admission table remains unchanged.
	out := routingAdminFingerprints(t, db, "ledger_accounts", "ledger_holds", "ledger_journal", "ledger_operations", "ledger_movements", "tasks", "video_operations", "video_billing_snapshots", "video_quota_buckets", "video_quota_reservations", "video_credential_snapshots")
	var keys []struct {
		ID, OrgID              uint64
		QuotaLimit, UsedAmount string
	}
	if err := db.Table("api_keys").Select("id,org_id,quota_limit,used_amount").Order("id").Find(&keys).Error; err != nil {
		t.Fatal("cannot observe Key financial projection")
	}
	raw, err := json.Marshal(keys)
	if err != nil {
		t.Fatal("cannot serialize Key financial projection")
	}
	hash := sha256.Sum256(raw)
	out["api_key_money"] = routingAdminFingerprint{Rows: len(keys), Hash: hex.EncodeToString(hash[:])}
	return out
}

func TestMySQLVideoActionMembershipDiscoverySnapshotAndBudgets(t *testing.T) {
	for _, route := range []struct {
		name, path string
		count      int
	}{{"official", "/v1/video/models", 15}, {"sdk", "/v1/models", 18}} {
		t.Run(route.name, func(t *testing.T) {
			f, r := videoActionHostFixture(t)
			before := videoActionMoneyFacts(t, f.db)
			var pool gorm.ConnPool
			var statements []*gorm.DB
			classes := map[string]int{}
			readonly, changed := false, false
			var readerID, writerID uint64
			hint := regexp.MustCompile(`MAX_EXECUTION_TIME\(([0-9]+)\)`)
			remove := videoActionObserve(t, f.db, "video_action_snapshot", nil, func(q *gorm.DB) {
				if pool != nil && pool != q.Statement.ConnPool && (videoActionSubjectRead(q) != "" || hint.MatchString(q.Statement.SQL.String())) {
					t.Error("bounded directory SELECT escaped the captured transaction")
				}
				if _, ok := q.Statement.ConnPool.(gorm.TxCommitter); !ok {
					return // VerifyKey is outside the directory transaction.
				}
				if pool == nil && videoActionSubjectRead(q) == "subject_key" {
					pool = q.Statement.ConnPool
				}
				if pool == nil || pool != q.Statement.ConnPool || !strings.HasPrefix(strings.TrimSpace(strings.ToUpper(q.Statement.SQL.String())), "SELECT") {
					return // request audit has a different transaction and budget
				}
				statements = append(statements, q)
				classes[videoActionStatementClass(q)]++
				match := hint.FindStringSubmatch(q.Statement.SQL.String())
				if len(match) != 2 {
					t.Error("directory SELECT lacks execution-time hint")
				} else if millis, err := strconv.Atoi(match[1]); err != nil || millis < 1 || millis > 200 {
					t.Error("directory SELECT exceeded fixed hint budget")
				}
				if deadline, ok := q.Statement.Context.Deadline(); !ok || time.Until(deadline) > 200*time.Millisecond {
					t.Error("directory SELECT exceeded caller/200ms context")
				}
				if changed || videoActionSubjectRead(q) != "subject_key" {
					return
				}
				changed = true
				// This is AFTER the actual first consistent subject read. SQL uses
				// the underlying pool directly only for independent test probes;
				// those probes are not counted as application SELECTs.
				if err := pool.QueryRowContext(q.Statement.Context, "SELECT CONNECTION_ID()").Scan(&readerID); err != nil {
					t.Error("cannot identify reader connection")
				}
				_, err := pool.ExecContext(q.Statement.Context, "UPDATE organizations SET name='must-not-write' WHERE id=70001")
				var my *mysqlDriver.MySQLError
				readonly = errors.As(err, &my) && my.Number == 1792
				writeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				err = f.db.WithContext(writeCtx).Transaction(func(writer *gorm.DB) error {
					if e := writer.Statement.ConnPool.QueryRowContext(writeCtx, "SELECT CONNECTION_ID()").Scan(&writerID); e != nil {
						return e
					}
					if e := writer.Exec("UPDATE api_keys SET model_limits='priced' WHERE id=? AND org_id=70001", f.key.ID).Error; e != nil {
						return e
					}
					if e := writer.Exec("UPDATE video_pricing_rules SET retail_usd_per_1m=99 WHERE provider_scope_id=70001").Error; e != nil {
						return e
					}
					return writer.Exec("UPDATE video_org_policies SET async_execution_allowed=0 WHERE org_id=70001").Error
				})
				if err != nil {
					t.Error("second connection could not commit snapshot change")
				}
			})
			w := videoActionGET(t, r, f.plain, route.path)
			remove()
			wantIDs := []string{ark.Model20, ark.Model20Fast, ark.Model20Mini, ark.Model25}
			if route.name == "sdk" {
				wantIDs = append([]string{"priced"}, wantIDs...)
			}
			if !reflect.DeepEqual(videoActionIDs(t, w), wantIDs) || !readonly || !changed || readerID == 0 || readerID == writerID {
				t.Fatal("discovery did not use one real readonly snapshot and an independent writer")
			}
			wantClasses := map[string]int{"subject_key": 1, "subject_principal": 1, "subject_entitlements": 1, "video_model_configs": 1, "video_provider_scopes": 1, "video_org_policies": 1, "official_channels": 1, "video_pricing_rules": 1, "video_minimum_rule_sets": 1, "video_minimum_rule_cells": 1, "billing_group": 1, "model_group_ratios": 1, "video_asset_org_routes": 1, "video_asset_shared_defaults": 1, "video_asset_scope_configs": 1}
			if route.name == "sdk" {
				wantClasses["model_prices"], wantClasses["generic_channels"], wantClasses["model_meta"] = 1, 1, 1
			}
			if len(statements) != route.count || !reflect.DeepEqual(classes, wantClasses) {
				t.Fatalf("unexpected directory SELECTs: count=%d classes=%v", len(statements), classes)
			}
			for _, q := range statements {
				kind := videoActionStatementClass(q)
				defaultEntitlement := kind == "subject_entitlements" && errors.Is(q.Error, gorm.ErrRecordNotFound) && q.RowsAffected == 0
				if q.Error != nil && !defaultEntitlement || q.RowsAffected < 0 {
					t.Fatal("a captured SELECT did not complete successfully")
				}
				if kind == "video_minimum_rule_cells" {
					cells, decoded, _ := catalogTariffsAggregateFacts(t, q)
					if q.RowsAffected != 1 || cells != 8 || decoded != 8 {
						t.Fatal("aggregate count was not observed after real scan/close")
					}
				}
			}
			if route.name == "official" {
				var body struct {
					Data []video.CatalogModel `json:"data"`
				}
				if json.Unmarshal(w.Body.Bytes(), &body) != nil {
					t.Fatal("invalid official model DTO")
				}
				amounts := map[string][2]string{ark.Model20: {"4", "5"}, ark.Model25: {"5", "6"}, ark.Model20Fast: {"6", "7"}, ark.Model20Mini: {"0.875", "1"}}
				for _, row := range body.Data {
					if len(row.Prices) != 2 || !row.Actions.Create.Eligible || row.Publication.Status != "published" {
						t.Fatal("policy/publication escaped original snapshot")
					}
					for _, price := range row.Prices {
						i := 0
						if price.InputVideo {
							i = 1
						}
						if price.Amount != amounts[row.ID][i] {
							t.Fatal("exact price snapshot changed")
						}
					}
				}
			}
			nextIDs := []string{}
			if route.name == "sdk" {
				nextIDs = []string{"priced"}
			}
			if !reflect.DeepEqual(videoActionIDs(t, videoActionGET(t, r, f.plain, route.path)), nextIDs) {
				t.Fatal("next request did not observe committed membership change")
			}
			if !reflect.DeepEqual(before, videoActionMoneyFacts(t, f.db)) {
				t.Fatal("discovery changed financial/admission facts")
			}
		})
	}
}

func TestMySQLVideoActionMembershipGroupedAuthenticationRemainsClosed(t *testing.T) {
	f, r := videoActionHostFixture(t)
	// Grouped keys cannot be issued by production; the fixture alone writes a
	// structurally valid row under the real CHECK constraints, as in A1 tests.
	routingAdminSQL(t, f.db, "INSERT INTO routing_groups(id,slug,name,owner_org_id,status,policy_version,created_at,updated_at) VALUES(70001,'action-fixture','Synthetic group',0,'enabled',1,?,?)", f.at, f.at)
	routingAdminSQL(t, f.db, "INSERT INTO org_group_grants(org_id,group_id,status,grant_version,created_at,updated_at) VALUES(70001,70001,'enabled',1,?,?)", f.at, f.at)
	routingAdminSQL(t, f.db, "INSERT INTO channel_group_bindings(group_id,channel_id,created_at,updated_at) VALUES(70001,70001,?,?)", f.at, f.at)
	for _, mode := range []string{"inherit_group", "subset"} {
		t.Run(mode, func(t *testing.T) {
			limits := ""
			if mode == "subset" {
				limits = ark.Model20
			}
			routingAdminSQL(t, f.db, "UPDATE api_keys SET model_access_mode=?,routing_group_id=70001,model_limits=? WHERE id=?", mode, limits, f.key.ID)
			before := videoActionMoneyFacts(t, f.db)
			configBefore := routingAdminFingerprints(t, f.db, "api_keys", "routing_groups", "org_group_grants", "channel_group_bindings", "routing_policy_events")
			subjectReads := 0
			remove := videoActionObserve(t, f.db, "video_action_grouped", nil, func(q *gorm.DB) {
				if videoActionSubjectRead(q) != "" {
					subjectReads++
				}
			})
			for _, path := range []string{"/v1/video/models", "/v1/models"} {
				w := videoActionGET(t, r, f.plain, path)
				videoActionSafeFailure(t, w, http.StatusUnauthorized)
			}
			remove()
			if subjectReads != 0 || !reflect.DeepEqual(before, videoActionMoneyFacts(t, f.db)) || !reflect.DeepEqual(configBefore, routingAdminFingerprints(t, f.db, "api_keys", "routing_groups", "org_group_grants", "channel_group_bindings", "routing_policy_events")) {
				t.Fatal("grouped authentication entered discovery or changed durable facts")
			}
		})
	}
}

func videoActionSafeFailure(t *testing.T, w *httptest.ResponseRecorder, status int) {
	t.Helper()
	if w.Code != status || !json.Valid(w.Body.Bytes()) || strings.Contains(w.Body.String(), `"data"`) {
		t.Fatalf("unsafe/partial discovery failure: status=%d want=%d", w.Code, status)
	}
	for _, private := range []string{"rc46_action_private_probe", "synthetic-never-decrypted", "model_limits", "routing_group_id", "credential", "SELECT", "1054", "SLEEP"} {
		if strings.Contains(w.Body.String(), private) {
			t.Fatal("private failure details reached HTTP")
		}
	}
}

func TestMySQLVideoActionMembershipSubjectFailuresAreSafe(t *testing.T) {
	for _, fault := range []string{"subject_key", "subject_principal", "subject_entitlements", "deadline", "revoked_after_auth"} {
		t.Run(fault, func(t *testing.T) {
			f, r := videoActionHostFixture(t)
			before := videoActionMoneyFacts(t, f.db)
			basePool := f.db.Statement.ConnPool
			if _, ok := basePool.(gorm.TxBeginner); !ok {
				t.Fatal("fixture does not expose a real SQL pool")
			}
			tracedPool := videoActionPoolTrace{ConnPool: basePool}
			f.db.Statement.ConnPool, f.db.Config.ConnPool = tracedPool, tracedPool
			var logs bytes.Buffer
			f.db.Config.Logger = logger.New(log.New(&logs, "", 0), logger.Config{LogLevel: logger.Warn, Colorful: false})
			// Positive control proves the inherited logger really emits SQL errors.
			if f.db.Exec("SELECT rc46_action_private_probe_logger").Error == nil || !strings.Contains(logs.String(), "rc46_action_private_probe_logger") {
				t.Fatal("unsafe SQL logger control did not execute")
			}
			logs.Reset()
			injected, actualFault := false, false
			var target *gorm.DB
			remove := videoActionObserve(t, f.db, "video_action_failure", func(q *gorm.DB) {
				if injected || fault == "revoked_after_auth" {
					return
				}
				kind := videoActionSubjectRead(q)
				if kind == "" || kind != fault && !(fault == "deadline" && kind == "subject_entitlements") {
					return
				}
				injected, target = true, q
				q.Statement.SQL.Reset()
				q.Statement.Vars = nil
				if fault == "deadline" {
					q.Statement.SQL.WriteString("SELECT SLEEP(2)")
				} else {
					q.Statement.SQL.WriteString("SELECT rc46_action_private_probe_column FROM api_keys LIMIT 1")
				}
			}, func(q *gorm.DB) {
				if fault == "revoked_after_auth" && !injected && q.Statement.Table == "api_keys" {
					if _, inTx := q.Statement.ConnPool.(gorm.TxCommitter); !inTx {
						injected = true
						result := f.db.Exec("UPDATE api_keys SET status=0 WHERE id=?", f.key.ID)
						actualFault = result.Error == nil && result.RowsAffected == 1
					}
				}
				if target != q {
					return
				}
				if fault == "deadline" {
					actualFault = errors.Is(q.Error, context.DeadlineExceeded) && errors.Is(q.Statement.Context.Err(), context.DeadlineExceeded)
				} else {
					var my *mysqlDriver.MySQLError
					actualFault = errors.As(q.Error, &my) && my.Number == 1054
				}
			})
			path, status := "/v1/models", http.StatusServiceUnavailable
			if fault == "deadline" || fault == "revoked_after_auth" {
				path = "/v1/video/models"
			}
			if fault == "revoked_after_auth" {
				status = http.StatusForbidden
			}
			w := videoActionGET(t, r, f.plain, path)
			remove()
			videoActionSafeFailure(t, w, status)
			if !injected || !actualFault {
				t.Fatal("failure did not originate at the selected real SQL/auth boundary")
			}
			if target != nil {
				tx, ok := target.Statement.ConnPool.(*videoActionTxTrace)
				if !ok || tx.commits != 0 || tx.rollbacks != 1 {
					t.Fatal("failed subject transaction was not rolled back exactly once")
				}
				var unused int
				if err := tx.QueryRowContext(context.Background(), "SELECT 1").Scan(&unused); !errors.Is(err, sql.ErrTxDone) {
					t.Fatal("failed subject transaction remained open")
				}
			}
			if strings.Contains(logs.String(), "rc46_action_private_probe") || strings.Contains(logs.String(), "SLEEP(2)") || strings.Contains(logs.String(), f.plain) {
				t.Fatal("reader failure leaked SQL or credential to inherited logger")
			}
			if !reflect.DeepEqual(before, videoActionMoneyFacts(t, f.db)) {
				t.Fatal("failed discovery changed money or admission facts")
			}
		})
	}
}

func TestMySQLVideoActionMembershipReaderUsesOneObservation(t *testing.T) {
	f := newRoutingCandidateFixture(t, 0)
	f.channel(t, 10, "alpha, Alpha ,public,public")
	f.channel(t, 11, "alpha")
	// Keep the fixture's grouped key unchanged for its original closed-auth
	// cleanup control. A separately issued real legacy key is the three-read case.
	_, legacy, err := identity.IssueKey(f.db, identity.IssueKeyInput{OrgID: f.ref.OrgID, Name: "membership-legacy", Scopes: "video:read", ModelLimits: "Alpha, alpha, public"})
	if err != nil {
		t.Fatal("cannot issue legacy membership control")
	}
	tables := []string{"api_keys", "routing_groups", "org_group_grants", "channel_group_bindings", "routing_policy_events", "channels", "ledger_accounts", "ledger_holds", "ledger_journal", "tasks", "video_operations", "video_billing_snapshots"}
	before := routingAdminFingerprints(t, f.db, tables...)
	for _, sample := range []struct {
		mode, assurance string
		ref             routinggroup.CandidateSubjectRef
		selects         int
	}{
		{"legacy", "legacy_model_membership", routinggroup.CandidateSubjectRef{OrgID: f.ref.OrgID, KeyID: legacy.ID}, 3},
		{"inherit_group", "group_membership_only", f.ref, 7},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		tx := f.db.WithContext(ctx).Begin(&sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
		if tx.Error != nil {
			cancel()
			t.Fatal("cannot begin membership caller transaction")
		}
		captured, removeCapture := candidateCapture(t, f.db, tx.Statement.ConnPool)
		allSelects := 0
		removeAll := videoActionObserve(t, f.db, "video_action_membership_all", nil, func(q *gorm.DB) {
			if strings.HasPrefix(strings.TrimSpace(strings.ToUpper(q.Statement.SQL.String())), "SELECT") {
				allSelects++
				if q.Statement.ConnPool != tx.Statement.ConnPool {
					t.Error("membership SELECT escaped caller connection")
				}
			}
		})
		// Keep transaction/callback cleanup inside this scope, including Fatal.
		func() {
			defer cancel()
			defer tx.Rollback()
			defer removeCapture()
			defer removeAll()
			observation, err := f.resolver.ReadMembershipTx(ctx, tx, sample.ref, f.at)
			if err != nil {
				t.Fatal("real membership observation failed", sample.mode)
			}
			if len(captured()) != sample.selects || allSelects != sample.selects {
				t.Fatalf("membership reads mode=%s query=%d all=%d want=%d", sample.mode, len(captured()), allSelects, sample.selects)
			}
			seen := map[string]int{}
			for _, q := range captured() {
				seen[q.table]++
				if !regexp.MustCompile(`MAX_EXECUTION_TIME\((?:[1-9][0-9]?|1[0-9]{2}|200)\)`).MatchString(q.sql) {
					t.Fatal("membership SQL omitted bounded execution hint")
				}
			}
			if seen["api_keys"] != 1 || seen["o"] != 1 || seen["org_entitlements"] != 1 {
				t.Fatal("membership repeated or omitted current subject facts")
			}
			if sample.mode == "legacy" && (seen["routing_groups"] != 0 || seen["org_group_grants"] != 0 || seen["channel_group_bindings"] != 0 || seen["channels"] != 0) {
				t.Fatal("legacy membership queried grouped/default/channel facts")
			}
			for _, want := range []struct {
				model    string
				included bool
				channels []uint64
			}{{"alpha", true, []uint64{10, 11}}, {"Alpha", true, []uint64{10}}, {"public", true, []uint64{10}}, {"missing", false, []uint64{}}} {
				got, err := observation.ResolveModel(want.model)
				ids := want.channels
				if sample.mode == "legacy" {
					ids = []uint64{}
				}
				if err != nil || got.Mode != sample.mode || got.Model != want.model || got.ModelIncluded != want.included || !reflect.DeepEqual(got.ChannelIDs, ids) {
					t.Fatal("membership differs from literal model/channel oracle", sample.mode, want.model)
				}
			}
			fact, err := observation.Observation()
			if err != nil || fact.Assurance != sample.assurance || fact.OrgID != sample.ref.OrgID || fact.KeyID != sample.ref.KeyID || !fact.CheckedAt.Equal(f.at) {
				t.Fatal("membership observation identity/assurance mismatch")
			}
			if sample.mode == "legacy" && (fact.GroupID != 0 || fact.GroupPolicyVersion != 0 || fact.GrantVersion != 0) {
				t.Fatal("legacy observation invented group versions")
			}
			if sample.mode != "legacy" && (fact.GroupID != f.group || fact.GroupPolicyVersion != f.version || fact.GrantVersion != 1) {
				t.Fatal("grouped observation lost configuration versions")
			}
			if len(captured()) != sample.selects || allSelects != sample.selects {
				t.Fatal("pure membership accessors added SQL")
			}
			// The reader must leave transaction completion to this caller.
			if err := tx.Commit().Error; err != nil {
				t.Fatal("membership closed its caller transaction")
			}
		}()
	}
	if !reflect.DeepEqual(before, routingAdminFingerprints(t, f.db, tables...)) {
		t.Fatal("membership observation mutated durable facts")
	}
}
