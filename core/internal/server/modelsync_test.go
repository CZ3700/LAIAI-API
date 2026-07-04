package server

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/laiai/laiai-next/core/internal/relay"
)

// computeModelDiff 纯函数：missing 排除已配 + 已忽略；extra 为渠道有上游无；unpriced 为上游有未定价。
func TestComputeModelDiff(t *testing.T) {
	diff := computeModelDiff(
		[]string{"gpt-4", "gpt-5", "o3", "legacy-ignored"},
		csvSet("gpt-4,dropped-model"), // configured
		csvSet("gpt-4,gpt-5"),         // priced
		csvSet("legacy-ignored"),      // ignored
		nil,                           // 无映射
	)
	// missing = 上游有 − 已配 − 已忽略 = {gpt-5, o3}
	if got := strings.Join(diff.Missing, ","); got != "gpt-5,o3" {
		t.Fatalf("missing = %q, want gpt-5,o3", got)
	}
	// extra = 已配 − 上游 = {dropped-model}
	if got := strings.Join(diff.Extra, ","); got != "dropped-model" {
		t.Fatalf("extra = %q, want dropped-model", got)
	}
	// unpriced = 上游 − 已定价 = {legacy-ignored, o3}（忽略不影响定价维度）
	if got := strings.Join(diff.Unpriced, ","); got != "legacy-ignored,o3" {
		t.Fatalf("unpriced = %q, want legacy-ignored,o3", got)
	}
}

// 映射渠道名字轴对齐（对抗 review CONFIRMED 回归）：channels.models 存对外名 gpt-4o、
// mapping gpt-4o→gpt-4o-2024-08-06、上游返回 gpt-4o-2024-08-06。不得误报 missing/extra。
func TestComputeModelDiffWithMapping(t *testing.T) {
	diff := computeModelDiff(
		[]string{"gpt-4o-2024-08-06", "gpt-5"}, // upstream 原名
		csvSet("gpt-4o"),                       // configured 对外名
		csvSet("gpt-4o"),                       // priced（对外名）
		nil,                                    // ignored
		map[string]string{"gpt-4o": "gpt-4o-2024-08-06"}, // mapping 对外→上游
	)
	// gpt-4o-2024-08-06 已经通过映射服务 → 不算 missing；只有真正新的 gpt-5 是 missing
	if got := strings.Join(diff.Missing, ","); got != "gpt-5" {
		t.Fatalf("mapped missing = %q, want only gpt-5 (mapped model must not be false-missing)", got)
	}
	// gpt-4o（对外名）的上游 gpt-4o-2024-08-06 仍在上游 → 不算 extra（不能建议下架在用模型）
	if len(diff.Extra) != 0 {
		t.Fatalf("mapped extra = %v, want empty (live mapped model must not be flagged removed)", diff.Extra)
	}
}

// modelSyncUpstream：返回固定模型清单的上游 /models mock。
func modelSyncUpstream(t *testing.T, models ...string) *httptest.Server {
	t.Helper()
	ids := make([]string, len(models))
	for i, m := range models {
		ids[i] = `{"id":"` + m + `"}`
	}
	body := `{"data":[` + strings.Join(ids, ",") + `]}`
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(body))
			return
		}
		w.WriteHeader(404)
	}))
}

