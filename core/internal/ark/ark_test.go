package ark

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/laiai/laiai-next/core/internal/crypto"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"go.uber.org/goleak"
)

func openArkTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	ddls := []string{
		`CREATE TABLE organizations (id INTEGER PRIMARY KEY AUTOINCREMENT, slug TEXT, name TEXT, credit_shards INTEGER NOT NULL DEFAULT 1)`,
		`CREATE TABLE ledger_accounts (id INTEGER PRIMARY KEY AUTOINCREMENT, org_id INTEGER NOT NULL, type TEXT NOT NULL, shard INTEGER NOT NULL DEFAULT 0, posted_balance NUMERIC NOT NULL DEFAULT 0, held_amount NUMERIC NOT NULL DEFAULT 0, created_at DATETIME DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME DEFAULT CURRENT_TIMESTAMP, UNIQUE(org_id, type, shard))`,
		`CREATE TABLE ledger_journal (id INTEGER PRIMARY KEY AUTOINCREMENT, idempotency_key TEXT NOT NULL UNIQUE, account_id INTEGER NOT NULL, kind TEXT NOT NULL, delta NUMERIC NOT NULL, balance_after NUMERIC NOT NULL, hold_id INTEGER, ref_type TEXT NOT NULL DEFAULT '', ref_id TEXT NOT NULL DEFAULT '', reason TEXT NOT NULL DEFAULT '', created_at DATETIME DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE ledger_holds (id INTEGER PRIMARY KEY AUTOINCREMENT, idempotency_key TEXT NOT NULL UNIQUE, account_id INTEGER NOT NULL, amount NUMERIC NOT NULL, captured_amount NUMERIC NOT NULL DEFAULT 0, status INTEGER NOT NULL DEFAULT 0, ref_type TEXT NOT NULL DEFAULT '', ref_id TEXT NOT NULL DEFAULT '', expires_at DATETIME, created_at DATETIME DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE ext_settings (name TEXT PRIMARY KEY, value TEXT NOT NULL)`,
		`CREATE TABLE channel_configs (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT, provider_type TEXT, base_url TEXT, api_key_enc TEXT, api_key_mask TEXT, status INTEGER NOT NULL DEFAULT 1)`,
	}
	for _, ddl := range ddls {
		if err := db.Exec(ddl).Error; err != nil {
			t.Fatalf("exec ddl: %v", err)
		}
	}
	if err := EnsureSQLiteTestTables(db); err != nil {
		t.Fatalf("EnsureSQLiteTestTables: %v", err)
	}
	return db
}

func testKeyring(t *testing.T) *crypto.Keyring {
	t.Helper()
	kr, err := crypto.NewKeyring("k1:MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=", "k1")
	if err != nil {
		t.Fatalf("keyring: %v", err)
	}
	return kr
}

func TestEstimateAndBillMatching(t *testing.T) {
	if got := EstimateVideoCostCNY("doubao-seedance-1-0-pro-fast-251015", "succeeded", 1_000_000); !got.Equal(decimal.NewFromFloat(4.2)) {
		t.Fatalf("pro-fast cost = %s, want 4.2", got)
	}
	if got := EstimateVideoCostCNY("doubao-seedance-1-0-pro-250528", "succeeded", 1_000_000); !got.Equal(decimal.NewFromFloat(15)) {
		t.Fatalf("1-0-pro cost = %s, want 15", got)
	}
	if got := EstimateVideoCostCNY("doubao-seedance-2-5-260628", "succeeded", 100_000); !got.Equal(decimal.NewFromFloat(7)) {
		t.Fatalf("2-5 cost = %s, want 7", got)
	}
	if got := EstimateVideoCostCNY("doubao-seedance-2-5-260628", "failed", 100_000); !got.IsZero() {
		t.Fatalf("failed task cost must be 0, got %s", got)
	}

	task := VideoTask{
		Status:      "succeeded",
		TotalTokens: 47311,
		CreatedTime: "2026-08-08 10:02:10", // CST +08:00 -> ceil 5min slot 10:05:00
	}
	bill := SplitBillItem{
		Count:         47.311,
		ExpenseTime:   "2026-08-08 10:05:00",
		PayableAmount: decimal.RequireFromString("2.15"),
	}
	if !BillMatchesVideo(bill, task) {
		t.Fatal("expected exact 1:1 token + 5-min CST slot match to return true")
	}
	badTokenBill := SplitBillItem{
		Count:         48.0,
		ExpenseTime:   "2026-08-08 10:05:00",
		PayableAmount: decimal.RequireFromString("2.15"),
	}
	if BillMatchesVideo(badTokenBill, task) {
		t.Fatal("mismatched token count must not match")
	}
	badTimeBill := SplitBillItem{
		Count:         47.311,
		ExpenseTime:   "2026-08-08 10:20:00",
		PayableAmount: decimal.RequireFromString("2.15"),
	}
	if BillMatchesVideo(badTimeBill, task) {
		t.Fatal("out-of-window expense time must not match")
	}
}

