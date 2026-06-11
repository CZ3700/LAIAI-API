package relay

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/crypto"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"gorm.io/gorm"
)

const (
	defaultMaxTokens   = 4096
	upstreamTimeout    = 5 * time.Minute
	maxErrorBodyBytes  = 2048 // 超大上游错误体截断（NewAPI rc.10 教训）
	maxRequestBodySize = 10 << 20
)

type Relay struct {
	DB      *gorm.DB
	Keyring *crypto.Keyring
	Client  *http.Client
	Log     *slog.Logger
	Now     func() time.Time
}

func New(db *gorm.DB, kr *crypto.Keyring, log *slog.Logger) *Relay {
	return &Relay{
		DB:      db,
		Keyring: kr,
		Client:  &http.Client{Timeout: upstreamTimeout},
		Log:     log,
		Now:     time.Now,
	}
}

type usage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
}

// HandleChatCompletions：POST /v1/chat/completions
// 计费时序 = hold（按估算上限冻结）→ 转发 → capture（按真实 usage 结算，差额自动解冻）。
// 上游失败 → release 全额解冻。结构性消灭"预检黑盒"类问题（TrustQuota 事故）。
func (r *Relay) HandleChatCompletions(c *gin.Context) {
	// 1. API Key 认证
	auth := c.GetHeader("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		relayErr(c, http.StatusUnauthorized, "missing api key")
		return
	}
	key, err := identity.VerifyKey(r.DB, strings.TrimPrefix(auth, "Bearer "), r.Now())
	if err != nil {
		relayErr(c, http.StatusUnauthorized, "invalid api key")
		return
	}

	// 2. 读请求体，取 model / stream / max_tokens
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, maxRequestBodySize))
	if err != nil {
		relayErr(c, http.StatusBadRequest, "cannot read request body")
		return
	}
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		relayErr(c, http.StatusBadRequest, "invalid json")
		return
	}
	model, _ := req["model"].(string)
	if model == "" {
		relayErr(c, http.StatusBadRequest, "model is required")
		return
	}
	stream, _ := req["stream"].(bool)
	maxTokens := int64(defaultMaxTokens)
	if mt, ok := req["max_tokens"].(float64); ok && mt > 0 {
		maxTokens = int64(mt)
	}

	// 3. 定价与渠道
	price, err := GetPrice(r.DB, model)
	if err != nil {
		relayErr(c, http.StatusBadRequest, fmt.Sprintf("model %q is not available", model))
		return
	}
	ch, err := PickChannel(r.DB, model)
	if err != nil {
		relayErr(c, http.StatusServiceUnavailable, "no upstream available for this model")
		return
	}
	credential, err := r.Keyring.Decrypt(ch.CredentialEnc)
	if err != nil {
		r.Log.Error("channel credential decrypt failed", "channel", ch.ID, "err", err)
		relayErr(c, http.StatusServiceUnavailable, "upstream configuration error")
		return
	}

	// 4. Hold：按 估算输入 + max_tokens 上限 冻结
	estIn := EstimateTokens(len(body))
	holdAmount := price.Cost(estIn, maxTokens)
	var hold *ledger.Hold
	err = r.DB.Transaction(func(tx *gorm.DB) error {
		acc, err := ledger.EnsureAccount(tx, key.OrgID, ledger.AccountUserCredit)
		if err != nil {
			return err
		}
		h, _, err := ledger.CreateHold(tx, ledger.HoldInput{
			IdempotencyKey: fmt.Sprintf("relay:%d:%d", key.ID, r.Now().UnixNano()),
			AccountID:      acc.ID,
			Amount:         holdAmount,
			RefType:        "usage",
			RefID:          model,
		})
		hold = h
		return err
	})
	if err != nil {
		if strings.Contains(err.Error(), "insufficient") {
			relayErr(c, http.StatusPaymentRequired, "insufficient balance")
			return
		}
		r.Log.Error("hold failed", "err", err)
		relayErr(c, http.StatusInternalServerError, "billing error")
		return
	}

	// 5. 流式请求注入 include_usage（usage 三档抽取第一档）
	if stream {
		req["stream_options"] = map[string]any{"include_usage": true}
		if body, err = json.Marshal(req); err != nil {
			r.release(hold.ID)
			relayErr(c, http.StatusInternalServerError, "request rebuild failed")
			return
		}
	}

	// 6. 转发上游
	upReq, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost,
		strings.TrimRight(ch.BaseURL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		r.release(hold.ID)
		relayErr(c, http.StatusInternalServerError, "upstream request build failed")
		return
	}
	upReq.Header.Set("Content-Type", "application/json")
	upReq.Header.Set("Authorization", "Bearer "+credential)

	resp, err := r.Client.Do(upReq)
	if err != nil {
		r.release(hold.ID)
		r.Log.Warn("upstream unreachable", "channel", ch.ID, "err", err)
		relayErr(c, http.StatusBadGateway, "upstream unreachable")
		return
	}
	defer resp.Body.Close()

	// 上游非 2xx：release 并透传（错误体截断后记日志）
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		r.release(hold.ID)
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
		r.Log.Warn("upstream error", "channel", ch.ID, "status", resp.StatusCode,
			"body", string(errBody))
		c.Data(resp.StatusCode, "application/json", errBody)
		return
	}

	// 7. 响应回传 + usage 抽取 + capture
	var u usage
	var outBytes int
	if stream {
		u, outBytes = r.pipeSSE(c, resp.Body)
	} else {
		u, outBytes = r.pipeJSON(c, resp.Body)
	}
	if u.PromptTokens == 0 && u.CompletionTokens == 0 {
		// 第三档兜底：上游没给 usage，按字节估算（宁可少收不多收：输入用请求估算，输出用响应字节）
		u.PromptTokens = estIn
		u.CompletionTokens = EstimateTokens(outBytes)
		r.Log.Warn("usage missing from upstream, estimated", "channel", ch.ID, "model", model)
	}

	actual := price.Cost(u.PromptTokens, u.CompletionTokens)
	reason := fmt.Sprintf("model=%s in=%d out=%d", model, u.PromptTokens, u.CompletionTokens)
	if err := r.DB.Transaction(func(tx *gorm.DB) error {
		if actual.GreaterThan(hold.Amount) {
			// Overage：估算被真实用量击穿。journal 必须记录真实成本——
			// capture 全额 hold + 差额补一笔允许透支的 debit（轻微负余额下次充值补回）。
			// 绝不"封顶少收"（那是系统性漏收）。
			if _, err := ledger.Capture(tx, hold.ID, hold.Amount, reason); err != nil {
				return err
			}
			overage := actual.Sub(hold.Amount)
			r.Log.Warn("usage overage beyond hold", "hold", hold.Amount, "actual", actual, "overage", overage)
			_, _, err := ledger.Post(tx, ledger.PostInput{
				IdempotencyKey: fmt.Sprintf("overage:%d", hold.ID),
				AccountID:      hold.AccountID,
				Kind:           ledger.KindDebit,
				Delta:          overage.Neg(),
				RefType:        "usage_overage",
				RefID:          model,
				Reason:         reason,
				AllowNegative:  true,
			})
			return err
		}
		_, err := ledger.Capture(tx, hold.ID, actual, reason)
		return err
	}); err != nil {
		// 响应已发出，结算失败只能告警人工对账（journal 漂移自检会兜住）
		r.Log.Error("SETTLE FAILED — manual reconcile needed", "hold", hold.ID, "err", err)
	}
}