// apply-models：把选中上游新模型追加进 channels.models（可路由），且绝不写 model_prices。
func TestAdminApplyChannelModels(t *testing.T) {
	srv, db := setupRelayServer(t)
	cred, _ := testKeyring(t).Encrypt("sk-test")
	ch := relay.Channel{Name: "c1", Provider: "openai", BaseURL: "http://x",
		CredentialEnc: cred, Models: "gpt-4", DetectedModels: "gpt-5,o3", Status: 1}
	if e := db.Create(&ch).Error; e != nil {
		t.Fatal(e)
	}
	id := strconv.FormatUint(ch.ID, 10)

	w, out := srv.doAdmin(t, "POST", "/api/admin/channels/"+id+"/apply-models",
		map[string]any{"models": []string{"gpt-5"}})
	if w.Code != 200 {
		t.Fatalf("apply = %d %v", w.Code, out)
	}
	// models 追加 gpt-5（排序后 gpt-4,gpt-5）
	if out["models"] != "gpt-4,gpt-5" {
		t.Fatalf("models = %v, want gpt-4,gpt-5", out["models"])
	}
	var after relay.Channel
	db.First(&after, ch.ID)
	if after.Models != "gpt-4,gpt-5" {
		t.Fatalf("db models = %q", after.Models)
	}
	// 已 apply 的从 detected 徽标剔除（剩 o3）
	if after.DetectedModels != "o3" {
		t.Fatalf("detected after apply = %q, want o3", after.DetectedModels)
	}
	// 红线：apply 绝不写 model_prices —— gpt-5 仍未定价
	var priceCount int64
	db.Raw(`SELECT COUNT(*) FROM model_prices WHERE model = ?`, "gpt-5").Scan(&priceCount)
	if priceCount != 0 {
		t.Fatalf("apply auto-priced gpt-5 (count=%d) — violates 手动定价红线", priceCount)
	}
}

// mark-removed：把上游已下架模型从 channels.models 删除。
func TestAdminMarkChannelModelsRemoved(t *testing.T) {
	srv, db := setupRelayServer(t)
	cred, _ := testKeyring(t).Encrypt("sk-test")
	ch := relay.Channel{Name: "c1", Provider: "openai", BaseURL: "http://x",
		CredentialEnc: cred, Models: "gpt-4,gpt-3.5,dead-model", Status: 1}
	db.Create(&ch)
	id := strconv.FormatUint(ch.ID, 10)

	w, out := srv.doAdmin(t, "POST", "/api/admin/channels/"+id+"/mark-removed",
		map[string]any{"models": []string{"dead-model"}})
	if w.Code != 200 {
		t.Fatalf("mark-removed = %d %v", w.Code, out)
	}
	if out["models"] != "gpt-3.5,gpt-4" {
		t.Fatalf("models = %v, want gpt-3.5,gpt-4", out["models"])
	}
}

// ignore-models：加入忽略清单，后续 detect（fetch-models / 后台 job）不再提示。
func TestAdminIgnoreChannelModels(t *testing.T) {
	srv, db := setupRelayServer(t)
	up := modelSyncUpstream(t, "gpt-4", "gpt-5", "spam-model")
	defer up.Close()
	cred, _ := testKeyring(t).Encrypt("sk-test")
	ch := relay.Channel{Name: "c1", Provider: "openai", BaseURL: up.URL,
		CredentialEnc: cred, Models: "gpt-4", Status: 1}
	db.Create(&ch)
	id := strconv.FormatUint(ch.ID, 10)

	// 忽略 spam-model
	w, out := srv.doAdmin(t, "POST", "/api/admin/channels/"+id+"/ignore-models",
		map[string]any{"models": []string{"spam-model"}})
	if w.Code != 200 {
		t.Fatalf("ignore = %d %v", w.Code, out)
	}
	if out["ignored_models"] != "spam-model" {
		t.Fatalf("ignored = %v", out["ignored_models"])
	}
	// fetch-models 现在 missing 只剩 gpt-5（spam-model 已忽略）
	_, fm := srv.doAdmin(t, "GET", "/api/admin/channels/"+id+"/fetch-models", nil)
	missing := toStrSet(fm["missing_in_config"])
	if !missing["gpt-5"] || missing["spam-model"] {
		t.Fatalf("missing = %v, want only gpt-5 (spam-model ignored)", fm["missing_in_config"])
	}
}

