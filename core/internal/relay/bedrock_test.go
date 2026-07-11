package relay

// AWS Bedrock 适配器测试：凭证解析 + Converse 请求构造 + Converse 响应转换 + event stream 流式解码。
// fixture 按 AWS Bedrock Converse API 文档推导（非真机）；SigV4 正确性由 sigv4_test 官方向量保证。

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

func TestParseBedrockCred(t *testing.T) {
	ak, sk, region, tok, err := parseBedrockCred("AKID|SECRET|us-east-1")
	if err != nil || ak != "AKID" || sk != "SECRET" || region != "us-east-1" || tok != "" {
		t.Fatalf("3-part = (%q,%q,%q,%q) err=%v", ak, sk, region, tok, err)
	}
	_, _, _, tok, err = parseBedrockCred("AKID|SECRET|us-west-2|sess-token")
	if err != nil || tok != "sess-token" {
		t.Fatalf("4-part token = %q err=%v", tok, err)
	}
	if _, _, _, _, err := parseBedrockCred("AKID|SECRET"); err == nil {
		t.Fatal("2-part should error")
	}
	if _, _, _, _, err := parseBedrockCred("||us-east-1"); err == nil {
		t.Fatal("empty fields should error")
	}
}

func TestBedrockBuildRequest(t *testing.T) {
	ch := &Channel{Provider: "bedrock"}
	req := map[string]any{
		"model":       "anthropic.claude-3-5-sonnet-20241022-v2:0",
		"max_tokens":  float64(256),
		"temperature": float64(0.7),
		"messages": []any{
			map[string]any{"role": "system", "content": "be brief"},
			map[string]any{"role": "user", "content": "hi"},
		},
	}
	upReq, err := bedrockAdapter{}.buildRequest(context.Background(), ch, "AK|SK|us-east-1", req, false)
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	if upReq.URL.String() != "https://bedrock-runtime.us-east-1.amazonaws.com/model/anthropic.claude-3-5-sonnet-20241022-v2:0/converse" {
		t.Fatalf("endpoint = %s", upReq.URL.String())
	}
	if !strings.HasPrefix(upReq.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=AK/") {
		t.Fatalf("missing/bad SigV4 auth: %q", upReq.Header.Get("Authorization"))
	}
	if upReq.Header.Get("X-Amz-Date") == "" {
		t.Fatal("missing X-Amz-Date")
	}
	body, _ := io.ReadAll(upReq.Body)
	var b map[string]any
	if json.Unmarshal(body, &b) != nil {
		t.Fatalf("body not json: %s", body)
	}
	// Converse 形态：system 提顶层、messages 为 content 数组、inferenceConfig
	sys := b["system"].([]any)
	if len(sys) != 1 || sys[0].(map[string]any)["text"] != "be brief" {
		t.Fatalf("system = %v", b["system"])
	}
	msgs := b["messages"].([]any)
	if len(msgs) != 1 { // system 不进 messages
		t.Fatalf("messages = %v", b["messages"])
	}
	inf := b["inferenceConfig"].(map[string]any)
	if inf["maxTokens"] != float64(256) || inf["temperature"] != float64(0.7) {
		t.Fatalf("inferenceConfig = %v", inf)
	}
	// 流式端点
	upReq2, _ := bedrockAdapter{}.buildRequest(context.Background(), ch, "AK|SK|us-east-1", req, true)
	if !strings.HasSuffix(upReq2.URL.Path, "/converse-stream") {
		t.Fatalf("stream endpoint = %s", upReq2.URL.Path)
	}
}

func TestBedrockPipeJSON(t *testing.T) {
	c, rec := newSSECtx()
	c.Set(respModelKey, "anthropic.claude-3-5-sonnet-20241022-v2:0") // relay 存对外 model 名
	body := `{"output":{"message":{"role":"assistant","content":[{"text":"Hello "},{"text":"there"}]}},` +
		`"stopReason":"end_turn","usage":{"inputTokens":10,"outputTokens":5,"totalTokens":15}}`
	u, _, err := bedrockAdapter{}.pipeJSON(c, strings.NewReader(body))
	if err != nil {
		t.Fatalf("pipeJSON: %v", err)
	}
	if u.PromptTokens != 10 || u.CompletionTokens != 5 {
		t.Fatalf("usage = %+v", u)
	}
	var resp struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if json.Unmarshal(rec.Body.Bytes(), &resp) != nil || len(resp.Choices) == 0 {
		t.Fatalf("bad response: %s", rec.Body.String())
	}
	if resp.Model != "anthropic.claude-3-5-sonnet-20241022-v2:0" {
		t.Fatalf("model echo = %q, want the outward name (不应为空)", resp.Model)
	}
	if resp.Choices[0].Message.Content != "Hello there" {
		t.Fatalf("content = %q (多 content 块应拼接)", resp.Choices[0].Message.Content)
	}
	if resp.Choices[0].FinishReason != "stop" {
		t.Fatalf("finish = %q", resp.Choices[0].FinishReason)
	}
}

