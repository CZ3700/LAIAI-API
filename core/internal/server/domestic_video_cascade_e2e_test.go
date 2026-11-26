package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/relay"
	"github.com/shopspring/decimal"
)

func createRuleAndBind(
	t *testing.T,
	f *gin999,
	model string,
	fwdName string,
	fwdRuleType string,
	fwdConfig string,
	billRuleName string,
	billRate string,
) {
	t.Helper()
	wFR, outFR := f.doAdmin(t, "POST", "/api/admin/forward-rules", map[string]any{
		"name":        fwdName,
		"rule_type":   fwdRuleType,
		"category":    "视频",
		"config_json": fwdConfig,
	})
	if wFR.Code != http.StatusCreated {
		t.Fatalf("create forward rule %s: %d %v", fwdName, wFR.Code, outFR)
	}
	frID := uint64(outFR["rule"].(map[string]any)["id"].(float64))

	wBR, outBR := f.doAdmin(t, "POST", "/api/admin/billing-rules", map[string]any{
		"name":            billRuleName,
		"billing_type":    "tokens",
		"billing_rule":    "standard",
		"prompt_rate":     "0",
		"completion_rate": billRate,
	})
	if wBR.Code != http.StatusCreated {
		t.Fatalf("create billing rule %s: %d %v", billRuleName, wBR.Code, outBR)
	}
	brID := uint64(outBR["rule"].(map[string]any)["id"].(float64))

	wb, ob := f.doAdmin(t, "POST", "/api/admin/model-rule-bindings", map[string]any{
		"model":            model,
		"display_name":     model,
		"billing_rule_id":  brID,
		"forward_rule_ids": []uint64{frID},
		"is_listed":        true,
	})
	if wb.Code != http.StatusOK {
		t.Fatalf("bind %s: %d %v", model, wb.Code, ob)
	}
}

