package relay

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// thinkingToContentWriter transforms OpenAI canonical JSON/SSE reasoning_content
// into inline <think>...</think> blocks inside message/delta content when enabled per channel.
type thinkingToContentWriter struct {
	gin.ResponseWriter
	stream      bool
	status      int
	buf         bytes.Buffer
	sseStarted  bool
	pending     bytes.Buffer
	inThink     bool
	thinkClosed bool
}

func (w *thinkingToContentWriter) Status() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

func (w *thinkingToContentWriter) WriteHeader(code int) { w.status = code }
func (w *thinkingToContentWriter) WriteHeaderNow()      {}

func (w *thinkingToContentWriter) Write(b []byte) (int, error)       { return w.ingest(b) }
func (w *thinkingToContentWriter) WriteString(s string) (int, error) { return w.ingest([]byte(s)) }

func (w *thinkingToContentWriter) Flush() {
	if w.stream && w.Status() < 400 {
		w.pumpSSE()
		w.ResponseWriter.Flush()
	}
}

func (w *thinkingToContentWriter) ingest(b []byte) (int, error) {
	if w.Status() >= 400 {
		return w.buf.Write(b)
	}
	if w.stream {
		if !w.sseStarted {
			w.sseStarted = true
			w.ResponseWriter.WriteHeader(http.StatusOK)
		}
		n, _ := w.pending.Write(b)
		w.pumpSSE()
		return n, nil
	}
	return w.buf.Write(b)
}

func (w *thinkingToContentWriter) pumpSSE() {
	for {
		line, err := w.pending.ReadString('\n')
		if err != nil {
			w.pending.Reset()
			w.pending.WriteString(line)
			return
		}
		trimmed := strings.TrimRight(line, "\r\n")
		data, ok := strings.CutPrefix(trimmed, "data: ")
		if !ok || data == "" || data == "[DONE]" {
			if data == "[DONE]" && w.inThink && !w.thinkClosed {
				w.emitSyntheticContentChunk("</think>\n\n")
				w.thinkClosed = true
				w.inThink = false
			}
			_, _ = w.ResponseWriter.WriteString(line)
			continue
		}
		transformed := w.transformSSEChunk(data)
		_, _ = w.ResponseWriter.WriteString("data: " + transformed + "\n")
	}
}

func marshalRawJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

func (w *thinkingToContentWriter) emitSyntheticContentChunk(text string) {
	chunk, _ := marshalRawJSON(map[string]any{
		"object": "chat.completion.chunk",
		"choices": []map[string]any{{
			"index": 0,
			"delta": map[string]any{"content": text},
		}},
	})
	_, _ = w.ResponseWriter.WriteString("data: " + string(chunk) + "\n\n")
}

func (w *thinkingToContentWriter) transformSSEChunk(data string) string {
	var raw map[string]any
	if json.Unmarshal([]byte(data), &raw) != nil {
		return data
	}
	choices, ok := raw["choices"].([]any)
	if !ok || len(choices) == 0 {
		return data
	}
	modified := false
	for _, rc := range choices {
		ch, ok := rc.(map[string]any)
		if !ok {
			continue
		}
		delta, ok := ch["delta"].(map[string]any)
		if !ok {
			delta = nil
		}
		reasoning := ""
		if delta != nil {
			if s, ok := delta["reasoning_content"].(string); ok {
				reasoning = s
				delete(delta, "reasoning_content")
				modified = true
			}
		}
		content := ""
		hasContent := false
		if delta != nil {
			if s, ok := delta["content"].(string); ok && s != "" {
				content = s
				hasContent = true
			}
		}
		hasTool := delta != nil && delta["tool_calls"] != nil
		fin, _ := ch["finish_reason"].(string)

		var outText strings.Builder
		if reasoning != "" {
			if !w.inThink {
				outText.WriteString("<think>")
				w.inThink = true
				w.thinkClosed = false
			}
			outText.WriteString(reasoning)
		}
		if (hasContent || hasTool || fin != "") && w.inThink && !w.thinkClosed {
			outText.WriteString("</think>\n\n")
			w.thinkClosed = true
			w.inThink = false
		}
		if hasContent {
			outText.WriteString(content)
		}
		if outText.Len() > 0 && delta != nil {
			delta["content"] = outText.String()
			modified = true
		}
	}
	if !modified {
		return data
	}
	out, err := marshalRawJSON(raw)
	if err != nil {
		return data
	}
	return string(out)
}

func (w *thinkingToContentWriter) finalize() {
	if w.Status() >= 400 {
		h := w.ResponseWriter.Header()
		b := w.buf.Bytes()
		h.Set("Content-Length", strconv.Itoa(len(b)))
		w.ResponseWriter.WriteHeader(w.Status())
		_, _ = w.ResponseWriter.Write(b)
		return
	}
	if w.stream {
		w.pumpSSE()
		if w.pending.Len() > 0 {
			_, _ = w.ResponseWriter.Write(w.pending.Bytes())
			w.pending.Reset()
		}
		w.ResponseWriter.Flush()
		return
	}
	out := transformJSONThinkingToContent(w.buf.Bytes())
	h := w.ResponseWriter.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Content-Length", strconv.Itoa(len(out)))
	w.ResponseWriter.WriteHeader(http.StatusOK)
	_, _ = w.ResponseWriter.Write(out)
}

func transformJSONThinkingToContent(raw []byte) []byte {
	var parsed map[string]any
	if json.Unmarshal(raw, &parsed) != nil {
		return raw
	}
	choices, ok := parsed["choices"].([]any)
	if !ok || len(choices) == 0 {
		return raw
	}
	changed := false
	for _, rc := range choices {
		ch, ok := rc.(map[string]any)
		if !ok {
			continue
		}
		msg, ok := ch["message"].(map[string]any)
		if !ok {
			continue
		}
		reasoning, _ := msg["reasoning_content"].(string)
		if reasoning == "" {
			continue
		}
		delete(msg, "reasoning_content")
		existing, _ := msg["content"].(string)
		msg["content"] = "<think>" + reasoning + "</think>\n\n" + existing
		changed = true
	}
	if !changed {
		return raw
	}
	out, err := marshalRawJSON(parsed)
	if err != nil {
		return raw
	}
	return out
}