// fetch-models 对映射渠道不得误报（对抗 review CONFIRMED 端到端回归）：
// channels.models=对外名 gpt-4o，mapping gpt-4o→gpt-4o-2024-08-06，上游返回上游原名。
// 不能把在用的映射模型报成 missing（诱导重复上货）或 extra（诱导下架在用模型）。
func TestFetchModelsRespectsMapping(t *testing.T) {
	srv, db := setupRelayServer(t)
	up := modelSyncUpstream(t, "gpt-4o-2024-08-06", "gpt-5")
	defer up.Close()
	cred, _ := testKeyring(t).Encrypt("sk-test")
	ch := relay.Channel{Name: "mapped", Provider: "openai", BaseURL: up.URL,
		CredentialEnc: cred, Models: "gpt-4o",
		ModelMapping: `{"gpt-4o":"gpt-4o-2024-08-06"}`, Status: 1}
	db.Create(&ch)
	id := strconv.FormatUint(ch.ID, 10)

	_, out := srv.doAdmin(t, "GET", "/api/admin/channels/"+id+"/fetch-models", nil)
	if out["ok"] != true {
		t.Fatalf("fetch-models not ok: %v", out)
	}
	missing := toStrSet(out["missing_in_config"])
	// 只有真新的 gpt-5 是 missing；映射服务中的 gpt-4o-2024-08-06 不得出现
	if !missing["gpt-5"] || missing["gpt-4o-2024-08-06"] {
		t.Fatalf("missing = %v, want only gpt-5 (mapped upstream name must not appear)", out["missing_in_config"])
	}
	// extra 必须空：不能建议下架经映射在用的 gpt-4o
	if extra := out["extra_in_config"]; extra != nil {
		if arr, ok := extra.([]any); ok && len(arr) > 0 {
			t.Fatalf("extra = %v, want empty (must not advise removing live mapped model)", extra)
		}
	}
}

// 后台 job：opt-in 渠道自动检测新上游模型 → detected_models 徽标；绝不改 channels.models。
func TestSyncChannelModelsJob(t *testing.T) {
	srv, db := setupRelayServer(t)
	// 后台 job 强制 SSRF 守卫拨号（红线5）；httptest 走 127.0.0.1，须开 allow_private 才可探测。
	db.Exec("INSERT INTO ext_settings (name, value) VALUES ('advanced_custom_allow_private', 'true')")
	up := modelSyncUpstream(t, "gpt-4", "gpt-5", "o3")
	defer up.Close()
	cred, _ := testKeyring(t).Encrypt("sk-test")
	// 开启同步的渠道
	on := relay.Channel{Name: "on", Provider: "openai", BaseURL: up.URL,
		CredentialEnc: cred, Models: "gpt-4", ModelSyncEnabled: 1, Status: 1}
	db.Create(&on)
	// 未开启的渠道：不应被检测
	off := relay.Channel{Name: "off", Provider: "openai", BaseURL: up.URL,
		CredentialEnc: cred, Models: "gpt-4", ModelSyncEnabled: 0, Status: 1}
	db.Create(&off)

	checked, withNew := srv.srv.SyncChannelModels(t.Context(), 100)
	if checked != 1 || withNew != 1 {
		t.Fatalf("sync checked=%d withNew=%d, want 1/1 (only opt-in channel)", checked, withNew)
	}
	var onAfter, offAfter relay.Channel
	db.First(&onAfter, on.ID)
	db.First(&offAfter, off.ID)
	// on：detected = 上游 − 已配 = {gpt-5, o3}
	if onAfter.DetectedModels != "gpt-5,o3" {
		t.Fatalf("on.detected = %q, want gpt-5,o3", onAfter.DetectedModels)
	}
	// 红线：后台 job 绝不改 channels.models（上货仍需手动 apply）
	if onAfter.Models != "gpt-4" {
		t.Fatalf("sync mutated channels.models = %q — 违反只写元数据红线", onAfter.Models)
	}
	if onAfter.LastSyncAt == nil {
		t.Fatal("on.last_sync_at not set")
	}
	// off：未 opt-in，detected 保持空
	if offAfter.DetectedModels != "" {
		t.Fatalf("off.detected = %q, want empty (not opted in)", offAfter.DetectedModels)
	}
}
