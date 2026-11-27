package relay

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/klauspost/compress/zstd"
)

func TestCompiledChannelMetadata(t *testing.T) {
	t.Parallel()

	ch := &Channel{
		Provider:       "openai",
		Models:         "gpt-4o, claude-sonnet-4, gemini-2.5-pro",
		ModelMapping:   `{"gpt-4o":"gpt-4o-2024-11-20","claude-3-5-*":"claude-3-5-sonnet-20241022","re:^qwen3-.*$":"qwen3-max"}`,
		ParamOverride:  `{"temperature":0.2,"stream":true,"max_tokens":999999}`,
		HeaderOverride: `{"X-Auth":"Bearer {{credential}}","X-Drop":null,"Host":"evil.example"}`,
	}
	ch.compile()

	if !ch.ServesModel("gpt-4o") || !ch.ServesModel("gemini-2.5-pro") || ch.ServesModel("gpt-3.5-turbo") {
		t.Fatalf("unexpected ServesModel results")
	}
	if got := ch.UpstreamModel("gpt-4o"); got != "gpt-4o-2024-11-20" {
		t.Fatalf("exact mapping = %q", got)
	}
	if got := ch.UpstreamModel("claude-3-5-haiku"); got != "claude-3-5-sonnet-20241022" {
		t.Fatalf("prefix wildcard mapping = %q", got)
	}
	if got := ch.UpstreamModel("qwen3-coder-plus"); got != "qwen3-max" {
		t.Fatalf("regex mapping = %q", got)
	}
	if pairs := ch.MappingPairs(); len(pairs) != 3 {
		t.Fatalf("MappingPairs len = %d, want 3", len(pairs))
	}

	req := map[string]any{"model": "gpt-4o", "max_tokens": float64(512), "stream": false}
	overridden := applyParamOverrideChannel(req, ch)
	if overridden["temperature"] != 0.2 {
		t.Fatalf("param override temperature missing: %v", overridden)
	}
	if overridden["max_tokens"] != float64(512) || overridden["stream"] != false {
		t.Fatalf("protected param keys were mutated: %v", overridden)
	}

	upReq := httptest.NewRequest(http.MethodPost, "https://api.example.com/v1/chat/completions", nil)
	upReq.Host = "api.example.com"
	upReq.Header.Set("X-Drop", "remove-me")
	applyHeaderOverrideChannel(upReq, ch, "sk-live-cred")
	if got := upReq.Header.Get("X-Auth"); got != "Bearer sk-live-cred" {
		t.Fatalf("header override X-Auth = %q", got)
	}
	if upReq.Header.Get("X-Drop") != "" {
		t.Fatalf("header X-Drop not deleted")
	}
	if upReq.Host != "api.example.com" {
		t.Fatalf("forbidden Host header modified: %q", upReq.Host)
	}
}

func TestThinkingToContentWriter(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	t.Run("non-stream JSON folding", func(t *testing.T) {
		t.Parallel()
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		ttc := &thinkingToContentWriter{ResponseWriter: c.Writer, stream: false}
		c.Writer = ttc

		c.Data(http.StatusOK, "application/json", []byte(`{"id":"1","choices":[{"message":{"role":"assistant","reasoning_content":"step 1","content":"final"}}]}`))
		ttc.finalize()

		var parsed map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &parsed); err != nil {
			t.Fatalf("unmarshal: %v body=%s", err, rec.Body.String())
		}
		msg := parsed["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)
		if _, has := msg["reasoning_content"]; has {
			t.Fatalf("reasoning_content should be removed: %v", msg)
		}
		if got, _ := msg["content"].(string); got != "<think>step 1</think>\n\nfinal" {
			t.Fatalf("content = %q, want <think>step 1</think>\\n\\nfinal", got)
		}
	})

	t.Run("stream SSE folding", func(t *testing.T) {
		t.Parallel()
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		ttc := &thinkingToContentWriter{ResponseWriter: c.Writer, stream: true}
		c.Writer = ttc
		ttc.WriteHeader(http.StatusOK)

		_, _ = ttc.WriteString("data: {\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"hmm \"}}]}\n\n")
		_, _ = ttc.WriteString("data: {\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"}}]}\n\n")
		_, _ = ttc.WriteString("data: [DONE]\n\n")
		ttc.finalize()

		body := rec.Body.String()
		if !strings.Contains(body, `"content":"<think>hmm "`) {
			t.Fatalf("expected <think> opening in SSE, got:\n%s", body)
		}
		if !strings.Contains(body, `"content":"</think>\n\nok"`) {
			t.Fatalf("expected </think> close before answer content in SSE, got:\n%s", body)
		}
	})
}