func TestDomesticNativeInboundRoutesAndDialects(t *testing.T) {
	f, db, ts, _, apiKey := setupStage2Server(t)

	var mu sync.Mutex
	var gotPaths []string
	var gotBodies []map[string]any
	var gotHeaders []http.Header

	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawBody, _ := io.ReadAll(r.Body)
		var bMap map[string]any
		if len(rawBody) > 0 {
			_ = json.Unmarshal(rawBody, &bMap)
		}
		mu.Lock()
		gotPaths = append(gotPaths, r.Method+" "+r.URL.RequestURI())
		gotBodies = append(gotBodies, bMap)
		gotHeaders = append(gotHeaders, r.Header.Clone())
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		switch {
		// Vidu
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/ent/v2/"):
			_, _ = w.Write([]byte(`{"task_id":"up-vidu-1","state":"created"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/ent/v2/tasks/up-vidu-1/creations":
			_, _ = w.Write([]byte(`{"state":"success","credits":3,"creations":[{"id":"c1","url":"https://cdn.example.com/vidu.mp4"}]}`))
		// MiniMax V2
		case r.Method == http.MethodPost && r.URL.Path == "/v2/video_generation":
			_, _ = w.Write([]byte(`{"task_id":"up-mm-1","base_resp":{"status_code":0}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v2/query/video_generation/up-mm-1":
			_, _ = w.Write([]byte(`{"task":{"id":"up-mm-1","status":"succeeded","content":{"url":"https://cdn.example.com/mm.mp4"},"usage":{"completion_tokens":100000}},"base_resp":{"status_code":0}}`))
		// Kling 3.0
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/omni-video/"):
			_, _ = w.Write([]byte(`{"code":0,"data":{"id":"up-kling-1"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/tasks":
			_, _ = w.Write([]byte(`{"code":0,"data":[{"id":"up-kling-1","status":"succeeded","outputs":[{"url":"https://cdn.example.com/kling3.mp4"}],"final_unit_deduction":"2"}]}`))
		// Bailian DashScope
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/services/aigc/video-generation/video-synthesis":
			_, _ = w.Write([]byte(`{"output":{"task_id":"up-wan-1","task_status":"PENDING"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/tasks/up-wan-1":
			_, _ = w.Write([]byte(`{"output":{"task_id":"up-wan-1","task_status":"SUCCEEDED","video_url":"https://cdn.example.com/wan.mp4"}}`))
		// Jimeng CV V4
		case r.Method == http.MethodPost && r.URL.Query().Get("Action") == "CVSync2AsyncSubmitTask":
			_, _ = w.Write([]byte(`{"code":10000,"data":{"task_id":"up-jm-1"}}`))
		case r.Method == http.MethodPost && r.URL.Query().Get("Action") == "CVSync2AsyncGetResult":
			_, _ = w.Write([]byte(`{"code":10000,"data":{"status":"done","video_url":"https://cdn.example.com/jimeng.mp4"}}`))
		// Tencent VOD TC3
		case r.Method == http.MethodPost && r.Header.Get("X-TC-Action") == "CreateAigcVideoTask":
			_, _ = w.Write([]byte(`{"Response":{"TaskId":"up-tc-1"}}`))
		case r.Method == http.MethodPost && r.Header.Get("X-TC-Action") == "DescribeTaskDetail":
			_, _ = w.Write([]byte(`{"Response":{"Status":"FINISH","AigcVideoTask":{"ErrCode":0,"Output":{"FileInfos":[{"FileUrl":"https://cdn.example.com/tencent.mp4"}]}}}}`))
		// Seedance Draft pinning
		case r.Method == http.MethodPost && r.URL.Path == "/api/v3/contents/generations/tasks":
			if strings.Contains(string(rawBody), `"draft_task"`) {
				_, _ = w.Write([]byte(`{"id":"up-seed-final-2"}`))
			} else {
				_, _ = w.Write([]byte(`{"id":"up-seed-draft-1"}`))
			}
		case r.Method == http.MethodGet && r.URL.Path == "/api/v3/contents/generations/tasks/up-seed-draft-1":
			_, _ = w.Write([]byte(`{"status":"succeeded","content":{"video_url":"https://cdn.example.com/draft.mp4"},"usage":{"completion_tokens":50000}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v3/contents/generations/tasks/up-seed-final-2":
			_, _ = w.Write([]byte(`{"status":"succeeded","content":{"video_url":"https://cdn.example.com/final.mp4"},"usage":{"completion_tokens":100000}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"unmocked: ` + r.Method + ` ` + r.URL.RequestURI() + `"}`))
		}
	}))
	defer up.Close()

	// Channels for each dialect
	for _, chSpec := range []struct {
		name     string
		provider string
		key      string
		models   []string
	}{
		{"ch-vidu", "vidu", "vidu-token-secret", []string{"viduq3-pro"}},
		{"ch-mm", "minimax_video", "mm-bearer-secret", []string{"MiniMax-H3"}},
		{"ch-kling3", "kling_video", "kling-v3-secret", []string{"kling-v3-omni"}},
		{"ch-wan", "wan", "wan-bearer-secret", []string{"wan3.0-video"}},
		{"ch-jimeng", "jimeng_video", "jmAk:jmSk", []string{"dreamina_ic_generate_video_v2"}},
		{"ch-tcvod", "tencent_vod_video", "tcSid:tcSkey:13000999", []string{"Kling@2.5"}},
		{"ch-seed-pin", "volcengine", "volc-secret", []string{"seedance-2-5-pin"}},
	} {
		w, out := f.doAdmin(t, "POST", "/api/admin/channels", map[string]any{
			"name": chSpec.name, "provider": chSpec.provider, "base_url": up.URL,
			"api_key": chSpec.key, "models": chSpec.models,
		})
		if w.Code != http.StatusCreated {
			t.Fatalf("create channel %s: %d %v", chSpec.name, w.Code, out)
		}
	}

	createRuleAndBind(t, f, "viduq3-pro", "Vidu FR", "vidu",
		`{"target_type":"vidu","auth_type":"token","path_rewrite":{"old":"/v1/video/generations","new":"/ent/v2/img2video"},"path_passthrough":["/ent/v2/img2video","/ent/v2/start-end2video"],"poll_path":"/ent/v2/tasks/${task_id}/creations"}`,
		"Vidu BR", "1")
	createRuleAndBind(t, f, "MiniMax-H3", "MiniMax FR", "minimax",
		`{"target_type":"minimax_video","auth_type":"bearer","path_rewrite":{"old":"/v1/video/generations","new":"/v2/video_generation"},"path_passthrough":["/v2/video_generation"],"poll_path":"/v2/query/video_generation/${task_id}"}`,
		"MiniMax BR", "2")
	createRuleAndBind(t, f, "kling-v3-omni", "Kling3 FR", "kling",
		`{"target_type":"kling_video","auth_type":"bearer","path_rewrite":{"old":"/v1/video/generations","new":"/text-to-video/${model}"},"path_passthrough":["/omni-video/${model}","/text-to-video/${model}"],"poll_path":"/tasks?task_ids=${task_id}"}`,
		"Kling3 BR", "1")
	createRuleAndBind(t, f, "wan3.0-video", "Wan FR", "aliyun",
		`{"target_type":"dashscope","auth_type":"bearer","moderation":true,"path_rewrite":{"old":"/v1/video/generations","new":"/api/v1/services/aigc/video-generation/video-synthesis"},"path_passthrough":["/api/v1/services/aigc/video-generation/video-synthesis"],"poll_path":"/api/v1/tasks/${task_id}"}`,
		"Wan BR", "1")
	createRuleAndBind(t, f, "dreamina_ic_generate_video_v2", "Jimeng FR", "jimeng",
		`{"target_type":"jimeng_video","auth_type":"jimeng","path_rewrite":{"old":"/v1/video/generations","new":"/"}}`,
		"Jimeng BR", "1")
	createRuleAndBind(t, f, "Kling@2.5", "TCVOD FR", "tencent",
		`{"target_type":"tencent_vod_video","auth_type":"tencent_vod","path_rewrite":{"old":"/v1/video/generations","new":"/"}}`,
		"TCVOD BR", "1")
	createRuleAndBind(t, f, "seedance-2-5-pin", "SeedPin FR", "volcengine",
		`{"target_type":"volcengine","auth_type":"bearer","path_rewrite":{"old":"/v1/video/generations","new":"/api/v3/contents/generations/tasks"},"path_passthrough":["/api/v3/contents/generations/tasks"],"poll_path":"/api/v3/contents/generations/tasks/${task_id}"}`,
		"SeedPin BR", "10")

	// 1. Vidu native /ent/v2/start-end2video -> /ent/v2/tasks/:id/creations
	wv := doAPIKeyReq(t, f, http.MethodPost, "/ent/v2/start-end2video", "Authorization", "Token "+apiKey, map[string]any{
		"model": "viduq3-pro", "prompt": "sunrise", "images": []string{"https://i/1.png", "https://i/2.png"},
	})
	if wv.Code != http.StatusOK || !strings.Contains(wv.Body.String(), `"state":"created"`) {
		t.Fatalf("vidu native create = %d %s", wv.Code, wv.Body.String())
	}
	var vRes map[string]any
	_ = json.Unmarshal(wv.Body.Bytes(), &vRes)
	vID := vRes["task_id"].(string)

	// 2. MiniMax native /v2/video_generation -> /v2/query/video_generation/:id
	wmm := doAPIKeyReq(t, f, http.MethodPost, "/v2/video_generation", "Authorization", "Bearer "+apiKey, map[string]any{
		"model": "MiniMax-H3", "prompt": "sea wave", "watermark": true, "duration": 6,
	})
	if wmm.Code != http.StatusOK || !strings.Contains(wmm.Body.String(), `"task_id"`) {
		t.Fatalf("minimax native create = %d %s", wmm.Code, wmm.Body.String())
	}
	var mmRes map[string]any
	_ = json.Unmarshal(wmm.Body.Bytes(), &mmRes)
	mmID := mmRes["task_id"].(string)

	// 3. Kling 3.0 native /omni-video/kling-v3-omni -> /tasks?task_ids=
	wk := doAPIKeyReq(t, f, http.MethodPost, "/omni-video/kling-v3-omni", "Authorization", "Bearer "+apiKey, map[string]any{
		"prompt": "city night", "videos": []string{"https://v/1.mp4"},
	})
	if wk.Code != http.StatusOK || !strings.Contains(wk.Body.String(), `"code":0`) {
		t.Fatalf("kling3 native create = %d %s", wk.Code, wk.Body.String())
	}
	var kRes map[string]any
	_ = json.Unmarshal(wk.Body.Bytes(), &kRes)
	kID := kRes["task_id"].(string)

	// 4. DashScope native /api/v1/services/aigc/video-generation/video-synthesis -> /api/v1/tasks/:id
	wds := doAPIKeyReq(t, f, http.MethodPost, "/api/v1/services/aigc/video-generation/video-synthesis", "Authorization", "Bearer "+apiKey, map[string]any{
		"model": "wan3.0-video", "prompt": "golden dog",
	})
	if wds.Code != http.StatusOK || !strings.Contains(wds.Body.String(), `"PENDING"`) {
		t.Fatalf("dashscope native create = %d %s", wds.Code, wds.Body.String())
	}
	var dsRes map[string]any
	_ = json.Unmarshal(wds.Body.Bytes(), &dsRes)
	dsID := dsRes["output"].(map[string]any)["task_id"].(string)

	// 5. Jimeng V4 & Tencent VOD TC3 via OpenAI /v1/videos alias
	wjm := doAPIKeyReq(t, f, http.MethodPost, "/v1/videos", "Authorization", "Bearer "+apiKey, map[string]any{
		"model": "dreamina_ic_generate_video_v2", "prompt": "mountain", "duration": 10,
	})
	if wjm.Code != http.StatusAccepted {
		t.Fatalf("jimeng /v1/videos create = %d %s", wjm.Code, wjm.Body.String())
	}
	var jmRes map[string]any
	_ = json.Unmarshal(wjm.Body.Bytes(), &jmRes)
	jmID := jmRes["id"].(string)

	wtc := doAPIKeyReq(t, f, http.MethodPost, "/v1/videos/generations", "Authorization", "Bearer "+apiKey, map[string]any{
		"model": "Kling@2.5", "prompt": "dragon", "images": []string{"https://img/f.png"},
	})
	if wtc.Code != http.StatusAccepted {
		t.Fatalf("tencent vod /v1/videos/generations create = %d %s", wtc.Code, wtc.Body.String())
	}
	var tcRes map[string]any
	_ = json.Unmarshal(wtc.Body.Bytes(), &tcRes)
	tcID := tcRes["id"].(string)

	// 6. Seedance Draft -> Draft Task Pinning
	wsd1 := doAPIKeyReq(t, f, http.MethodPost, "/api/v3/contents/generations/tasks", "Authorization", "Bearer "+apiKey, map[string]any{
		"model": "seedance-2-5-pin", "prompt": "preview cat", "draft": true,
	})
	if wsd1.Code != http.StatusOK {
		t.Fatalf("seedance draft create = %d %s", wsd1.Code, wsd1.Body.String())
	}
	var sd1 map[string]any
	_ = json.Unmarshal(wsd1.Body.Bytes(), &sd1)
	sd1ID := sd1["id"].(string)

	wsd2 := doAPIKeyReq(t, f, http.MethodPost, "/api/v3/contents/generations/tasks", "Authorization", "Bearer "+apiKey, map[string]any{
		"model": "seedance-2-5-pin", "draft_task_id": sd1ID, "resolution": "1080p",
	})
	if wsd2.Code != http.StatusOK {
		t.Fatalf("seedance draft->final create = %d %s", wsd2.Code, wsd2.Body.String())
	}
	var sd2 map[string]any
	_ = json.Unmarshal(wsd2.Body.Bytes(), &sd2)
	sd2ID := sd2["id"].(string)

	// Verify draft_task_id was rewritten to upstream ID `up-seed-draft-1`
	mu.Lock()
	lastBody := gotBodies[len(gotBodies)-1]
	// Check DashScope async+moderation headers & Jimeng V4 + TC3 headers across recorded requests
	sawV4, sawTC3, sawDSAsync := false, false, false
	for _, h := range gotHeaders {
		if strings.HasPrefix(h.Get("Authorization"), "HMAC-SHA256 ") {
			sawV4 = true
		}
		if strings.HasPrefix(h.Get("Authorization"), "TC3-HMAC-SHA256 ") {
			sawTC3 = true
		}
		if h.Get("X-DashScope-Async") == "enable" && h.Get("X-DashScope-DataInspection") != "" {
			sawDSAsync = true
		}
	}
	mu.Unlock()
	if !sawV4 || !sawTC3 || !sawDSAsync {
		t.Fatalf("missing expected upstream auth/headers: v4=%v tc3=%v ds=%v", sawV4, sawTC3, sawDSAsync)
	}
	contentArr, _ := json.Marshal(lastBody["content"])
	if !strings.Contains(string(contentArr), "up-seed-draft-1") {
		t.Fatalf("expected rewritten upstream draft_task id in payload, got %s", string(contentArr))
	}

	// Poll all tasks to completion
	ts.PollOnce(context.Background())

	// Assert official native query envelopes
	gv := doAPIKeyReq(t, f, http.MethodGet, "/ent/v2/tasks/"+vID+"/creations", "Authorization", "Token "+apiKey, nil)
	if gv.Code != http.StatusOK || !strings.Contains(gv.Body.String(), `"state":"success"`) || !strings.Contains(gv.Body.String(), "https://cdn.example.com/vidu.mp4") {
		t.Fatalf("vidu native get = %d %s", gv.Code, gv.Body.String())
	}
	gmm := doAPIKeyReq(t, f, http.MethodGet, "/v2/query/video_generation/"+mmID, "Authorization", "Bearer "+apiKey, nil)
	if gmm.Code != http.StatusOK || !strings.Contains(gmm.Body.String(), `"status":"succeeded"`) || !strings.Contains(gmm.Body.String(), "https://cdn.example.com/mm.mp4") {
		t.Fatalf("minimax v2 get = %d %s", gmm.Code, gmm.Body.String())
	}
	gk := doAPIKeyReq(t, f, http.MethodGet, "/tasks?task_ids="+kID, "Authorization", "Bearer "+apiKey, nil)
	if gk.Code != http.StatusOK || !strings.Contains(gk.Body.String(), `"status":"succeeded"`) || !strings.Contains(gk.Body.String(), "https://cdn.example.com/kling3.mp4") {
		t.Fatalf("kling3 native get = %d %s", gk.Code, gk.Body.String())
	}
	gds := doAPIKeyReq(t, f, http.MethodGet, "/api/v1/tasks/"+dsID, "Authorization", "Bearer "+apiKey, nil)
	if gds.Code != http.StatusOK || !strings.Contains(gds.Body.String(), `"task_status":"SUCCEEDED"`) || !strings.Contains(gds.Body.String(), "https://cdn.example.com/wan.mp4") {
		t.Fatalf("dashscope native get = %d %s", gds.Code, gds.Body.String())
	}
	gjm := doAPIKeyReq(t, f, http.MethodGet, "/v1/videos/"+jmID, "Authorization", "Bearer "+apiKey, nil)
	if gjm.Code != http.StatusOK || !strings.Contains(gjm.Body.String(), "https://cdn.example.com/jimeng.mp4") {
		t.Fatalf("jimeng /v1/videos/:id get = %d %s", gjm.Code, gjm.Body.String())
	}
	gtc := doAPIKeyReq(t, f, http.MethodGet, "/v1/videos/generations/"+tcID, "Authorization", "Bearer "+apiKey, nil)
	if gtc.Code != http.StatusOK || !strings.Contains(gtc.Body.String(), "https://cdn.example.com/tencent.mp4") {
		t.Fatalf("tencent /v1/videos/generations/:id get = %d %s", gtc.Code, gtc.Body.String())
	}
	gsd2 := doAPIKeyReq(t, f, http.MethodGet, "/api/v3/contents/generations/tasks/"+sd2ID, "Authorization", "Bearer "+apiKey, nil)
	if gsd2.Code != http.StatusOK || !strings.Contains(gsd2.Body.String(), "https://cdn.example.com/final.mp4") {
		t.Fatalf("seedance final get = %d %s", gsd2.Code, gsd2.Body.String())
	}

	_ = db
}

func TestCascadeSuperResolutionLifecycleAndRefund(t *testing.T) {
	f, db, ts, _, apiKey := setupStage2Server(t)

	var s1SubmitResolutions []string
	var s2SubmitPayloads []map[string]any
	var mu sync.Mutex
	var s2FailMode atomic.Bool

	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var bMap map[string]any
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &bMap)
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v3/contents/generations/tasks":
			mu.Lock()
			if rs, ok := bMap["resolution"].(string); ok {
				s1SubmitResolutions = append(s1SubmitResolutions, rs)
			}
			n := len(s1SubmitResolutions)
			mu.Unlock()
			_, _ = w.Write([]byte(`{"id":"up-s1-` + string(rune('0'+n)) + `"}`))
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v3/contents/generations/tasks/up-s1-"):
			_, _ = w.Write([]byte(`{"status":"succeeded","content":{"video_url":"https://internal.cdn.example.com/s1_base.mp4","last_frame_url":"https://internal.cdn.example.com/lf.png"},"usage":{"completion_tokens":100000}}`))
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/v1/tools/enhance-video"):
			mu.Lock()
			s2SubmitPayloads = append(s2SubmitPayloads, bMap)
			n := len(s2SubmitPayloads)
			mu.Unlock()
			_, _ = w.Write([]byte(`{"task_id":"up-s2-` + string(rune('0'+n)) + `","status":"pending"}`))
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v1/tasks/up-s2-"):
			if s2FailMode.Load() {
				_, _ = w.Write([]byte(`{"task_id":"up-s2","status":"failed","error":"enhance gpu oom"}`))
				return
			}
			_, _ = w.Write([]byte(`{"task_id":"up-s2","status":"success","result":{"video_url":"https://public.cdn.example.com/enhanced_1080p.mp4","resolution":"1920x1080"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer up.Close()

	// Seedance Stage-1 & MediaKit Stage-2 channels
	wCh1, outCh1 := f.doAdmin(t, "POST", "/api/admin/channels", map[string]any{
		"name": "cascade-s1", "provider": "volcengine", "base_url": up.URL,
		"api_key": "sk-s1", "models": []string{"seedance-cascade"},
	})
	if wCh1.Code != http.StatusCreated {
		t.Fatalf("create s1 channel: %d %v", wCh1.Code, outCh1)
	}
	wCh2, outCh2 := f.doAdmin(t, "POST", "/api/admin/channels", map[string]any{
		"name": "cascade-s2", "provider": "volcengine_media_enhance", "base_url": up.URL,
		"api_key": "sk-s2", "models": []string{"vve-sd"},
	})
	if wCh2.Code != http.StatusCreated {
		t.Fatalf("create s2 channel: %d %v", wCh2.Code, outCh2)
	}

	// 10/1M tokens
	cascadeCfg := `{
		"target_type": "volcengine",
		"auth_type": "bearer",
		"is_cascade": true,
		"cascade_engine": "volc",
		"path_rewrite": {"old": "/v1/video/generations", "new": "/api/v3/contents/generations/tasks"},
		"path_passthrough": ["/api/v3/contents/generations/tasks"],
		"poll_path": "/api/v3/contents/generations/tasks/${task_id}",
		"res_mul": {"480p": 1.5, "720p": 2.15, "1080p": 2.25},
		"res_base": {"480p": "480p", "720p": "480p", "1080p": "720p"},
		"res_enhance_enabled": {"480p": false, "720p": true, "1080p": true}
	}`
	createRuleAndBind(t, f, "seedance-cascade", "Seedance Cascade Rule", "volcengine", cascadeCfg, "Cascade 10/1M", "10")

	// Check initial org balance = 100
	var accBefore ledger.Account
	_ = db.Where("type = ? AND org_id = ?", ledger.AccountUserCredit, uint64(1)).First(&accBefore)

	// 1) 1080p 2-stage flow: Stage 1 receives 720p, Stage 2 enhances to 1080p, billing = 100k * 2.25 * $10/1M = $2.25
	wCreate := doAPIKeyReq(t, f, http.MethodPost, "/api/v3/contents/generations/tasks", "Authorization", "Bearer "+apiKey, map[string]any{
		"model":      "seedance-cascade",
		"prompt":     "cinematic drone shot",
		"resolution": "1080p",
		"duration":   5,
	})
	if wCreate.Code != http.StatusOK {
		t.Fatalf("cascade 1080p create = %d %s", wCreate.Code, wCreate.Body.String())
	}
	var cr struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(wCreate.Body.Bytes(), &cr)

	// Poll #1: Stage 1 succeeds -> submits Stage 2, task stays running, NEVER leaks S1 URL!
	ts.PollOnce(context.Background())
	midGet := doAPIKeyReq(t, f, http.MethodGet, "/api/v3/contents/generations/tasks/"+cr.ID, "Authorization", "Bearer "+apiKey, nil)
	if midGet.Code != http.StatusOK || !strings.Contains(midGet.Body.String(), `"status":"running"`) {
		t.Fatalf("after S1 poll, task should stay running while S2 executes: %s", midGet.Body.String())
	}
	if strings.Contains(midGet.Body.String(), "s1_base.mp4") {
		t.Fatalf("client response leaked internal S1 URL: %s", midGet.Body.String())
	}

	// Poll #2: Stage 2 completes -> final 1080p URL + 225,000 scaled tokens = $2.25
	ts.PollOnce(context.Background())
	finalGet := doAPIKeyReq(t, f, http.MethodGet, "/api/v3/contents/generations/tasks/"+cr.ID, "Authorization", "Bearer "+apiKey, nil)
	if finalGet.Code != http.StatusOK || !strings.Contains(finalGet.Body.String(), `"status":"succeeded"`) ||
		!strings.Contains(finalGet.Body.String(), "https://public.cdn.example.com/enhanced_1080p.mp4") ||
		!strings.Contains(finalGet.Body.String(), `"completion_tokens":225000`) {
		t.Fatalf("unexpected final cascade 1080p get: %s", finalGet.Body.String())
	}
	var fin struct {
		CostUSD decimal.Decimal `json:"cost_usd"`
	}
	_ = json.Unmarshal(finalGet.Body.Bytes(), &fin)
	if !fin.CostUSD.Equal(decimal.RequireFromString("2.25")) {
		t.Fatalf("cascade 1080p cost_usd = %s, want 2.25", fin.CostUSD.String())
	}

	mu.Lock()
	if len(s1SubmitResolutions) == 0 || s1SubmitResolutions[0] != "720p" {
		t.Fatalf("expected S1 base resolution 720p for 1080p request, got %v", s1SubmitResolutions)
	}
	if len(s2SubmitPayloads) == 0 || s2SubmitPayloads[0]["resolution"] != "1080p" || s2SubmitPayloads[0]["video_url"] != "https://internal.cdn.example.com/s1_base.mp4" {
		t.Fatalf("unexpected S2 enhance payload: %+v", s2SubmitPayloads)
	}
	mu.Unlock()

	// 2) 480p skip_s2 fast path: settles on first poll with 1.5x -> 150,000 tokens = $1.50, zero extra S2 submit
	w480 := doAPIKeyReq(t, f, http.MethodPost, "/v1/video/generations", "Authorization", "Bearer "+apiKey, map[string]any{
		"model":      "seedance-cascade",
		"prompt":     "fast 480p clip",
		"resolution": "480p",
		"duration":   4,
	})
	if w480.Code != http.StatusAccepted {
		t.Fatalf("cascade 480p create = %d %s", w480.Code, w480.Body.String())
	}
	var cr480 struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(w480.Body.Bytes(), &cr480)
	ts.PollOnce(context.Background())
	get480 := doAPIKeyReq(t, f, http.MethodGet, "/v1/video/generations/"+cr480.ID, "Authorization", "Bearer "+apiKey, nil)
	if !strings.Contains(get480.Body.String(), `"status":"succeeded"`) || !strings.Contains(get480.Body.String(), `"completion_tokens":150000`) {
		t.Fatalf("480p skip_s2 failed: %s", get480.Body.String())
	}

	// 3) Stage-2 failure -> full hold refund (ledger balance untouched from prior $100 - $2.25 - $1.50 = $96.25)
	s2FailMode.Store(true)
	wFail := doAPIKeyReq(t, f, http.MethodPost, "/api/v3/contents/generations/tasks", "Authorization", "Bearer "+apiKey, map[string]any{
		"model":      "seedance-cascade",
		"prompt":     "will fail in s2",
		"resolution": "1080p",
		"duration":   5,
	})
	var crFail struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(wFail.Body.Bytes(), &crFail)
	ts.PollOnce(context.Background()) // S1 ok -> submit S2
	ts.PollOnce(context.Background()) // S2 fails -> release hold
	getFail := doAPIKeyReq(t, f, http.MethodGet, "/api/v3/contents/generations/tasks/"+crFail.ID, "Authorization", "Bearer "+apiKey, nil)
	if !strings.Contains(getFail.Body.String(), `"status":"failed"`) || !strings.Contains(getFail.Body.String(), "enhance gpu oom") {
		t.Fatalf("expected failed task with s2 reason, got: %s", getFail.Body.String())
	}

	var accAfter ledger.Account
	_ = db.Where("type = ? AND org_id = ?", ledger.AccountUserCredit, accBefore.OrgID).First(&accAfter)
	wantBal := decimal.RequireFromString("96.25")
	if !accAfter.PostedBalance.Equal(wantBal) || !accAfter.HeldAmount.IsZero() {
		t.Fatalf("after cascade tests: posted=%s held=%s, want posted=%s held=0", accAfter.PostedBalance, accAfter.HeldAmount, wantBal)
	}
	_ = relay.ErrNoChannel
}