func TestPlanArkWalletAntiPhantomRefund(t *testing.T) {
	// 场景：脏锚点 wallet_charged_quota=1000，而真实流水 netLedger=10，本次 used=6 → 最多退 10-6=4（绝不退 994 造成余额暴增）
	plan := PlanArkWallet(decimal.NewFromInt(6), decimal.NewFromInt(1000), decimal.NewFromInt(10))
	if !plan.ActionCNY.Equal(decimal.NewFromInt(-4)) {
		t.Fatalf("expected capped refund -4, got %s", plan.ActionCNY)
	}
	if !plan.NewAnchor.Equal(decimal.NewFromInt(6)) {
		t.Fatalf("expected new anchor 6, got %s", plan.NewAnchor)
	}

	// 场景：净流水为 0，脏锚点=500，used=20 → 不得退款，且应按实补扣 20
	planNoNet := PlanArkWallet(decimal.NewFromInt(20), decimal.NewFromInt(500), decimal.Zero)
	if !planNoNet.ActionCNY.IsZero() {
		t.Fatalf("dirty anchor 500 > used 20 with netLedger 0 must NOT phantom refund, got %s", planNoNet.ActionCNY)
	}
	if !planNoNet.NewAnchor.Equal(decimal.NewFromInt(20)) {
		t.Fatalf("expected anchor corrected to 20, got %s", planNoNet.NewAnchor)
	}

	// 场景：正常增量扣费 used=25, charged=15, netLedger=15 → 扣 10
	planInc := PlanArkWallet(decimal.NewFromInt(25), decimal.NewFromInt(15), decimal.NewFromInt(15))
	if !planInc.ActionCNY.Equal(decimal.NewFromInt(10)) {
		t.Fatalf("expected +10 consume, got %s", planInc.ActionCNY)
	}
}

