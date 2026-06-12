// smoke：上游边界冒烟验证器（fixture 纪律的"真实 raw response"环节工具化）。
//
// 测的不是转换逻辑（单测 fixture 已覆盖），是 mock 永远无法证明的边界：
// 端点可达、认证被接受、响应可解析、usage 存在、计费精确落账。
//
//	LAIAI_URL=https://api.example.com LAIAI_SMOKE_KEY=sk-laiai-xxx \
//	  go run ./cmd/smoke -models gpt-4o-mini,claude-3-5-haiku,gemini-2.0-flash
//
// 前置：渠道与定价已经 admin API 配好、LAIAI_SMOKE_KEY 所属 org 已授信。
// 每个模型打一次非流式 + 一次流式真实调用，输出表格 + 非零退出码（CI 可用）。
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

type result struct {
	Model     string
	Mode      string
	OK        bool
	Status    int
	RequestID string
	InTokens  int64
	OutTokens int64
	FRT       time.Duration
	Err       string
}

func main() {
	models := flag.String("models", "", "逗号分隔的待验证模型列表（必填）")
	timeout := flag.Duration("timeout", 90*time.Second, "单次调用超时")
	flag.Parse()

	baseURL := strings.TrimRight(os.Getenv("LAIAI_URL"), "/")
	apiKey := os.Getenv("LAIAI_SMOKE_KEY")
	if baseURL == "" || apiKey == "" || *models == "" {
		fmt.Fprintln(os.Stderr, "usage: LAIAI_URL=... LAIAI_SMOKE_KEY=... smoke -models m1,m2,...")
		os.Exit(1)
	}
	client := &http.Client{Timeout: *timeout}

	var results []result
	failed := 0
	for _, model := range strings.Split(*models, ",") {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		for _, stream := range []bool{false, true} {
			r := smokeOne(client, baseURL, apiKey, model, stream)
			results = append(results, r)
			if !r.OK {
				failed++
			}
		}
	}

	fmt.Printf("\n%-28s %-7s %-5s %-6s %-8s %-8s %-9s %s\n",
		"MODEL", "MODE", "OK", "HTTP", "IN_TOK", "OUT_TOK", "FRT", "REQUEST_ID / ERROR")
	fmt.Println(strings.Repeat("-", 110))
	for _, r := range results {
		okStr := "PASS"
		detail := r.RequestID
		if !r.OK {
			okStr = "FAIL"
			detail = r.Err
		}
		fmt.Printf("%-28s %-7s %-5s %-6d %-8d %-8d %-9s %s\n",
			r.Model, r.Mode, okStr, r.Status, r.InTokens, r.OutTokens,
			r.FRT.Round(time.Millisecond), detail)
	}
	fmt.Printf("\n%d checks, %d failed\n", len(results), failed)
	if failed > 0 {
		os.Exit(2)
	}
}

func smokeOne(client *http.Client, baseURL, apiKey, model string, stream bool) result {
	mode := "json"
	if stream {
		mode = "stream"
	}
	r := result{Model: model, Mode: mode}

	body, _ := json.Marshal(map[string]any{
		"model": model, "stream": stream, "max_tokens": 32,
		"messages": []map[string]string{{"role": "user", "content": "Reply with the single word: pong"}},
	})
	req, err := http.NewRequest(http.MethodPost, baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		r.Err = err.Error()
		return r
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		r.Err = "unreachable: " + err.Error()
		return r
	}
	defer resp.Body.Close()
	r.Status = resp.StatusCode
	r.RequestID = resp.Header.Get("X-Request-Id")
	if resp.StatusCode != http.StatusOK {
		raw := make([]byte, 512)
		n, _ := resp.Body.Read(raw)
		r.Err = strings.TrimSpace(string(raw[:n]))
		return r
	}

	if stream {
		sawContent, sawDone := false, false
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 64*1024), 1<<20)
		for scanner.Scan() {
			line := scanner.Text()
			if r.FRT == 0 && line != "" {
				r.FRT = time.Since(start)
			}
			data, ok := strings.CutPrefix(line, "data: ")
			if !ok {
				continue
			}
			if data == "[DONE]" {
				sawDone = true
				continue
			}
			var chunk struct {
				Choices []struct {
					Delta struct {
						Content string `json:"content"`
					} `json:"delta"`
				} `json:"choices"`
				Usage *struct {
					PromptTokens     int64 `json:"prompt_tokens"`
					CompletionTokens int64 `json:"completion_tokens"`
				} `json:"usage"`
			}
			if json.Unmarshal([]byte(data), &chunk) != nil {
				continue
			}
			if len(chunk.Choices) > 0 && chunk.Choices[0].Delta.Content != "" {
				sawContent = true
			}
			if chunk.Usage != nil {
				r.InTokens, r.OutTokens = chunk.Usage.PromptTokens, chunk.Usage.CompletionTokens
			}
		}
		switch {
		case !sawContent:
			r.Err = "stream produced no content delta"
		case !sawDone:
			r.Err = "stream missing [DONE]"
		case r.OutTokens == 0:
			r.Err = "stream missing usage (billing falls back to estimate — check provider)"
		default:
			r.OK = true
		}
		return r
	}

	r.FRT = time.Since(start)
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int64 `json:"prompt_tokens"`
			CompletionTokens int64 `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		r.Err = "response not parseable: " + err.Error()
		return r
	}
	r.InTokens, r.OutTokens = parsed.Usage.PromptTokens, parsed.Usage.CompletionTokens
	switch {
	case len(parsed.Choices) == 0 || parsed.Choices[0].Message.Content == "":
		r.Err = "empty completion content"
	case r.OutTokens == 0:
		r.Err = "missing usage (billing falls back to estimate — check provider)"
	default:
		r.OK = true
	}
	return r
}
