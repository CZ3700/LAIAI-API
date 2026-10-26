package video

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/relay"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

func actionAdmissionRequest() ark.CreateRequest {
	return ark.CreateRequest{Model: ark.Model20, Content: []ark.ContentItem{{Type: "text", Text: "test prompt private"}}}
}

// Full encrypted payload/staging rows stay in memory; only their digest is
// compared. This also catches a leaked Stage that financial row counts miss.
func actionAdmissionFacts(t *testing.T, f *fixture, taskID string) string {
	t.Helper()
	var metadata, staging []map[string]any
	must(t, f.s.DB.Table("invocation_payloads").Where("org_id = ?", f.key.OrgID).Order("payload_id").Find(&metadata).Error)
	must(t, f.s.DB.Table("invocation_payload_staging AS s").Select("s.*").Joins("JOIN invocation_payloads p ON p.payload_id = s.payload_id").Where("p.org_id = ?", f.key.OrgID).Order("s.payload_id").Find(&staging).Error)
	raw, err := json.Marshal([]any{catalogCreateFacts(t, f, taskID), metadata, staging})
	must(t, err)
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:])
}

// Register establishes the actual owner membership; IssueKey associates the
// user with that organization. No synthetic member row bypasses identity APIs.
func actionAdmissionNamedKey(t *testing.T, f *fixture) {
	t.Helper()
	id, err := randomID("action-user-")
	must(t, err)
	must(t, f.s.DB.Transaction(func(tx *gorm.DB) error {
		user, org, err := identity.Register(tx, identity.RegisterInput{Email: id + "@example.invalid", Password: "synthetic-Action-Password-123", EmailVerified: true})
		if err != nil {
			return err
		}
		f.plain, f.key, err = identity.IssueKey(tx, identity.IssueKeyInput{OrgID: org.ID, UserID: &user.ID, Name: "named action subject", Scopes: "video:*"})
		if err != nil {
			return err
		}
		account, err := ledger.EnsureAccount(tx, org.ID, ledger.AccountUserCredit)
		if err != nil {
			return err
		}
		_, _, err = ledger.Post(tx, ledger.PostInput{IdempotencyKey: id, AccountID: account.ID, Kind: ledger.KindCredit, Delta: decimal.NewFromInt(100), RefType: "integration", RefID: id})
		if err != nil {
			return err
		}
		return tx.Create(&Policy{OrgID: org.ID, AsyncExecutionAllowed: true, CaptureHistory: true, RetentionDays: 30, UpdatedAt: f.now}).Error
	}))
}

