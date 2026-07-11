package relay

// forwardWithFailover：非 chat 模态（embeddings/rerank/images/images_edit/audio）共用的换渠道重试骨架
// （Adapter contract v2 第二期，商用迭代指令 §3.1；由 audioForward 泛化而来——此前五个 handler 各抄一份
// 逐字近似的 ~40 行循环）。收编 cachedChannels 快照 + pickFromSnapshot(健康分) + selectCredential(多 key)
// + 凭证解密 + build + clientFor(proxy/SSRF).Do + classifyUpstreamError 处置 + noteKeyFailure(禁 key) +
// exclude 换渠道 + 全失败释放 hold/透传上游错误。
//
// 行为保真（与 audioForward 逐字一致，红线：资金/流式/透传语义不变）：hold 只在**全失败**时释放一次
// （成功路径由调用方 settle）；build 返回 unsupported 哨兵 → 400 终止不换渠道；classify 的
// 禁渠道/可重试/透传语义、健康分喂点、noteKeyFailure(credID) 全部照旧。**唯一有意统一**：每次上游错误
// attempt 都记一条 debug 级 warn（此前仅 embeddings/images 有，audio/rerank/images_edit 补齐——纯观测增强）。

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/ledger"
)

// forwardOpts：一次非 chat 换渠道转发的模态参数。
type forwardOpts struct {
	model       string       // 选路模型名（对外名）
	hold        *ledger.Hold // 提交时冻结的 hold（全失败时 release）
	unsupported error        // build 返回它（==）→ 该 provider 不支持本模态，400 终止不换渠道
	modality    string       // 400 消息/finish brief 用："embeddings"/"rerank"/"audio"/"image generation"/"image editing"
	logLabel    string       // 日志前缀："embeddings"/"rerank"/"audio"/"images"/"image-edit"
	policy      string       // 路由策略（0043，resolveRoutePolicy 求解；空=order 默认加权随机）
	// build：为选定渠道 + 解密凭证构造上游请求（含 URL/鉴权/模型映射/header override）。
	// 返回 opts.unsupported → 400；其他错误 → 500 upstream request build failed。
	build func(ctx context.Context, ch *Channel, credential string) (*http.Request, error)
}