// 异常帧（:message-type=exception，无 :event-type）→ pipeSSE 返回 error + emitStreamError，
// 不把限流/模型错误当成功（对抗 review medium）。
func TestBedrockStreamException(t *testing.T) {
	c, rec := newSSECtx()
	var stream bytes.Buffer
	stream.Write(awsEventFrame("contentBlockDelta", []byte(`{"delta":{"text":"partial"}}`)))
	stream.Write(awsExceptionFrame("throttlingException"))
	_, _, err := bedrockAdapter{}.pipeSSE(c, &stream)
	if err == nil {
		t.Fatal("exception frame must yield stream error (not silent success)")
	}
	out := rec.Body.String()
	if !strings.Contains(out, "upstream_error") {
		t.Fatalf("client should get error frame, got:\n%s", out)
	}
}

func TestBedrockPipeSSE(t *testing.T) {
	c, rec := newSSECtx()
	var stream bytes.Buffer
	stream.Write(awsEventFrame("messageStart", []byte(`{"role":"assistant"}`)))
	stream.Write(awsEventFrame("contentBlockDelta", []byte(`{"delta":{"text":"Hel"}}`)))
	stream.Write(awsEventFrame("contentBlockDelta", []byte(`{"delta":{"text":"lo"}}`)))
	stream.Write(awsEventFrame("messageStop", []byte(`{"stopReason":"end_turn"}`)))
	stream.Write(awsEventFrame("metadata", []byte(`{"usage":{"inputTokens":8,"outputTokens":3}}`)))

	u, _, err := bedrockAdapter{}.pipeSSE(c, &stream)
	if err != nil {
		t.Fatalf("pipeSSE: %v", err)
	}
	if u.PromptTokens != 8 || u.CompletionTokens != 3 {
		t.Fatalf("stream usage = %+v", u)
	}
	out := rec.Body.String()
	if !strings.Contains(out, `"content":"Hel"`) || !strings.Contains(out, `"content":"lo"`) {
		t.Fatalf("stream text deltas missing:\n%s", out)
	}
	if !strings.Contains(out, `"finish_reason":"stop"`) {
		t.Fatalf("stream finish missing:\n%s", out)
	}
	if !strings.Contains(out, "data: [DONE]") {
		t.Fatalf("stream not terminated:\n%s", out)
	}
}

func TestDecodeAWSEventStreamTruncated(t *testing.T) {
	// 半个 prelude（读到 1-11 字节后断）= 真截断 → 返回 error（不当干净 EOF，对抗 review low）
	half := awsEventFrame("contentBlockDelta", []byte(`{"delta":{"text":"x"}}`))[:6]
	var got []string
	err := decodeAWSEventStream(context.Background(), bytes.NewReader(half), func(et string, _ []byte) {
		got = append(got, et)
	})
	if err == nil {
		t.Fatal("truncated prelude must return error, not silent nil")
	}
	if len(got) != 0 {
		t.Fatalf("truncated frame should yield no events, got %v", got)
	}
	// 干净结束（0 字节）= nil
	if e := decodeAWSEventStream(context.Background(), bytes.NewReader(nil), func(string, []byte) {}); e != nil {
		t.Fatalf("clean EOF should be nil, got %v", e)
	}
}

// awsExceptionFrame：构造异常帧（:message-type=exception + :exception-type=<name>，无 :event-type）。
func awsExceptionFrame(exceptionType string) []byte {
	var hdr bytes.Buffer
	writeStrHeader(&hdr, ":message-type", "exception")
	writeStrHeader(&hdr, ":exception-type", exceptionType)
	headers := hdr.Bytes()
	payload := []byte(`{"message":"rate exceeded"}`)
	totalLen := 12 + len(headers) + len(payload) + 4
	var buf bytes.Buffer
	_ = binary.Write(&buf, binary.BigEndian, uint32(totalLen))
	_ = binary.Write(&buf, binary.BigEndian, uint32(len(headers)))
	_ = binary.Write(&buf, binary.BigEndian, uint32(0))
	buf.Write(headers)
	buf.Write(payload)
	_ = binary.Write(&buf, binary.BigEndian, uint32(0))
	return buf.Bytes()
}

func writeStrHeader(b *bytes.Buffer, name, val string) {
	b.WriteByte(byte(len(name)))
	b.WriteString(name)
	b.WriteByte(7)
	_ = binary.Write(b, binary.BigEndian, uint16(len(val)))
	b.WriteString(val)
}

// awsEventFrame：构造一个 AWS event stream 帧（测试用；CRC 填 0，解码器不校验 CRC）。
func awsEventFrame(eventType string, payload []byte) []byte {
	name := ":event-type"
	var hdr bytes.Buffer
	hdr.WriteByte(byte(len(name)))
	hdr.WriteString(name)
	hdr.WriteByte(7) // string value type
	_ = binary.Write(&hdr, binary.BigEndian, uint16(len(eventType)))
	hdr.WriteString(eventType)
	headers := hdr.Bytes()

	totalLen := 12 + len(headers) + len(payload) + 4
	var buf bytes.Buffer
	_ = binary.Write(&buf, binary.BigEndian, uint32(totalLen))
	_ = binary.Write(&buf, binary.BigEndian, uint32(len(headers)))
	_ = binary.Write(&buf, binary.BigEndian, uint32(0)) // prelude CRC (unchecked)
	buf.Write(headers)
	buf.Write(payload)
	_ = binary.Write(&buf, binary.BigEndian, uint32(0)) // msg CRC (unchecked)
	return buf.Bytes()
}