func TestAnthropicThinkingSignatureAndRedactedRoundtrip(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	t.Run("JSON response roundtrip preserves signature on anthropic ingress only", func(t *testing.T) {
		t.Parallel()
		upstreamPayload := []byte(`{
			"id":"msg_1","model":"claude-sonnet-4","stop_reason":"end_turn",
			"content":[
				{"type":"thinking","thinking":"deep thought","signature":"sig_xyz"},
				{"type":"redacted_thinking","data":"cipher_blob"},
				{"type":"text","text":"answer"}
			],
			"usage":{"input_tokens":10,"output_tokens":5}
		}`)

		// Anthropic ingress retains internal signature metadata for lossless egress translation.
		recAnt := httptest.NewRecorder()
		cAnt, _ := gin.CreateTestContext(recAnt)
		cAnt.Set(ingressProtocolKey, "anthropic")
		if _, _, err := (anthropicAdapter{}).pipeJSON(cAnt, strings.NewReader(string(upstreamPayload))); err != nil {
			t.Fatalf("pipeJSON anthropic err: %v", err)
		}
		antJSON, ok := openaiRespToAnthropic(recAnt.Body.Bytes())
		if !ok {
			t.Fatalf("openaiRespToAnthropic failed")
		}
		var msgOut map[string]any
		if err := json.Unmarshal(antJSON, &msgOut); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		blocks := msgOut["content"].([]any)
		if len(blocks) != 3 {
			t.Fatalf("blocks len = %d, want 3 (thinking + redacted_thinking + text): %v", len(blocks), blocks)
		}
		b0 := blocks[0].(map[string]any)
		if b0["type"] != "thinking" || b0["signature"] != "sig_xyz" || b0["thinking"] != "deep thought" {
			t.Fatalf("thinking block mismatch: %v", b0)
		}
		b1 := blocks[1].(map[string]any)
		if b1["type"] != "redacted_thinking" || b1["data"] != "cipher_blob" {
			t.Fatalf("redacted_thinking block mismatch: %v", b1)
		}

		// Native OpenAI ingress stays clean without underscore internal fields.
		recOAI := httptest.NewRecorder()
		cOAI, _ := gin.CreateTestContext(recOAI)
		if _, _, err := (anthropicAdapter{}).pipeJSON(cOAI, strings.NewReader(string(upstreamPayload))); err != nil {
			t.Fatalf("pipeJSON openai err: %v", err)
		}
		oaiBody := recOAI.Body.String()
		if strings.Contains(oaiBody, "_thinking_signature") || strings.Contains(oaiBody, "_redacted_thinking") {
			t.Fatalf("native OpenAI response leaked underscore fields: %s", oaiBody)
		}
	})

	t.Run("SSE stream roundtrip preserves thinking signature and redacted_thinking", func(t *testing.T) {
		t.Parallel()
		sseIn := strings.Join([]string{
			`data: {"type":"message_start","message":{"usage":{"input_tokens":7}}}`,
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"step"}}`,
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig_stream"}}`,
			`data: {"type":"content_block_start","index":1,"content_block":{"type":"redacted_thinking","data":"sec_blob"}}`,
			`data: {"type":"content_block_start","index":2,"content_block":{"type":"text","text":""}}`,
			`data: {"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"done"}}`,
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":4}}`,
			`data: {"type":"message_stop"}`,
		}, "\n") + "\n"

		recPipe := httptest.NewRecorder()
		cPipe, _ := gin.CreateTestContext(recPipe)
		cPipe.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		cPipe.Set(ingressProtocolKey, "anthropic")
		if _, _, err := (anthropicAdapter{}).pipeSSE(cPipe, strings.NewReader(sseIn)); err != nil {
			t.Fatalf("pipeSSE: %v", err)
		}

		// Feed pipeSSE chunks through anthropicSSE egress translator.
		recOut := httptest.NewRecorder()
		cOut, _ := gin.CreateTestContext(recOut)
		state := &anthropicSSE{}
		for line := range strings.SplitSeq(recPipe.Body.String(), "\n") {
			data, ok := strings.CutPrefix(line, "data: ")
			if !ok {
				continue
			}
			if data == "[DONE]" {
				state.finishStream(cOut.Writer)
				break
			}
			state.handleChunk(cOut.Writer, data)
		}
		outSSE := recOut.Body.String()
		for _, want := range []string{`"type":"thinking"`, `"type":"thinking_delta"`, `"type":"signature_delta"`, `"signature":"sig_stream"`, `"type":"redacted_thinking"`, `"data":"sec_blob"`, `"text":"done"`} {
			if !strings.Contains(outSSE, want) {
				t.Fatalf("SSE output missing %s:\n%s", want, outSSE)
			}
		}
	})

	t.Run("multi-turn inbound _thinking_blocks restored for Anthropic and stripped for OpenAI", func(t *testing.T) {
		t.Parallel()
		inbound := []byte(`{
			"model":"claude-sonnet-4","max_tokens":1024,
			"messages":[
				{"role":"user","content":"q1"},
				{"role":"assistant","content":[
					{"type":"thinking","thinking":"prev","signature":"sig_prev"},
					{"type":"redacted_thinking","data":"rd_prev"},
					{"type":"text","text":"a1"}
				]},
				{"role":"user","content":"q2"}
			]
		}`)
		openAIReqBytes, _, err := anthropicToOpenAIRequest(inbound)
		if err != nil {
			t.Fatalf("anthropicToOpenAIRequest: %v", err)
		}
		var reqMap map[string]any
		if err := json.Unmarshal(openAIReqBytes, &reqMap); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}

		antCh := &Channel{Provider: "anthropic"}
		antPrepared := prepareAttemptRequest(reqMap, antCh, "claude-sonnet-4", 0)
		antBody, err := anthropicRequestBody(antPrepared, false)
		if err != nil {
			t.Fatalf("anthropicRequestBody: %v", err)
		}
		antMsgs := antBody["messages"].([]map[string]any)
		assistantBlocks := antMsgs[1]["content"].([]any)
		if assistantBlocks[0].(map[string]any)["type"] != "thinking" || assistantBlocks[1].(map[string]any)["type"] != "redacted_thinking" {
			t.Fatalf("thinking blocks lost in Anthropic outbound: %v", assistantBlocks)
		}

		openAICh := &Channel{Provider: "openai"}
		openAIPrepared := prepareAttemptRequest(reqMap, openAICh, "gpt-4o", 8192)
		if _, hasSB := openAIPrepared["_suffix_thinking_budget"]; hasSB {
			t.Fatalf("_suffix_thinking_budget leaked to OpenAI request")
		}
		oaiBytes, _ := json.Marshal(openAIPrepared)
		if strings.Contains(string(oaiBytes), "_thinking_blocks") {
			t.Fatalf("_thinking_blocks leaked to OpenAI upstream payload: %s", string(oaiBytes))
		}
	})
}