// forwardWithFailover：执行换渠道重试。返回 (resp, winCh, aborted)：
//   - aborted=true：已写完客户端响应 + finish + release，调用方**直接 return**（成功/失败均已收尾）。
//   - resp!=nil：winCh 上 2xx 成功，调用方继续做响应处理（strict 校验用 winCh）+ settle（hold 留给 settle）。
func (r *Relay) forwardWithFailover(c *gin.Context, inv *invocation.Invocation, finish func(string, int, string), o forwardOpts) (*http.Response, *Channel, bool) {
	var resp *http.Response
	exclude := map[uint64]bool{}
	var lastStatus int
	var lastBody []byte
	channelSnap, snapErr := r.cachedChannels() // 取一次快照，maxAttempts 次重试复用（WS-2）
	breaker := r.newBreakerPicker()            // 熔断闸（breaker.go）：与 chat 主循环同款
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if snapErr != nil {
			break
		}
		ch, pickErr := r.pickSnapshotPolicy(channelSnap, o.model, attempt, exclude, o.policy, inv.OrgID, nil)
		if pickErr != nil {
			if !breaker.reopen(exclude) {
				break
			}
			ch, pickErr = r.pickSnapshotPolicy(channelSnap, o.model, attempt, exclude, o.policy, inv.OrgID, nil)
			if pickErr != nil {
				break
			}
		}
		if !breaker.admit(ch.ID, exclude) {
			attempt-- // 熔断摘除不烧重试预算
			continue
		}
		inv.Attempts = attempt + 1
		inv.ChannelID, inv.Provider = ch.ID, ch.Provider
		credID, credEnc, credOK := r.selectCredential(ch) // 选 key（0034 Multi-key）
		if !credOK {
			r.Log.Error("channel has no usable credential", "channel", ch.ID)
			exclude[ch.ID] = true
			lastStatus, lastBody = http.StatusServiceUnavailable, []byte(`{"error":{"message":"upstream configuration error"}}`)
			continue
		}
		credential, e := r.Keyring.Decrypt(credEnc)
		if e != nil {
			r.Log.Error("channel credential decrypt failed", "channel", ch.ID, "cred", credID, "err", e)
			exclude[ch.ID] = true
			lastStatus, lastBody = http.StatusServiceUnavailable, []byte(`{"error":{"message":"upstream configuration error"}}`)
			continue
		}
		upReq, e := o.build(c.Request.Context(), ch, credential)
		if e != nil {
			// build 失败 = 请求不合法，不换渠道：释放 hold + 转译（unsupported→400，其余→500）。
			r.release(o.hold.ID)
			if e == o.unsupported {
				finish(invocation.StatusUpstreamError, http.StatusBadRequest, "provider lacks "+o.modality)
				relayErr(c, http.StatusBadRequest, fmt.Sprintf("provider %q does not support %s", ch.Provider, o.modality))
				return nil, nil, true
			}
			finish(invocation.StatusUpstreamError, http.StatusInternalServerError, "upstream request build failed")
			relayErr(c, http.StatusInternalServerError, "upstream request build failed")
			return nil, nil, true
		}
		resp, e = r.clientFor(ch).Do(upReq)
		if e != nil {
			// 下游 ctx 取消（客户端断连）不是渠道故障，不喂健康分/熔断并终止（对抗 review 修，同 chat 主循环）
			if c.Request.Context().Err() != nil {
				r.release(o.hold.ID)
				finish(invocation.StatusUpstreamError, 499, "client disconnected")
				return nil, nil, true
			}
			r.Log.Warn(o.logLabel+" upstream unreachable", "channel", ch.ID, "err", e)
			r.observe(ch.ID, false)     // 健康分：网络不可达
			r.breakerNote(ch.ID, false) // 熔断：不可达计一次
			exclude[ch.ID] = true
			lastStatus, lastBody = http.StatusBadGateway, []byte(`{"error":{"message":"upstream unreachable"}}`)
			resp = nil
			continue
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			r.enableChannelIfDisabled(ch)
			r.resetKeyFailures(credID)
			r.breakerNote(ch.ID, true) // 熔断：成功即闭合
			inv.UpstreamErrorCode = "" // 成功清前次失败尝试的错误码（对抗 review，同 chat 主循环）
			return resp, ch, false // 成功：winCh=ch，hold 留给调用方 settle
		}
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
		resp.Body.Close()
		r.breakerNote(ch.ID, resp.StatusCode < 500) // 熔断口径：仅 5xx 计失败
		disp := classifyUpstreamError(ch.Provider, resp.StatusCode, errBody)
		inv.UpstreamErrorCode = upstreamErrorCode(disp.Reason, errBody) // 排障闭环（0045）
		if healthPenalty(disp, resp.StatusCode) {
			r.observe(ch.ID, false) // 健康分：仅渠道整体可靠性失败（5xx/429/硬故障）；渠道特有 4xx 不计
		}
		r.Log.Warn(o.logLabel+" upstream error", "channel", ch.ID, "status", resp.StatusCode,
			"disable", disp.DisableChannel, "retryable", disp.Retryable)
		if disp.DisableChannel {
			r.noteSuggestDisable(ch.ID, disp.Reason)     // 硬故障 → 禁用建议（B-2，不自动禁）
			r.noteKeyFailure(credID, ch.ID, disp.Reason) // 多 key：禁 key 而非渠道（0034）
		}
		lastStatus, lastBody = resp.StatusCode, errBody
		resp = nil
		if !disp.Retryable {
			break
		}
		exclude[ch.ID] = true
	}
	// 全失败：释放 hold 并转译响应
	r.release(o.hold.ID)
	if lastStatus == 0 {
		finish(invocation.StatusNoChannel, http.StatusServiceUnavailable, "no upstream available")
		relayErr(c, http.StatusServiceUnavailable, "no upstream available for this model")
		return nil, nil, true
	}
	finish(invocation.StatusUpstreamError, lastStatus, fmt.Sprintf("upstream status %d after %d attempts", lastStatus, inv.Attempts))
	c.Data(lastStatus, "application/json", lastBody)
	return nil, nil, true
}
