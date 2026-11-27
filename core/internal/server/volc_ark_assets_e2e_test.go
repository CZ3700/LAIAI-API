package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/shopspring/decimal"
)

func TestArkMonitorRoutesReconcileFuseAndDrift(t *testing.T) {
	f, db, _, userToken, _ := setupStage2Server(t)
	if err := ark.EnsureSQLiteTestTables(db); err != nil {
		t.Fatalf("EnsureSQLiteTestTables: %v", err)
	}

	// Setup org with modest balance ($1 USD = ¥7.3) so bill ¥9.40 causes wallet fuse.
	f.do(t, "POST", "/api/auth/register", "", map[string]string{"email": "arkfuse@test.com", "password": "longenough1"})
	_, loginFuse := f.do(t, "POST", "/api/auth/login", "", map[string]string{"email": "arkfuse@test.com", "password": "longenough1"})
	fuseToken := loginFuse["token"].(string)
	fuseOrgID := uint64(loginFuse["org_id"].(float64))
	if w, _ := f.doAdmin(t, "POST", "/api/admin/credits", map[string]any{
		"org_id": fuseOrgID, "amount_usd": "1", "reason": "ark fuse seed", "idempotency_key": "ark-fuse-credit-1",
	}); w.Code != http.StatusOK {
		t.Fatalf("credit fuse org failed: %d", w.Code)
	}

	var stopCalls, startCalls atomic.Int32
	mockArk := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v3/contents/generations/tasks" {
			_, _ = w.Write([]byte(`{
				"items": [{
					"id": "cgt-e2e-1",
					"model": "doubao-seedance-2-5-260628",
					"status": "succeeded",
					"duration": 5,
					"resolution": "720p",
					"created_at": "2026-08-08 11:02:00",
					"usage": {"total_tokens": 100000}
				}]
			}`))
			return
		}
		switch r.URL.Query().Get("Action") {
		case "QueryBalanceAcct":
			_, _ = w.Write([]byte(`{"ResponseMetadata":{"Action":"QueryBalanceAcct"},"Result":{"AccountID":"2100001","AvailableBalance":"888.50","CashBalance":"888.50"}}`))
		case "ListSplitBillDetail":
			_, _ = w.Write([]byte(`{
				"ResponseMetadata":{"Action":"ListSplitBillDetail"},
				"Result":{"Total":1,"List":[{"BillPeriod":"2026-08","ExpenseTime":"2026-08-08 11:05:00","Count":"100","PayableAmount":"9.40"}]}
			}`))
		case "StopEndpoint":
			stopCalls.Add(1)
			_, _ = w.Write([]byte(`{"ResponseMetadata":{"Action":"StopEndpoint"},"Result":{}}`))
		case "StartEndpoint":
			startCalls.Add(1)
			_, _ = w.Write([]byte(`{"ResponseMetadata":{"Action":"StartEndpoint"},"Result":{}}`))
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer mockArk.Close()
	f.srv.SetArkTestHooks(mockArk.Client(), mockArk.URL, 5*time.Millisecond)

	// 1. Admin creates Ark account & queries live balance
	wAcc := adminDo(t, f, http.MethodPost, "/api/v1/plugins/volcengine_ark_monitor/accounts", map[string]any{
		"name": "E2E-Ark-Main", "access_key": "AK-TEST-1", "secret_key": "SK-TEST-1", "region": "cn-beijing",
	}, true)
	if wAcc.Code != http.StatusCreated {
		t.Fatalf("create ark account: %d %s", wAcc.Code, wAcc.Body.String())
	}
	var accResp struct {
		Data ark.Account `json:"data"`
	}
	_ = json.Unmarshal(wAcc.Body.Bytes(), &accResp)

	wBal := adminDo(t, f, http.MethodPost, "/api/v1/plugins/volcengine_ark_monitor/accounts/"+strconv.FormatUint(accResp.Data.ID, 10)+"/balance", nil, true)
	if wBal.Code != http.StatusOK || !strings.Contains(wBal.Body.String(), "888.5") {
		t.Fatalf("balance query failed: %d %s", wBal.Code, wBal.Body.String())
	}

	// 2. Create endpoint binding for fuseOrgID -> Sync triggers StopEndpoint fuse
	wBind := adminDo(t, f, http.MethodPost, "/api/v1/plugins/volcengine_ark_monitor/bindings", map[string]any{
		"account_id": accResp.Data.ID, "endpoint_id": "ep-e2e-99", "org_id": fuseOrgID, "api_key": "ark-ep-key-99",
	}, true)
	if wBind.Code != http.StatusCreated {
		t.Fatalf("create binding: %d %s", wBind.Code, wBind.Body.String())
	}
	var bindResp struct {
		Data ark.EndpointBinding `json:"data"`
	}
	_ = json.Unmarshal(wBind.Body.Bytes(), &bindResp)
	bindIDStr := strconv.FormatUint(bindResp.Data.ID, 10)

	wSync := adminDo(t, f, http.MethodPost, "/api/v1/plugins/volcengine_ark_monitor/sync", nil, true)
	if wSync.Code != http.StatusOK || stopCalls.Load() != 1 {
		t.Fatalf("sync expected 200 & stopCalls=1, got %d body=%s stopCalls=%d", wSync.Code, wSync.Body.String(), stopCalls.Load())
	}

	// Zero-balance guard: admin toggle ON while negative balance must be rejected with 400
	wToggleBad := adminDo(t, f, http.MethodPost, "/api/v1/plugins/volcengine_ark_monitor/bindings/"+bindIDStr+"/toggle", map[string]any{"status": 1}, true)
	if wToggleBad.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 enabling binding with depleted balance, got %d", wToggleBad.Code)
	}

	// Check user overview shows bound endpoint & confirmed task (is_estimated = false)
	wOv, _ := f.do(t, http.MethodGet, "/api/v1/ark-monitor/overview", fuseToken, nil)
	if wOv.Code != http.StatusOK || !strings.Contains(wOv.Body.String(), "cgt-e2e-1") {
		t.Fatalf("user ark-monitor/overview unexpected: %d %s", wOv.Code, wOv.Body.String())
	}

	// Check finance recharges lists ark_video_consume while /v1/user/wallet excludes it from recharge
	wFin := adminDo(t, f, http.MethodGet, "/api/v1/finance/recharges?recharge_type=ark_video_consume", nil, true)
	if wFin.Code != http.StatusOK || !strings.Contains(wFin.Body.String(), "ark_video_consume") {
		t.Fatalf("finance recharges missing ark_video_consume: %d %s", wFin.Code, wFin.Body.String())
	}

	// Rebinding org isolates old ledger via wallet_ledger_after without resetting used_quota/wallet_charged_quota
	wMe, meOut := f.do(t, http.MethodGet, "/api/me", userToken, nil)
	if wMe.Code != http.StatusOK {
		t.Fatalf("me: %d", wMe.Code)
	}
	orgID1 := uint64(meOut["org_id"].(float64))
	wRebind := adminDo(t, f, http.MethodPut, "/api/v1/plugins/volcengine_ark_monitor/bindings/"+bindIDStr, map[string]any{
		"org_id": orgID1,
	}, true)
	if wRebind.Code != http.StatusOK {
		t.Fatalf("rebind failed: %d %s", wRebind.Code, wRebind.Body.String())
	}
	var rebound ark.EndpointBinding
	_ = db.First(&rebound, bindResp.Data.ID).Error
	if rebound.WalletLedgerAfter == nil || !rebound.UsedQuota.Equal(decimal.RequireFromString("9.4")) || !rebound.WalletChargedQuota.Equal(decimal.RequireFromString("9.4")) {
		t.Fatalf("rebind must set wallet_ledger_after and keep used/charged quotas intact: %+v", rebound)
	}

	// Ledger check drift must remain 0
	drifts, err := ledger.CheckDrift(db)
	if err != nil || len(drifts) != 0 {
		t.Fatalf("expected 0 ledger drift, got %+v err=%v", drifts, err)
	}
}

