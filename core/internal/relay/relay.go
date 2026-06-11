package relay

import (
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
	maxAttempts        = 3 // 1 次 + 2 次换渠道重试（知识库 §1）
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

	// 3. 定价
	price, err := GetPrice(r.DB, model)
	if err != nil {
		relayErr(c, http.StatusBadRequest, fmt.Sprintf("model %q is not available", model))
		return
	}

	// 4. Hold：按 估算输入 + max_tokens 上限 冻结（整个重试周期只 hold 一次）
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

	// 5. 换渠道重试循环（知识库 §1/§2：分类驱动；hold 跨重试只此一份）。
	// 请求体已在内存（req map），每次 attempt 由 adapter 重建上游请求——天然可重放，
	// 不需要 NewAPI 的磁盘 BufStorage。
	var resp *http.Response
	var ch *Channel
	exclude := map[uint64]bool{}
	var lastStatus int
	var lastBody []byte
	for attempt := 0; attempt < maxAttempts; attempt++ {
		var pickErr error
		ch, pickErr = PickChannelForAttempt(r.DB, model, attempt, exclude)
		if pickErr != nil {
			break // 无渠道可用：落到循环后的失败处理
		}
		credential, err := r.Keyring.Decrypt(ch.CredentialEnc)
		if err != nil {
			r.Log.Error("channel credential decrypt failed", "channel", ch.ID, "err", err)
			exclude[ch.ID] = true
			lastStatus, lastBody = http.StatusServiceUnavailable, []byte(`{"error":{"message":"upstream configuration error"}}`)
			continue // 我方配置错误（知识库 channel: 前缀类）→ 换渠道
		}
		ad := adapterFor(ch.Provider)
		upReq, err := ad.buildRequest(c.Request.Context(), ch, credential, req, stream)
		if err != nil {
			r.release(hold.ID)
			relayErr(c, http.StatusInternalServerError, "upstream request build failed")
			return // 请求本身不合法，换渠道无意义
		}
		resp, err = r.Client.Do(upReq)
		if err != nil {
			r.Log.Warn("upstream unreachable", "channel", ch.ID, "attempt", attempt, "err", err)
			exclude[ch.ID] = true
			lastStatus, lastBody = http.StatusBadGateway, []byte(`{"error":{"message":"upstream unreachable"}}`)
			resp = nil
			continue // 网络不可达 → 换渠道
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			break // 成功
		}
		// 非 2xx：分类 → 禁用/重试决策
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
		resp.Body.Close()
		disp := classifyUpstreamError(ch.Provider, resp.StatusCode, errBody)
		r.Log.Warn("upstream error", "channel", ch.ID, "attempt", attempt, "status", resp.StatusCode,
			"disable", disp.DisableChannel, "retryable", disp.Retryable, "body", string(errBody))
		if disp.DisableChannel {
			// 异步禁用，不阻塞响应（知识库 §2.2：不能用 request context，直接传 channel id）
			r.disableChannel(ch.ID, disp.Reason)
		}
		lastStatus, lastBody = resp.StatusCode, errBody
		resp = nil
		if !disp.Retryable {
			break // 400/408/504/524 等：重试无意义，直接透传
		}
		exclude[ch.ID] = true
	}
	if resp == nil {
		// 所有尝试失败：release 全额解冻，透传最后一个上游错误（或无渠道）
		r.release(hold.ID)
		if lastStatus == 0 {
			relayErr(c, http.StatusServiceUnavailable, "no upstream available for this model")
			return
		}
		c.Data(lastStatus, "application/json", lastBody)
		return
	}
	defer resp.Body.Close()
	ad := adapterFor(ch.Provider)

	// 成功响应：若渠道曾被 auto-disabled，自动重启用（知识库 §2.2）
	r.enableChannelIfDisabled(ch)

	// 7. 响应回传（adapter 转换）+ usage 抽取 + capture
	var u usage
	var outBytes int
	if stream {
		u, outBytes = ad.pipeSSE(c, resp.Body)
	} else {
		u, outBytes = ad.pipeJSON(c, resp.Body)
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

func (r *Relay) release(holdID uint64) {
	if err := r.DB.Transaction(func(tx *gorm.DB) error {
		return ledger.Release(tx, holdID)
	}); err != nil {
		r.Log.Error("RELEASE FAILED — funds stuck in hold", "hold", holdID, "err", err)
	}
}

// disableChannel 异步禁用渠道（知识库 §2.2）。一次成功请求会自动重启用（见 enableChannelIfDisabled）。
// 用独立 goroutine + 新 context，不依赖已可能结束的请求 context。
func (r *Relay) disableChannel(channelID uint64, reason string) {
	go func() {
		if err := r.DB.Model(&Channel{}).Where("id = ? AND status = 1", channelID).
			Update("status", 2).Error; err != nil { // 2 = auto-disabled
			r.Log.Error("channel auto-disable failed", "channel", channelID, "err", err)
			return
		}
		r.Log.Warn("channel auto-disabled", "channel", channelID, "reason", reason)
	}()
}

// enableChannelIfDisabled：成功请求后，若渠道处于 auto-disabled 则自动重启用（知识库 §2.2）。
func (r *Relay) enableChannelIfDisabled(ch *Channel) {
	if ch.Status != 2 {
		return
	}
	go func() {
		if err := r.DB.Model(&Channel{}).Where("id = ? AND status = 2", ch.ID).
			Update("status", 1).Error; err == nil {
			r.Log.Info("channel auto-re-enabled after success", "channel", ch.ID)
		}
	}()
}

func relayErr(c *gin.Context, code int, msg string) {
	c.AbortWithStatusJSON(code, gin.H{"error": gin.H{"message": msg, "type": "laiai_relay_error"}})
}
