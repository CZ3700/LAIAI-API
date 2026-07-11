package relay

// Replicate 生图适配测试：请求转换（透传/排除/n/size 语义）+ prediction 收尾
// （立即成功/轮询兜底/失败不收费/b64 下载）。fixture 按 Replicate predictions API 文档推导。

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestReplicateBuildRequest(t *testing.T) {
	ch := &Channel{Provider: "replicate", BaseURL: "https://api.replicate.com"}
	rawBody := []byte(`{"model":"black-forest-labs/flux-1.1-pro","prompt":"a cat","n":1,` +
		`"size":"1792x1024","response_format":"url","seed":42,"output_format":"png","quality":"hd",` +
		`"num_outputs":9,"num_images":9,"batch_size":9}`) // 张数别名旋钮必须被剥离（计费口径只认 n）
	upReq, err := buildReplicateImagesRequest(context.Background(), ch, "r8_key", rawBody, "black-forest-labs/flux-1.1-pro")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if upReq.URL.String() != "https://api.replicate.com/v1/models/black-forest-labs/flux-1.1-pro/predictions" {
		t.Fatalf("url = %s", upReq.URL.String())
	}
	if upReq.Header.Get("Authorization") != "Bearer r8_key" || !strings.HasPrefix(upReq.Header.Get("Prefer"), "wait") {
		t.Fatalf("headers = %v", upReq.Header)
	}
	body, _ := io.ReadAll(upReq.Body)
	var b struct {
		Input map[string]any `json:"input"`
	}
	if json.Unmarshal(body, &b) != nil || b.Input == nil {
		t.Fatalf("body: %s", body)
	}
	if b.Input["prompt"] != "a cat" || b.Input["seed"] != float64(42) || b.Input["output_format"] != "png" {
		t.Fatalf("input 透传缺失: %v", b.Input)
	}
	if b.Input["aspect_ratio"] != "16:9" { // size → aspect_ratio 枚举
		t.Fatalf("aspect_ratio = %v", b.Input["aspect_ratio"])
	}
	// n=1 不发 num_outputs（单图模型 schema 兼容）；OpenAI 专有字段与张数别名旋钮不进 input
	// （num_outputs=9 直传会绕过按张 hold：冻 1 张实拿 9 张，资金红线——对抗 review HIGH）
	for _, k := range []string{"num_outputs", "num_images", "batch_size", "model", "n", "size", "response_format", "quality"} {
		if _, has := b.Input[k]; has {
			t.Fatalf("input 不应含 %q: %v", k, b.Input)
		}
	}

	// n=4 → num_outputs=4；客户端显式 aspect_ratio 优先于 size 映射
	rawBody2 := []byte(`{"model":"m","prompt":"p","n":4,"size":"1024x1024","aspect_ratio":"21:9"}`)
	upReq2, _ := buildReplicateImagesRequest(context.Background(), ch, "k", rawBody2, "m")
	body2, _ := io.ReadAll(upReq2.Body)
	var b2 struct {
		Input map[string]any `json:"input"`
	}
	_ = json.Unmarshal(body2, &b2)
	if b2.Input["num_outputs"] != float64(4) || b2.Input["aspect_ratio"] != "21:9" {
		t.Fatalf("n>1/显式 aspect_ratio: %v", b2.Input)
	}
}

func TestReplicateFinalizeImmediateSuccess(t *testing.T) {
	now := func() time.Time { return time.Unix(1_700_000_000, 0) }
	raw := []byte(`{"id":"pred1","status":"succeeded","output":["https://replicate.delivery/a.webp","https://replicate.delivery/b.webp"]}`)
	out, err := replicateFinalizeImages(context.Background(), http.DefaultClient, http.DefaultClient,
		"https://api.replicate.com", "k", raw, false, 0, now)
	if err != nil {
		t.Fatalf("finalize: %v", err)
	}
	var resp struct {
		Created int64 `json:"created"`
		Data    []struct {
			URL string `json:"url"`
		} `json:"data"`
	}
	if json.Unmarshal(out, &resp) != nil || len(resp.Data) != 2 {
		t.Fatalf("openai shape: %s", out)
	}
	if resp.Data[0].URL != "https://replicate.delivery/a.webp" || resp.Created != 1_700_000_000 {
		t.Fatalf("data = %+v created=%d", resp.Data, resp.Created)
	}

	// output 为单 string 也接受
	raw2 := []byte(`{"id":"pred2","status":"succeeded","output":"https://replicate.delivery/c.webp"}`)
	out2, err := replicateFinalizeImages(context.Background(), http.DefaultClient, http.DefaultClient, "x", "k", raw2, false, 0, now)
	if err != nil || !strings.Contains(string(out2), "c.webp") {
		t.Fatalf("single output: %s err=%v", out2, err)
	}
}