// pipeJSON：非流式——读完上游响应转发，抽 usage（第二档）。
func (r *Relay) pipeJSON(c *gin.Context, body io.Reader) (usage, int) {
	raw, err := io.ReadAll(body)
	if err != nil {
		return usage{}, 0
	}
	var parsed struct {
		Usage usage `json:"usage"`
	}
	_ = json.Unmarshal(raw, &parsed)
	c.Data(http.StatusOK, "application/json", raw)
	return parsed.Usage, len(raw)
}

// pipeSSE：流式——逐行透传并 flush，扫描末帧 usage（第一档）。
func (r *Relay) pipeSSE(c *gin.Context, body io.Reader) (usage, int) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Status(http.StatusOK)
	w := c.Writer

	var u usage
	outBytes := 0
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		outBytes += len(line)
		if data, ok := strings.CutPrefix(line, "data: "); ok && data != "[DONE]" {
			var chunk struct {
				Usage *usage `json:"usage"`
			}
			if json.Unmarshal([]byte(data), &chunk) == nil && chunk.Usage != nil &&
				(chunk.Usage.PromptTokens > 0 || chunk.Usage.CompletionTokens > 0) {
				u = *chunk.Usage
			}
		}
		_, _ = w.WriteString(line + "\n")
		w.Flush()
	}
	return u, outBytes
}

func (r *Relay) release(holdID uint64) {
	if err := r.DB.Transaction(func(tx *gorm.DB) error {
		return ledger.Release(tx, holdID)
	}); err != nil {
		r.Log.Error("RELEASE FAILED — funds stuck in hold", "hold", holdID, "err", err)
	}
}

func relayErr(c *gin.Context, code int, msg string) {
	c.AbortWithStatusJSON(code, gin.H{"error": gin.H{"message": msg, "type": "laiai_relay_error"}})
}