func TestMySQLOfficialActionAdmissionUsesDatabaseSubject(t *testing.T) {
	for _, fault := range []string{"key_disabled", "key_expired", "org_disabled", "model_removed", "scope_removed", "feature_disabled", "user_disabled"} {
		t.Run(fault, func(t *testing.T) {
			f := mysqlFixture(t)
			if fault == "user_disabled" {
				actionAdmissionNamedKey(t, f)
			}
			first := f.create(t, "action-subject-original")
			// ID, OrgID and UserID remain trusted immutable identity references.
			// This test changes only mutable authorization facts in storage.
			keyCopy := *f.key
			keyQ := f.s.DB.Model(&identity.APIKey{}).Where("id = ? AND org_id = ?", f.key.ID, f.key.OrgID)
			switch fault {
			case "user_disabled":
				must(t, f.s.DB.Model(&identity.User{}).Where("id = ?", *keyCopy.UserID).Update("status", 2).Error)
			case "key_disabled":
				must(t, keyQ.Update("status", 2).Error)
			case "key_expired":
				must(t, keyQ.Update("expires_at", f.now.Add(-time.Hour)).Error)
			case "org_disabled":
				must(t, f.s.DB.Model(&identity.Organization{}).Where("id = ?", f.key.OrgID).Update("status", 2).Error)
			case "model_removed":
				must(t, keyQ.Update("model_limits", ark.Model20Mini).Error)
			case "scope_removed":
				must(t, keyQ.Update("scopes", "video:read").Error)
			case "feature_disabled":
				ent := identity.DefaultEntitlements(f.key.OrgID)
				ent.FeatureVideo = false
				must(t, f.s.DB.Create(&ent).Error)
			}
			before := actionAdmissionFacts(t, f, first.ID)
			if fault != "feature_disabled" {
				if _, err := f.s.EstimateForKey(context.Background(), &keyCopy, actionAdmissionRequest()); !errors.Is(err, ErrForbidden) {
					t.Fatal("stale authenticated key bypassed estimate subject observation")
				}
			}
			_, replay, err := f.s.Create(context.Background(), &keyCopy, f.requestID("action-denied"), "action-subject-new", actionAdmissionRequest())
			if !errors.Is(err, ErrForbidden) || replay {
				t.Fatal("stale authenticated key admitted a new operation")
			}
			// This service-level replay is the pre-existing behavior with the
			// same caller key copy. HTTP authentication remains its own boundary.
			again, replay, err := f.s.Create(context.Background(), &keyCopy, f.requestID("action-replay"), "action-subject-original", actionAdmissionRequest())
			must(t, err)
			if !replay || again.ID != first.ID {
				t.Fatal("new-subject admission changed the original replay branch")
			}
			if actionAdmissionFacts(t, f, first.ID) != before || f.posts.Load() != 0 || f.gets.Load() != 0 {
				t.Fatal("denied admission or replay changed money, quota or durable acceptance")
			}
			must(t, f.s.DB.Model(&identity.APIKey{}).Where("id = ? AND org_id = ?", f.key.ID, f.key.OrgID).Updates(map[string]any{"status": 1, "expires_at": nil, "model_limits": "", "scopes": "video:*"}).Error)
			must(t, f.s.DB.Model(&identity.Organization{}).Where("id = ?", f.key.OrgID).Update("status", 1).Error)
			if fault == "feature_disabled" {
				must(t, f.s.DB.Model(&identity.Entitlements{}).Where("org_id = ?", f.key.OrgID).Update("feature_video", true).Error)
			}
			if fault == "user_disabled" {
				must(t, f.s.DB.Model(&identity.User{}).Where("id = ?", *keyCopy.UserID).Update("status", 1).Error)
			}
			next, replay, err := f.s.Create(context.Background(), &keyCopy, f.requestID("action-restored"), "action-subject-new", actionAdmissionRequest())
			must(t, err)
			if replay || next.ID == first.ID || !f.hold(t, next.ID).Amount.Equal(f.hold(t, first.ID).Amount) || f.hold(t, next.ID).Amount.String() != "0.864" {
				t.Fatal("failed subject check left an idempotency reservation or changed the original price")
			}
		})
	}
}

func TestMySQLOfficialActionEstimateKeepsExecutionPrerequisitesIndependent(t *testing.T) {
	for _, fault := range []string{"feature_disabled", "policy_missing", "policy_disabled", "retention_invalid", "credential_missing"} {
		t.Run(fault, func(t *testing.T) {
			f := mysqlFixture(t)
			first := f.create(t, "action-estimate-original")
			var cfg ModelConfig
			must(t, f.s.DB.Where("public_model = ?", ark.Model20).First(&cfg).Error)
			switch fault {
			case "feature_disabled":
				ent := identity.DefaultEntitlements(f.key.OrgID)
				ent.FeatureVideo = false
				must(t, f.s.DB.Create(&ent).Error)
			case "policy_missing":
				must(t, f.s.DB.Where("org_id = ?", f.key.OrgID).Delete(&Policy{}).Error)
			case "policy_disabled":
				must(t, f.s.DB.Model(&Policy{}).Where("org_id = ?", f.key.OrgID).Update("async_execution_allowed", false).Error)
			case "retention_invalid":
				must(t, f.s.DB.Model(&Policy{}).Where("org_id = ?", f.key.OrgID).Update("retention_days", 31).Error)
			case "credential_missing":
				must(t, f.s.DB.Model(&relay.Channel{}).Where("id = ?", cfg.ChannelID).Update("credential_enc", "").Error)
			}
			before := actionAdmissionFacts(t, f, first.ID)
			quote, err := f.s.EstimateForKey(context.Background(), f.key, actionAdmissionRequest())
			must(t, err)
			if quote.HoldUSD.String() != "0.864" || quote.Rule.RetailUSDPer1M.String() != "8" || quote.Metering.FXUSDPerCNY.String() != "0.14" {
				t.Fatal("estimate changed fixed price or FX")
			}
			_, replay, err := f.s.Create(context.Background(), f.key, f.requestID("action-execution-gate"), "action-estimate-new", actionAdmissionRequest())
			want := ErrForbidden
			if fault == "retention_invalid" {
				want = ErrCapability
			}
			if fault == "credential_missing" {
				want = ErrUnavailable
			}
			if !errors.Is(err, want) || replay {
				t.Fatal("create lost its independent execution prerequisite")
			}
			if before != actionAdmissionFacts(t, f, first.ID) || f.posts.Load() != 0 || f.gets.Load() != 0 {
				t.Fatal("estimate or rejected create changed financial state")
			}
		})
	}
}