func TestRateLimitRetryAndMetaFingerprintReuse(t *testing.T) {
	ignore := goleak.IgnoreCurrent()
	t.Cleanup(func() { goleak.VerifyNone(t, ignore) })
	db := openArkTestDB(t)
	kr := testKeyring(t)

	var contentVersion atomic.Int32
	contentVersion.Store(1)
	var createCalls atomic.Int32
	var hit429 atomic.Int32

	mediaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v := contentVersion.Load()
		if v == 1 {
			w.Header().Set("ETag", `"etag-v1"`)
			w.Header().Set("Content-Range", "bytes 0-0/1024")
		} else {
			w.Header().Set("ETag", `"etag-v2"`)
			w.Header().Set("Content-Range", "bytes 0-0/2048")
		}
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte("a"))
	}))
	defer mediaSrv.Close()

	arkSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		action := r.URL.Query().Get("Action")
		w.Header().Set("Content-Type", "application/json")
		switch action {
		case "CreateAssetGroup":
			_, _ = w.Write([]byte(`{"ResponseMetadata":{"Action":"CreateAssetGroup"},"Result":{"Id":"group-test-1"}}`))
		case "CreateAsset":
			n := createCalls.Add(1)
			if n == 1 {
				hit429.Add(1)
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"ResponseMetadata":{"Error":{"Code":"AccountFlowLimitExceeded","Message":"flow limit"}}}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ResponseMetadata": map[string]any{"Action": "CreateAsset"},
				"Result":           map[string]any{"Id": "asset-" + r.Method + "-" + string(rune('0'+n))},
			})
		case "GetAsset":
			_, _ = w.Write([]byte(`{"ResponseMetadata":{"Action":"GetAsset"},"Result":{"Id":"asset-ok","Status":"Active"}}`))
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer arkSrv.Close()

	skEnc, _ := kr.Encrypt("test-sk")
	saveExtSetting(db, "plugin_cfg:asset_manager:volc_access_key", "test-ak")
	saveExtSetting(db, "plugin_cfg:asset_manager:volc_secret_key", skEnc)

	makeBody := func() map[string]any {
		return map[string]any{
			"content": []any{
				map[string]any{"type": "image_url", "image_url": map[string]any{"url": mediaSrv.URL + "/char.png"}},
			},
		}
	}

	opts := ConvertRequestOptions{
		OrgID:             1,
		UserID:            1,
		AssetConvert:      true,
		AssetConvertNS:    PluginAssetManager,
		AllowPrivateFetch: true,
		PollInterval:      5 * time.Millisecond,
		OpenAIOverride:    arkSrv.URL,
	}

	// 第 1 次：触发 429 自动重试并成功转成 asset://
	body1 := makeBody()
	logs1, err := ConvertBodyAssets(context.Background(), db, mediaSrv.Client(), kr, opts, body1)
	if err != nil {
		t.Fatalf("first ConvertBodyAssets: %v", err)
	}
	if hit429.Load() != 1 || createCalls.Load() != 2 || len(logs1) != 1 {
		t.Fatalf("unexpected retry counters hit429=%d createCalls=%d logs=%v", hit429.Load(), createCalls.Load(), logs1)
	}

	// 第 2 次：ETag 未变，必须命中 L1 指纹缓存，不再调 CreateAsset
	body2 := makeBody()
	logs2, err := ConvertBodyAssets(context.Background(), db, mediaSrv.Client(), kr, opts, body2)
	if err != nil {
		t.Fatalf("second ConvertBodyAssets: %v", err)
	}
	if createCalls.Load() != 2 {
		t.Fatalf("expected cache hit without CreateAsset, createCalls=%d logs=%v", createCalls.Load(), logs2)
	}

	// 第 3 次：同 URL 内容变化（ETag v2）-> L1 未命中且跳过 L2 URL -> 重新 CreateAsset
	contentVersion.Store(2)
	body3 := makeBody()
	_, err = ConvertBodyAssets(context.Background(), db, mediaSrv.Client(), kr, opts, body3)
	if err != nil {
		t.Fatalf("third ConvertBodyAssets: %v", err)
	}
	if createCalls.Load() != 3 {
		t.Fatalf("changed content at same URL must bypass L2 URL cache and recreate asset, createCalls=%d", createCalls.Load())
	}
}