func TestReasoningSuffixFallbackAndCompression(t *testing.T) {
	t.Parallel()

	cases := []struct {
		model      string
		wantBase   string
		wantEffort string
		wantBudget int64
		wantOK     bool
	}{
		{"gpt-5-high", "gpt-5", "high", reasoningBudgetHigh, true},
		{"o3-mini-MEDIUM", "o3-mini", "medium", reasoningBudgetMedium, true},
		{"claude-sonnet-4-low", "claude-sonnet-4", "low", reasoningBudgetLow, true},
		{"gpt-4o", "gpt-4o", "", 0, false},
	}
	for _, tc := range cases {
		base, effort, ok := splitReasoningSuffix(tc.model)
		if base != tc.wantBase || effort != tc.wantEffort || ok != tc.wantOK {
			t.Fatalf("split(%q) = (%q,%q,%v), want (%q,%q,%v)", tc.model, base, effort, ok, tc.wantBase, tc.wantEffort, tc.wantOK)
		}
		if got := reasoningEffortBudget(effort); got != tc.wantBudget {
			t.Fatalf("budget(%q) = %d, want %d", effort, got, tc.wantBudget)
		}
	}

	// Suffix budget converts into Anthropic thinking block when targeting Claude.
	antBody, err := anthropicRequestBody(map[string]any{
		"model":                   "claude-sonnet-4",
		"messages":                []any{map[string]any{"role": "user", "content": "hi"}},
		"_suffix_thinking_budget": reasoningBudgetHigh,
	}, false)
	if err != nil {
		t.Fatalf("anthropicRequestBody: %v", err)
	}
	th, ok := antBody["thinking"].(map[string]any)
	if !ok || th["budget_tokens"] != float64(reasoningBudgetHigh) {
		t.Fatalf("thinking = %v, want budget %d", antBody["thinking"], reasoningBudgetHigh)
	}

	// Test zstd, gzip, and deflate decompression helpers.
	const payload = `{"ok":true,"msg":"compressed-hello"}`
	t.Run("zstd", func(t *testing.T) {
		var buf bytes.Buffer
		enc, _ := zstd.NewWriter(&buf)
		_, _ = enc.Write([]byte(payload))
		_ = enc.Close()
		resp := &http.Response{
			Header: http.Header{"Content-Encoding": {"zstd"}, "Content-Length": {"99"}},
			Body:   io.NopCloser(&buf),
		}
		rc := wrapCompressedBody(resp)
		defer rc.Close()
		got, err := io.ReadAll(rc)
		if err != nil || string(got) != payload {
			t.Fatalf("zstd read = %q err=%v", string(got), err)
		}
		if resp.Header.Get("Content-Encoding") != "" {
			t.Fatalf("Content-Encoding should be stripped")
		}
	})

	t.Run("gzip", func(t *testing.T) {
		var buf bytes.Buffer
		gw := gzip.NewWriter(&buf)
		_, _ = gw.Write([]byte(payload))
		_ = gw.Close()
		resp := &http.Response{
			Header: http.Header{"Content-Encoding": {"gzip"}},
			Body:   io.NopCloser(&buf),
		}
		rc := wrapCompressedBody(resp)
		defer rc.Close()
		got, err := io.ReadAll(rc)
		if err != nil || string(got) != payload {
			t.Fatalf("gzip read = %q err=%v", string(got), err)
		}
	})

	t.Run("deflate", func(t *testing.T) {
		var buf bytes.Buffer
		fw, _ := flate.NewWriter(&buf, flate.DefaultCompression)
		_, _ = fw.Write([]byte(payload))
		_ = fw.Close()
		resp := &http.Response{
			Header: http.Header{"Content-Encoding": {"deflate"}},
			Body:   io.NopCloser(&buf),
		}
		rc := wrapCompressedBody(resp)
		defer rc.Close()
		got, err := io.ReadAll(rc)
		if err != nil || string(got) != payload {
			t.Fatalf("deflate read = %q err=%v", string(got), err)
		}
	})
}