func TestMySQLOfficialActionAdmissionReadFailureRollsBack(t *testing.T) {
	for _, fault := range []string{"sql", "cancel"} {
		t.Run(fault, func(t *testing.T) {
			f := mysqlFixture(t)
			first := f.create(t, "action-storage-original")
			before := actionAdmissionFacts(t, f, first.ID)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			hits, storage1054, cancelledAfterRead := 0, 0, 0
			name := "action:failure_" + fault
			callback := f.s.DB.Callback().Query()
			if fault == "sql" {
				must(t, callback.Before("gorm:query").Register(name, func(q *gorm.DB) {
					if q.Statement.Table == "api_keys" && strings.Contains(strings.Join(q.Statement.Selects, " "), "model_limits_bytes") {
						hits++
						q.Statement.Selects = []string{"missing_action_subject_column"}
					}
				}))
				must(t, callback.After("gorm:query").Register(name+":actual", func(q *gorm.DB) {
					if q.Statement.Table == "api_keys" && strings.Contains(q.Statement.SQL.String(), "missing_action_subject_column") {
						var driverError *mysqlDriver.MySQLError
						if errors.As(q.Error, &driverError) && driverError.Number == 1054 {
							storage1054++
						}
					}
				}))
				defer callback.Remove(name + ":actual")
			} else {
				must(t, callback.After("gorm:query").Register(name, func(q *gorm.DB) {
					if q.Statement.Table == "api_keys" && strings.Contains(strings.Join(q.Statement.Selects, " "), "model_limits_bytes") && q.Error == nil && q.RowsAffected == 1 {
						hits++
						cancelledAfterRead++
						cancel()
					}
				}))
			}
			defer callback.Remove(name)
			_, replay, err := f.s.Create(ctx, f.key, f.requestID("action-storage-fault"), "action-storage-new", actionAdmissionRequest())
			must(t, callback.Remove(name))
			if fault == "sql" {
				must(t, callback.Remove(name+":actual"))
			}
			if fault == "sql" && storage1054 != 1 || fault == "cancel" && cancelledAfterRead != 1 {
				t.Fatal("actual subject storage/cancellation fault was not observed")
			}
			want := ErrCatalogUnavailable
			if fault == "cancel" {
				want = context.Canceled
			}
			if !errors.Is(err, want) || hits != 1 || replay || strings.Contains(err.Error(), "missing_action_subject_column") {
				t.Fatal("subject read failure was erased, exposed, or missed")
			}
			if before != actionAdmissionFacts(t, f, first.ID) || f.posts.Load() != 0 || f.gets.Load() != 0 {
				t.Fatal("failed subject observation left acceptance side effects")
			}
			var snapshots int64
			must(t, f.s.DB.Model(&videobilling.Snapshot{}).Where("org_id = ?", f.key.OrgID).Count(&snapshots).Error)
			if snapshots != 1 {
				t.Fatal("subject failure created a second billing snapshot")
			}
		})
	}
}

type actionAdmissionSelects struct {
	total   int
	subject map[string]int
	plan    map[string]int
	pool    gorm.ConnPool
}