func TestSyncAllAccountsWalletFuseRecoveryAndZeroDrift(t *testing.T) {
	db := openArkTestDB(t)
	kr := testKeyring(t)

	db.Exec(`INSERT INTO organizations (id, slug, name, credit_shards) VALUES (1, 'org1', 'Org1', 1)`)
	acc, err := ledger.EnsureAccount(db, 1, ledger.AccountUserCredit)
	if err != nil {
		t.Fatalf("EnsureAccount: %v", err)
	}
	// 初始充值 $1 USD（约 ¥7.3）
	if _, _, err := ledger.Post(db, ledger.PostInput{
		IdempotencyKey: "init-topup-1",
		AccountID:      acc.ID,
		Kind:           ledger.KindCredit,
		Delta:          decimal.NewFromInt(1),
		RefType:        "topup",
		RefID:          "ord-1",
	}); err != nil {
		t.Fatalf("topup: %v", err)
	}

	skEnc, _ := kr.Encrypt("sk-test")
	epKeyEnc, _ := kr.Encrypt("ep-bearer-1")
	arkAcc := Account{Name: "MainVolc", AccessKey: "ak-test", SecretKeyEnc: skEnc, Region: "cn-beijing"}
	if err := db.Create(&arkAcc).Error; err != nil {
		t.Fatalf("create account: %v", err)
	}
	binding := EndpointBinding{
		AccountID: arkAcc.ID, EndpointID: "ep-1234", OrgID: 1, UserID: 1, UserUID: "U1",
		APIKeyEnc: epKeyEnc, Status: 1,
	}
	if err := db.Create(&binding).Error; err != nil {
		t.Fatalf("create binding: %v", err)
	}

	var stopCalls, startCalls atomic.Int32
	mockArk := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v3/contents/generations/tasks" {
			_, _ = w.Write([]byte(`{
				"items": [{
					"id": "cgt-sync-1",
					"model": "doubao-seedance-1-0-pro-fast-251015",
					"status": "succeeded",
					"duration": 5,
					"resolution": "720p",
					"created_at": "2026-08-08 10:02:10",
					"usage": {"total_tokens": 2000000}
				}]
			}`))
			return
		}
		switch r.URL.Query().Get("Action") {
		case "ListSplitBillDetail":
			// 2000 个千 token * 单价 -> 出账 ¥9.40（大于初始 $1=¥7.3，将把钱包扣成负数并触发 StopEndpoint 熔断）
			_, _ = w.Write([]byte(`{
				"ResponseMetadata":{"Action":"ListSplitBillDetail"},
				"Result":{"Total":1,"List":[{"BillPeriod":"2026-08","ExpenseTime":"2026-08-08 10:05:00","Count":"2000","PayableAmount":"9.40"}]}
			}`))
		case "StopEndpoint":
			stopCalls.Add(1)
			_, _ = w.Write([]byte(`{"ResponseMetadata":{"Action":"StopEndpoint"},"Result":{}}`))
		case "StartEndpoint":
			startCalls.Add(1)
			_, _ = w.Write([]byte(`{"ResponseMetadata":{"Action":"StartEndpoint"},"Result":{}}`))
		}
	}))
	defer mockArk.Close()

	// 第 1 次同步：拉取 cgt-sync-1、精确匹配分账 ¥9.40、扣减 $1.28767123、触发 StopEndpoint
	sum1, err := SyncAllAccounts(context.Background(), db, kr, decimal.NewFromFloat(7.3), mockArk.Client(), mockArk.URL)
	if err != nil {
		t.Fatalf("SyncAllAccounts #1: %v", err)
	}
	if sum1.TasksUpserted != 1 || sum1.BillsReconciled != 1 || sum1.EndpointsFused != 1 || stopCalls.Load() != 1 {
		t.Fatalf("unexpected sync summary #1: %+v stopCalls=%d", sum1, stopCalls.Load())
	}

	var gotBind EndpointBinding
	_ = db.First(&gotBind, binding.ID).Error
	if gotBind.Status != 0 || gotBind.FuseReason != FuseReasonWallet || !gotBind.UsedQuota.Equal(decimal.RequireFromString("9.4")) {
		t.Fatalf("binding state after fuse = %+v", gotBind)
	}

	// 用户充值 $5 USD 恢复正余额 -> 第 2 次同步应自动 StartEndpoint 恢复绑定
	if _, _, err := ledger.Post(db, ledger.PostInput{
		IdempotencyKey: "recover-topup-2",
		AccountID:      acc.ID,
		Kind:           ledger.KindCredit,
		Delta:          decimal.NewFromInt(5),
		RefType:        "topup",
		RefID:          "ord-2",
	}); err != nil {
		t.Fatalf("recover topup: %v", err)
	}
	sum2, err := SyncAllAccounts(context.Background(), db, kr, decimal.NewFromFloat(7.3), mockArk.Client(), mockArk.URL)
	if err != nil {
		t.Fatalf("SyncAllAccounts #2: %v", err)
	}
	if sum2.EndpointsRecovered != 1 || startCalls.Load() != 1 {
		t.Fatalf("expected endpoint auto recovery, summary=%+v startCalls=%d", sum2, startCalls.Load())
	}

	drifts, err := ledger.CheckDrift(db)
	if err != nil || len(drifts) != 0 {
		t.Fatalf("ledger drift detected: drifts=%+v err=%v", drifts, err)
	}
}