func TestAssetConvertAndUpstreamRelayE2E(t *testing.T) {
	f, db, ts, userTokenA, apiKeyA := setupStage2Server(t)
	if err := ark.EnsureSQLiteTestTables(db); err != nil {
		t.Fatalf("EnsureSQLiteTestTables: %v", err)
	}

	// Second user/org for multi-tenant isolation test
	f.do(t, "POST", "/api/auth/register", "", map[string]string{"email": "asset-userB@test.com", "password": "longenough1"})
	_, loginB := f.do(t, "POST", "/api/auth/login", "", map[string]string{"email": "asset-userB@test.com", "password": "longenough1"})
	tokenB := loginB["token"].(string)
	orgB := uint64(loginB["org_id"].(float64))
	f.doAdmin(t, "POST", "/api/admin/credits", map[string]any{"org_id": orgB, "amount_usd": "50", "reason": "b seed", "idempotency_key": "asset-b-credit"})
	_, keyOutB := f.do(t, "POST", "/api/keys", tokenB, map[string]any{"name": "b-key"})
	apiKeyB := keyOutB["key"].(string)

	_ = userTokenA
	var mu sync.Mutex
	var submittedBodies []map[string]any
	var assetCreateCalls atomic.Int32

	mockSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/media/hero.png" {
			w.Header().Set("ETag", `"hero-v1"`)
			w.Header().Set("Content-Range", "bytes 0-0/512")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write([]byte("x"))
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var bodyMap map[string]any
		_ = json.Unmarshal(raw, &bodyMap)
		w.Header().Set("Content-Type", "application/json")

		action := r.URL.Query().Get("Action")
		switch action {
		case "CreateAssetGroup":
			_, _ = w.Write([]byte(`{"ResponseMetadata":{"Action":"CreateAssetGroup"},"Result":{"Id":"grp-e2e-1"}}`))
			return
		case "CreateAsset":
			n := assetCreateCalls.Add(1)
			_, _ = w.Write([]byte(`{"ResponseMetadata":{"Action":"CreateAsset"},"Result":{"Id":"asset-e2e-` + strconv.Itoa(int(n)) + `"}}`))
			return
		case "GetAsset":
			_, _ = w.Write([]byte(`{"ResponseMetadata":{"Action":"GetAsset"},"Result":{"Id":"asset-e2e-1","Status":"Active"}}`))
			return
		}

		if r.URL.Path == "/api/v3/contents/generations/tasks" && r.Method == http.MethodPost {
			mu.Lock()
			submittedBodies = append(submittedBodies, bodyMap)
			mu.Unlock()
			_, _ = w.Write([]byte(`{"id":"up-seedance-asset-1","status":"queued"}`))
			return
		}
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v3/contents/generations/tasks/") {
			_, _ = w.Write([]byte(`{"id":"up-seedance-asset-1","status":"succeeded","content":{"video_url":"https://cdn.example.com/res.mp4"},"usage":{"completion_tokens":1000}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mockSrv.Close()
	ts.Client = mockSrv.Client()
	f.srv.SetArkTestHooks(mockSrv.Client(), mockSrv.URL, 5*time.Millisecond)

	// 1. Configure asset_manager credentials via admin API & test multi-tenant isolation on POST /api
	wCfg := adminDo(t, f, http.MethodPut, "/api/v1/assets/admin/config", map[string]any{
		"ns": "asset_manager", "volc_access_key": "AK-ASM", "volc_secret_key": "SK-ASM", "volc_project_name": "default", "volc_region": "cn-beijing",
	}, true)
	if wCfg.Code != http.StatusOK {
		t.Fatalf("put asset config: %d %s", wCfg.Code, wCfg.Body.String())
	}

	wGrpA := doAPIKeyReq(t, f, http.MethodPost, "/api?Action=CreateAssetGroup&Version=2024-01-01", "Authorization", "Bearer "+apiKeyA, map[string]any{
		"Name": "userA-group", "Description": "isolation test",
	})
	if wGrpA.Code != http.StatusOK {
		t.Fatalf("CreateAssetGroup failed: %d %s", wGrpA.Code, wGrpA.Body.String())
	}
	wAssetA := doAPIKeyReq(t, f, http.MethodPost, "/api?Action=CreateAsset&Version=2024-01-01", "Authorization", "Bearer "+apiKeyA, map[string]any{
		"GroupId": "grp-e2e-1", "URL": mockSrv.URL + "/media/hero.png", "AssetType": "Image",
	})
	if wAssetA.Code != http.StatusOK {
		t.Fatalf("CreateAsset failed: %d %s", wAssetA.Code, wAssetA.Body.String())
	}
	// User B must be forbidden from getting User A's asset via /api
	wForbidB := doAPIKeyReq(t, f, http.MethodPost, "/api?Action=GetAsset&Version=2024-01-01", "Authorization", "Bearer "+apiKeyB, map[string]any{
		"Id": "asset-e2e-1",
	})
	if wForbidB.Code != http.StatusForbidden {
		t.Fatalf("expected 403 tenant isolation for user B, got %d %s", wForbidB.Code, wForbidB.Body.String())
	}

	// 2. Upstream Asset Relay: channel_config -> binding -> /v1/plugin/:rid/assets -> one-click forward rule -> video submission
	wChCfg := adminDo(t, f, http.MethodPost, "/api/v1/channel-configs", map[string]any{
		"name": "UpstreamArkRelayCfg", "provider_type": "volcengine", "base_url": mockSrv.URL, "api_key": "sk-up-bearer-1", "status": 1,
	}, true)
	if wChCfg.Code != http.StatusCreated {
		t.Fatalf("create channel-config: %d %s", wChCfg.Code, wChCfg.Body.String())
	}
	var chCfgResp struct {
		Data struct {
			ID uint64 `json:"id"`
		} `json:"data"`
	}
	_ = json.Unmarshal(wChCfg.Body.Bytes(), &chCfgResp)

	wUarBind := adminDo(t, f, http.MethodPost, "/api/v1/plugins/upstream_asset_relay/bindings", map[string]any{
		"name": "UarMain", "source_type": "channel", "channel_config_id": chCfgResp.Data.ID,
	}, true)
	if wUarBind.Code != http.StatusCreated {
		t.Fatalf("create uar binding: %d %s", wUarBind.Code, wUarBind.Body.String())
	}
	var uarResp struct {
		Data ark.UpstreamAssetBinding `json:"data"`
	}
	_ = json.Unmarshal(wUarBind.Body.Bytes(), &uarResp)

	bindingPath := "/v1/plugin/" + uarResp.Data.RID + "/assets?Action="
	unsupported := doAPIKeyReq(t, f, http.MethodPost, bindingPath+"UnsupportedAction", "Authorization", "Bearer "+apiKeyA, map[string]any{})
	if unsupported.Code != http.StatusBadRequest {
		t.Fatalf("unsupported binding action = %d: %s", unsupported.Code, unsupported.Body)
	}
	unowned := doAPIKeyReq(t, f, http.MethodPost, bindingPath+"DeleteAsset", "Authorization", "Bearer "+apiKeyA, map[string]any{"Id": "unowned"})
	if unowned.Code != http.StatusForbidden {
		t.Fatalf("unowned binding asset delete = %d: %s", unowned.Code, unowned.Body)
	}
	// Test direct proxy endpoint POST /v1/plugin/:rid/assets
	wProxy := doAPIKeyReq(t, f, http.MethodPost, "/v1/plugin/"+uarResp.Data.RID+"/assets?Action=CreateAsset&Version=2024-01-01", "Authorization", "Bearer "+apiKeyA, map[string]any{
		"URL": mockSrv.URL + "/media/hero.png", "AssetType": "Image",
	})
	if wProxy.Code != http.StatusOK || !strings.Contains(wProxy.Body.String(), "asset-e2e-") {
		t.Fatalf("upstream_asset_proxy expected 200 with asset ID, got %d %s", wProxy.Code, wProxy.Body.String())
	}

	var createdAsset map[string]any
	if err := json.Unmarshal(wProxy.Body.Bytes(), &createdAsset); err != nil {
		t.Fatal(err)
	}
	otherOrgDelete := doAPIKeyReq(t, f, http.MethodPost, bindingPath+"DeleteAsset", "Authorization", "Bearer "+apiKeyB,
		map[string]any{"Id": ark.ExtractResultField(createdAsset, "Id")})
	if otherOrgDelete.Code != http.StatusForbidden {
		t.Fatalf("cross-organization binding asset delete = %d: %s", otherOrgDelete.Code, otherOrgDelete.Body)
	}

	// Generate forward rule from binding
	wGen := adminDo(t, f, http.MethodPost, "/api/v1/plugins/upstream_asset_relay/bindings/"+strconv.FormatUint(uarResp.Data.ID, 10)+"/generate-rule", nil, true)
	if wGen.Code != http.StatusCreated && wGen.Code != http.StatusOK {
		t.Fatalf("generate-rule failed: %d %s", wGen.Code, wGen.Body.String())
	}
	var genResp struct {
		Data struct {
			ID         uint64 `json:"id"`
			ConfigJSON string `json:"config_json"`
		} `json:"data"`
	}
	_ = json.Unmarshal(wGen.Body.Bytes(), &genResp)
	if !strings.Contains(genResp.Data.ConfigJSON, `"upstream_asset_convert":true`) {
		t.Fatalf("generated forward rule missing upstream_asset_convert: %s", genResp.Data.ConfigJSON)
	}

	// Bind channel + billing + generated rule to doubao-seedance-asset-test
	createRuleAndBind(t, f, "doubao-seedance-asset-test", "AssetConvertRule", "volcengine", genResp.Data.ConfigJSON, "AssetConvertBill", "2.0")
	wCh, outCh := f.doAdmin(t, "POST", "/api/admin/channels", map[string]any{
		"name": "seedance-asset-ch", "provider": "volcengine", "base_url": mockSrv.URL,
		"api_key": "ark-ch-key", "models": []string{"doubao-seedance-asset-test"}, "weight": 1, "priority": 10,
	})
	if wCh.Code != http.StatusCreated {
		t.Fatalf("create channel: %d %v", wCh.Code, outCh)
	}

	// First video submission: converts media/hero.png -> asset://asset-e2e-...
	beforeCalls := assetCreateCalls.Load()
	wVid1 := doAPIKeyReq(t, f, http.MethodPost, "/v1/video/generations", "Authorization", "Bearer "+apiKeyA, map[string]any{
		"model":  "doubao-seedance-asset-test",
		"prompt": "hero walks",
		"images": []string{mockSrv.URL + "/media/hero.png"},
	})
	if wVid1.Code != http.StatusAccepted {
		t.Fatalf("video submit #1 expected 202, got %d %s", wVid1.Code, wVid1.Body.String())
	}
	// Second video submission with same image: hits L1 meta_fingerprint cache -> assetCreateCalls unchanged!
	wVid2 := doAPIKeyReq(t, f, http.MethodPost, "/v1/video/generations", "Authorization", "Bearer "+apiKeyA, map[string]any{
		"model":  "doubao-seedance-asset-test",
		"prompt": "hero runs",
		"images": []string{mockSrv.URL + "/media/hero.png"},
	})
	if wVid2.Code != http.StatusAccepted {
		t.Fatalf("video submit #2 expected 202, got %d %s", wVid2.Code, wVid2.Body.String())
	}
	if assetCreateCalls.Load() != beforeCalls+1 {
		t.Fatalf("expected 1 CreateAsset call then cache hit, got before=%d after=%d", beforeCalls, assetCreateCalls.Load())
	}

	mu.Lock()
	defer mu.Unlock()
	if len(submittedBodies) != 2 {
		t.Fatalf("expected 2 upstream submissions, got %d", len(submittedBodies))
	}
	rawFirst, _ := json.Marshal(submittedBodies[0])
	if !strings.Contains(string(rawFirst), "asset://asset-e2e-") {
		t.Fatalf("upstream payload must contain converted asset:// URI, got %s", string(rawFirst))
	}
}