// Observe actual Query/Row/Raw SELECTs with one callback. Counts are attributed
// to subject projections and publication tables, not just a coincident total.
func actionAdmissionObserve(t *testing.T, db *gorm.DB, capture **actionAdmissionSelects) func() {
	t.Helper()
	name := "action:select_budget"
	observe := func(q *gorm.DB) {
		if *capture == nil {
			return
		}
		sqlText := strings.ToLower(strings.ReplaceAll(q.Statement.SQL.String(), "`", ""))
		if !strings.HasPrefix(strings.TrimSpace(sqlText), "select ") {
			return
		}
		out := *capture
		out.total++
		if out.pool == nil {
			out.pool = q.Statement.ConnPool
		}
		if out.pool != q.Statement.ConnPool {
			t.Error("subject or publication escaped the caller transaction")
		}
		if _, ok := out.pool.(gorm.TxCommitter); !ok {
			t.Error("observation did not run in a real transaction")
		}
		projection := strings.ToLower(strings.Join(q.Statement.Selects, " "))
		switch {
		case q.Statement.Table == "api_keys" && strings.Contains(projection, "model_limits_bytes"):
			out.subject["key"]++
		case strings.Contains(projection, "o.status") && strings.Contains(projection, "as active"):
			out.subject["principal"]++
		case q.Statement.Table == "org_entitlements" && strings.Contains(projection, "feature_video"):
			out.subject["entitlements"]++
		default:
			for _, table := range []string{"video_model_configs", "video_provider_scopes", "video_pricing_rules", "video_minimum_rule_sets", "video_minimum_rule_cells", "model_group_ratios", "group_ratios"} {
				if strings.Contains(sqlText, "from "+table) || strings.Contains(sqlText, "join "+table) {
					out.plan[table]++
				}
			}
		}
	}
	must(t, db.Callback().Query().After("gorm:query").Register(name, observe))
	must(t, db.Callback().Row().After("gorm:row").Register(name, observe))
	must(t, db.Callback().Raw().After("gorm:raw").Register(name, observe))
	removed := false
	remove := func() {
		if removed {
			return
		}
		removed = true
		must(t, db.Callback().Query().Remove(name))
		must(t, db.Callback().Row().Remove(name))
		must(t, db.Callback().Raw().Remove(name))
	}
	t.Cleanup(remove)
	return remove
}

func TestMySQLOfficialActionAdmissionReadsSubjectOnceAndReplayNever(t *testing.T) {
	f := mysqlFixture(t)
	var capture *actionAdmissionSelects
	remove := actionAdmissionObserve(t, f.s.DB, &capture)
	begin := func() *actionAdmissionSelects {
		capture = &actionAdmissionSelects{subject: map[string]int{}, plan: map[string]int{}}
		return capture
	}
	baseline := begin()
	quote, err := f.s.Estimate(f.key.OrgID, actionAdmissionRequest())
	must(t, err)
	if quote.HoldUSD.String() != "0.864" || len(baseline.subject) != 0 || baseline.plan["video_model_configs"] != 1 || baseline.plan["video_pricing_rules"] != 1 || baseline.plan["model_group_ratios"] != 1 {
		t.Fatal("trusted estimate baseline did not observe actual publication reads")
	}
	estimate := begin()
	quote, err = f.s.EstimateForKey(context.Background(), f.key, actionAdmissionRequest())
	must(t, err)
	wantSubject := map[string]int{"key": 1, "principal": 1, "entitlements": 1}
	if estimate.total != baseline.total+3 || !reflect.DeepEqual(estimate.subject, wantSubject) || !reflect.DeepEqual(estimate.plan, baseline.plan) || quote.HoldUSD.String() != "0.864" {
		t.Fatal("estimate duplicated subject or publication work")
	}
	create := begin()
	first, replay, err := f.s.Create(context.Background(), f.key, f.requestID("count-create"), "action-counted-create", actionAdmissionRequest())
	must(t, err)
	// This text-only fixture has no minimum-rule reads. The original ReserveTx
	// revalidates price once (billing.go ResolvePricingTx) and then requotes in
	// CPU (minimum.go !InputVideo branch). That existing read is required, not
	// an extra membership/publication lookup. Keep its independent fixed count.
	wantCreatePlan := map[string]int{
		"video_model_configs": 1, "video_provider_scopes": 1,
		"video_pricing_rules": 2, "model_group_ratios": 1, "group_ratios": 1,
	}
	if replay || !reflect.DeepEqual(create.subject, wantSubject) || !reflect.DeepEqual(create.plan, wantCreatePlan) {
		t.Fatal("new create changed subject or existing publication/requote reads")
	}
	capture = nil
	before := actionAdmissionFacts(t, f, first.ID)
	replayed := begin()
	again, replay, err := f.s.Create(context.Background(), f.key, f.requestID("count-replay"), "action-counted-create", actionAdmissionRequest())
	must(t, err)
	capture = nil
	remove()
	if !replay || again.ID != first.ID || len(replayed.subject) != 0 || len(replayed.plan) != 0 || actionAdmissionFacts(t, f, first.ID) != before || f.posts.Load() != 0 || f.gets.Load() != 0 {
		t.Fatal("replay performed new authorization/publication work or changed durable acceptance")
	}
}