func TestReplicateFinalizePolling(t *testing.T) {
	old := replicatePollInterval
	replicatePollInterval = 5 * time.Millisecond
	defer func() { replicatePollInterval = old }()

	var polls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/v1/predictions/pred9") || r.Header.Get("Authorization") != "Bearer rk" {
			t.Errorf("poll req = %s auth=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		switch polls.Add(1) {
		case 1: // 瞬时 5xx：预算内容错，不得击杀整个 prediction（对抗 review MEDIUM）
			w.WriteHeader(http.StatusBadGateway)
		case 2:
			_, _ = w.Write([]byte(`{"id":"pred9","status":"processing"}`))
		default:
			_, _ = w.Write([]byte(`{"id":"pred9","status":"succeeded","output":["https://replicate.delivery/d.png"]}`))
		}
	}))
	defer srv.Close()

	raw := []byte(`{"id":"pred9","status":"starting"}`) // Prefer:wait 超窗返回未终态
	out, err := replicateFinalizeImages(context.Background(), http.DefaultClient, http.DefaultClient,
		srv.URL, "rk", raw, false, 0, time.Now)
	if err != nil {
		t.Fatalf("finalize with poll: %v", err)
	}
	if !strings.Contains(string(out), "d.png") || polls.Load() != 3 {
		t.Fatalf("out=%s polls=%d", out, polls.Load())
	}
}

// 确定性 4xx（401 等）不重试，立即终止。
func TestReplicateFinalizePollHard4xx(t *testing.T) {
	old := replicatePollInterval
	replicatePollInterval = 5 * time.Millisecond
	defer func() { replicatePollInterval = old }()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	_, err := replicateFinalizeImages(context.Background(), http.DefaultClient, http.DefaultClient,
		srv.URL, "rk", []byte(`{"id":"px","status":"processing"}`), false, 0, time.Now)
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("hard 4xx should terminate immediately: %v", err)
	}
}

// 交付张数封顶到计费口径 n：上游多回的图不下发（与 settle 钳制对齐，多拿少付堵死）。
func TestReplicateFinalizeCapsToN(t *testing.T) {
	raw := []byte(`{"id":"p","status":"succeeded","output":["https://r.d/1.png","https://r.d/2.png","https://r.d/3.png"]}`)
	out, err := replicateFinalizeImages(context.Background(), http.DefaultClient, http.DefaultClient,
		"x", "k", raw, false, 1, time.Now)
	if err != nil {
		t.Fatalf("cap finalize: %v", err)
	}
	var resp struct {
		Data []any `json:"data"`
	}
	_ = json.Unmarshal(out, &resp)
	if len(resp.Data) != 1 {
		t.Fatalf("delivery must cap to n=1, got %d", len(resp.Data))
	}
}

func TestReplicateFinalizeFailure(t *testing.T) {
	now := time.Now
	// failed → error（调用方释放 hold 不收费）
	raw := []byte(`{"id":"p","status":"failed","error":"NSFW content detected"}`)
	if _, err := replicateFinalizeImages(context.Background(), http.DefaultClient, http.DefaultClient, "x", "k", raw, false, 0, now); err == nil {
		t.Fatal("failed prediction must error")
	}
	// succeeded 但 output 空 → error（不收费）
	raw2 := []byte(`{"id":"p","status":"succeeded","output":[]}`)
	if _, err := replicateFinalizeImages(context.Background(), http.DefaultClient, http.DefaultClient, "x", "k", raw2, false, 0, now); err == nil {
		t.Fatal("empty output must error")
	}
	// 非 prediction 形态 → error
	if _, err := replicateFinalizeImages(context.Background(), http.DefaultClient, http.DefaultClient, "x", "k", []byte(`[1,2]`), false, 0, now); err == nil {
		t.Fatal("non-object must error")
	}
	// 未终态且无 id → error（没法轮询）
	if _, err := replicateFinalizeImages(context.Background(), http.DefaultClient, http.DefaultClient, "x", "k", []byte(`{"status":"processing"}`), false, 0, now); err == nil {
		t.Fatal("non-terminal without id must error")
	}
}

func TestReplicateFinalizeB64(t *testing.T) {
	imgBytes := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a}
	img := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(imgBytes)
	}))
	defer img.Close()

	raw := []byte(`{"id":"p","status":"succeeded","output":["` + img.URL + `/out.png"]}`)
	out, err := replicateFinalizeImages(context.Background(), http.DefaultClient, http.DefaultClient,
		"x", "k", raw, true, 0, time.Now)
	if err != nil {
		t.Fatalf("b64 finalize: %v", err)
	}
	var resp struct {
		Data []struct {
			B64 string `json:"b64_json"`
			URL string `json:"url"`
		} `json:"data"`
	}
	if json.Unmarshal(out, &resp) != nil || len(resp.Data) != 1 {
		t.Fatalf("shape: %s", out)
	}
	if resp.Data[0].B64 != base64.StdEncoding.EncodeToString(imgBytes) || resp.Data[0].URL != "" {
		t.Fatalf("b64 = %+v", resp.Data[0])
	}
}
