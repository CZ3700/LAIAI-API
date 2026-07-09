package relay

// Realtime WebSocket relay（商用迭代指令 §3.9；设计 §4.4）。知识移植自 New API
// relay/channel/openai/relay-openai.go（协议/计费/生命周期知识，禁止照抄架构）。
//
// /v1/realtime?model=... 双向 WS 网关：客户端 OpenAI Realtime SDK 直连 → 我方透传帧到上游
// （OpenAI/Azure）→ 按上游 response.done 的权威 usage 逐轮计费。
//
// 计费（新资金模型，红线不破）：不开整会话 hold（时长/用量无上界）。建连预检余额；每个
// response.done 一次 capture（CreateHoldSharded+SettleHoldExact 同事务，走唯一写余额点，
// 幂等键 rt:<reqID>:<seq>）；余额不足回写 error 事件 + 拆连。断连/上游断/心跳超时都有明确
// 日志 + 结算路径。**v1 只认上游权威 usage**（不做本地估算 fallback→杜绝 New API 的双算 +
// flush race）：未走到 response.done 的中断响应不计费（bounded 少收，安全）。

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// Realtime 可调超时（导出供测试缩短，与 StreamChunkTimeout 同款）。
var (
	RealtimeDialTimeout = 15 * time.Second
	RealtimePingPeriod  = 20 * time.Second
	RealtimeIdleTimeout = 60 * time.Second // 无消息/无 pong 超过此值判假死（心跳保活刷新）
	// RealtimeWriteWait：单帧写超时（对抗 review CONFIRMED HIGH）——对端只写不读把 TCP 发送缓冲填满时，
	// 无写截止的 WriteMessage 会永久阻塞→泵 goroutine 卡死→绕过 idle/ctx 收敛→goroutine+FD 泄漏（DoS）。
	RealtimeWriteWait  = 10 * time.Second
	RealtimeMaxSession = 30 * time.Minute // 会话硬上限（防健康 idle 会话零计费长驻，收口 DoS 面）
	realtimeReadLimit  = int64(2 << 20)   // 单帧上限 2MB（音频 base64 帧较大）
)

// writeRealtimeFrame：带写截止的数据帧写——写阻塞（对端不读）在 RealtimeWriteWait 后报错，交由
// 调用方当作断连收敛，绝不永久卡死。
func writeRealtimeFrame(conn *websocket.Conn, mt int, msg []byte) error {
	_ = conn.SetWriteDeadline(time.Now().Add(RealtimeWriteWait))
	return conn.WriteMessage(mt, msg)
}

// AzureRealtimeAPIVersion：Azure realtime 默认 api-version（渠道级覆盖留待扩展）。
const AzureRealtimeAPIVersion = "2025-04-01-preview"

var realtimeUpgrader = websocket.Upgrader{
	Subprotocols:    []string{"realtime"}, // 只回 realtime，绝不回 openai-insecure-api-key.*（含 key）
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	// CheckOrigin：realtime 走 Bearer/子协议 key 鉴权（非 cookie），CSRF 面为零；放行跨域连接
	// 让浏览器 SDK 可用。鉴权在 upgrade 前已做（HandleRealtime）。
	CheckOrigin: func(*http.Request) bool { return true },
}

// realtimeSession：一次会话的计费/记录上下文。
type realtimeSession struct {
	orgID    uint64
	reqID    string
	model    string
	upstream string // 上游模型名（映射后）
	price    *ModelPrice
	ratio    decimal.Decimal
	seq      int             // response.done 计数（幂等键序号）
	total    RealtimeUsage   // 会话累计 usage（日志/记录用）
	cost     decimal.Decimal // 会话累计实收
}

// realtimeAuthKey：从 Sec-WebSocket-Protocol 的 openai-insecure-api-key.<sk> 段抽平台 key
// （浏览器 ws 无法自定义 header 的标准做法），否则回退 Authorization: Bearer。
func realtimeAuthKey(c *gin.Context) string {
	for _, proto := range websocket.Subprotocols(c.Request) {
		if k, ok := strings.CutPrefix(proto, "openai-insecure-api-key."); ok && k != "" {
			return k
		}
	}
	if auth := c.GetHeader("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimPrefix(auth, "Bearer ")
	}
	return ""
}

// HandleRealtime：GET /v1/realtime?model=... —— Realtime WS 网关。
func (r *Relay) HandleRealtime(c *gin.Context) {
	t0 := r.Now()
	// 1. 鉴权（upgrade 前——失败走普通 HTTP 401，不 upgrade）
	rawKey := realtimeAuthKey(c)
	if rawKey == "" {
		relayErr(c, http.StatusUnauthorized, "missing api key")
		return
	}
	key, err := identity.VerifyKey(r.DB.WithContext(c.Request.Context()), rawKey, r.Now())
	if err != nil {
		relayErr(c, http.StatusUnauthorized, "invalid api key")
		return
	}
	model := c.Query("model")
	if model == "" {
		relayErr(c, http.StatusBadRequest, "model query param is required")
		return
	}
	if code, msg, ok := r.gateKey(key, c.ClientIP(), model); !ok {
		relayErr(c, code, msg)
		return
	}
	if ok, _ := r.allowModelRate(key.OrgID, model); !ok {
		c.Header("Retry-After", "60")
		relayErr(c, http.StatusTooManyRequests, "model rate limit exceeded, slow down")
		return
	}
	price, err := r.cachedPrice(model)
	if err != nil {
		relayErr(c, http.StatusBadRequest, "model "+model+" is not available")
		return
	}
	// fail-closed（对抗 review CONFIRMED）：realtime 音频是 dominant 成本，未配音频价绝不放行——
	// 否则音频按文本价漏收 10-20×（红线4）。运营须显式配 audio_input/audio_output 价。
	if !price.RealtimeAudioPriced() {
		relayErr(c, http.StatusBadRequest, "model "+model+" is not priced for realtime audio (set audio_input_usd_per_1m and audio_output_usd_per_1m)")
		return
	}
	// 2. 建连预检余额（正余额门槛；不开整会话 hold）
	avail, err := ledger.OrgAvailable(r.DB, key.OrgID, ledger.AccountUserCredit)
	if err != nil || !avail.IsPositive() {
		relayErr(c, http.StatusPaymentRequired, "insufficient balance")
		return
	}
	// 3. 选渠道 + 凭证（realtime 用纯权重选路，不喂健康分）
	ch, err := PickChannelForAttempt(r.DB, model, 0, nil, nil)
	if err != nil {
		relayErr(c, http.StatusServiceUnavailable, "no upstream available for this model")
		return
	}
	credID, credEnc, ok := r.selectCredential(ch)
	if !ok {
		relayErr(c, http.StatusServiceUnavailable, "upstream configuration error")
		return
	}
	credential, err := r.Keyring.Decrypt(credEnc)
	if err != nil {
		r.Log.Error("realtime credential decrypt failed", "channel", ch.ID, "cred", credID)
		relayErr(c, http.StatusServiceUnavailable, "upstream configuration error")
		return
	}

	sess := &realtimeSession{
		orgID: key.OrgID, reqID: newRequestID(), model: model,
		upstream: ch.UpstreamModel(model), price: price,
		ratio: r.cachedRatio(key.OrgID, model), cost: decimal.Zero,
	}

	// 4. 先拨上游（失败则不 upgrade 客户端，直接 HTTP 502——比 upgrade 后再关体验好）
	upConn, err := r.dialRealtimeUpstream(c.Request.Context(), ch, credential, sess.upstream)
	if err != nil {
		r.Log.Warn("realtime upstream dial failed", "channel", ch.ID, "err", err)
		relayErr(c, http.StatusBadGateway, "upstream realtime unavailable")
		return
	}
	defer upConn.Close()

	// 5. upgrade 客户端
	clientConn, err := realtimeUpgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		r.Log.Warn("realtime client upgrade failed", "err", err)
		return // Upgrade 已写错误响应
	}
	defer clientConn.Close()

	r.Log.Info("realtime session opened", "req", sess.reqID, "org", sess.orgID, "model", model, "channel", ch.ID)

	// 6. 双向泵（含心跳/超时），阻塞至任一端关闭
	cause := r.pumpRealtime(c.Request.Context(), clientConn, upConn, sess)

	// 7. 会话终态：记 Invocation（脱敏，只 usage 数字）+ 日志（明确断连原因）
	r.enableChannelIfDisabled(ch)
	r.resetKeyFailures(credID)
	if r.Rec != nil {
		r.Rec.Record(&invocation.Invocation{
			OrgID: key.OrgID, UserID: key.UserID, APIKeyID: key.ID,
			RequestID: sess.reqID, Model: model, Modality: invocation.ModalityRealtime,
			ChannelID: ch.ID, Provider: ch.Provider,
			Status: invocation.StatusSuccess, StatusCode: http.StatusOK,
			PromptTokens:     sess.total.TextInput + sess.total.AudioInput,
			CompletionTokens: sess.total.TextOutput + sess.total.AudioOutput,
			CostUSD:          sess.cost, UsageSource: invocation.UsageSourceUpstream,
			DurationMs: int(r.Now().Sub(t0).Milliseconds()),
		})
	}
	r.Log.Info("realtime session closed", "req", sess.reqID, "cause", cause,
		"cost", sess.cost.String(), "turns", sess.seq)
}

// dialRealtimeUpstream：按 (OpenAI|Azure) 分叉构 wss URL + 鉴权头，带超时 + 渠道 proxy 拨号。
func (r *Relay) dialRealtimeUpstream(ctx context.Context, ch *Channel, credential, upModel string) (*websocket.Conn, error) {
	base := strings.TrimRight(ch.BaseURL, "/")
	wsURL := strings.Replace(strings.Replace(base, "https://", "wss://", 1), "http://", "ws://", 1)
	header := http.Header{}
	family := channelAdapterFamily(ch)
	esc := url.QueryEscape(upModel) // 模型名进 URL query 前转义（host 已 pin ch.BaseURL，纯防御）
	if family == "azure" {
		// Azure：/openai/realtime?deployment=<映射后模型>&api-version=；api-key 头
		wsURL += "/openai/realtime?deployment=" + esc + "&api-version=" + AzureRealtimeAPIVersion
		header.Set("api-key", credential)
	} else {
		// OpenAI 兼容：/realtime?model=；openai-beta + Bearer（后端直连 header 式，非子协议）
		wsURL += "/realtime?model=" + esc
		header.Set("Authorization", "Bearer "+credential)
		header.Set("OpenAI-Beta", "realtime=v1")
	}
	// 渠道级 header override（{{credential}} 占位；自定义认证上游）——复用 chat 同款
	applyHeaderOverride(&http.Request{Header: header}, ch.HeaderOverride, credential)

	dialer := &websocket.Dialer{
		HandshakeTimeout: RealtimeDialTimeout,
		Proxy:            wsProxyFor(ch), // 补 New API 缺口：wss 拨号支持渠道代理
	}
	dctx, cancel := context.WithTimeout(ctx, RealtimeDialTimeout)
	defer cancel()
	conn, _, err := dialer.DialContext(dctx, wsURL, header)
	return conn, err
}

// pumpRealtime：两 goroutine 双向泵 + 心跳/idle 超时；两端收敛后返回断连原因。
// 计费只在 upstream→client 方向的 response.done 触发（单 goroutine，无并发扣费）。
func (r *Relay) pumpRealtime(ctx context.Context, clientConn, upConn *websocket.Conn, sess *realtimeSession) string {
	clientConn.SetReadLimit(realtimeReadLimit)
	upConn.SetReadLimit(realtimeReadLimit)
	var closeOnce sync.Once
	closeBoth := func() { closeOnce.Do(func() { clientConn.Close(); upConn.Close() }) }
	// 心跳保活：pong 刷新读超时；ping ticker 主动探活。
	refresh := func(c *websocket.Conn) { _ = c.SetReadDeadline(r.Now().Add(RealtimeIdleTimeout)) }
	clientConn.SetPongHandler(func(string) error { refresh(clientConn); return nil })
	upConn.SetPongHandler(func(string) error { refresh(upConn); return nil })
	refresh(clientConn)
	refresh(upConn)

	var cause struct {
		sync.Mutex
		s string
	}
	setCause := func(s string) {
		cause.Lock()
		if cause.s == "" {
			cause.s = s
		}
		cause.Unlock()
	}

	var wg sync.WaitGroup
	wg.Add(2)
	// client → upstream（纯透传，保留 opcode）
	go func() {
		defer wg.Done()
		defer closeBoth()
		for {
			mt, msg, err := clientConn.ReadMessage()
			if err != nil {
				setCause(classifyWSClose(err, "client"))
				return
			}
			refresh(clientConn)
			if err := writeRealtimeFrame(upConn, mt, msg); err != nil {
				setCause("upstream_write_failed")
				return
			}
		}
	}()
	// upstream → client（透传 + response.done 计费）
	go func() {
		defer wg.Done()
		defer closeBoth()
		for {
			mt, msg, err := upConn.ReadMessage()
			if err != nil {
				setCause(classifyWSClose(err, "upstream"))
				return
			}
			refresh(upConn)
			// 计费：解析 response.done 的权威 usage（text 帧才含 JSON 事件）
			if mt == websocket.TextMessage {
				if u, ok := parseRealtimeDone(msg); ok {
					if err := r.chargeRealtimeTurn(sess, u); err != nil {
						// 余额不足/扣费失败：回写 error 事件 + 拆连（bounded：本轮已生成，最多少收一轮）
						r.Log.Warn("realtime charge failed, closing session", "req", sess.reqID, "err", err)
						writeRealtimeError(clientConn, "insufficient balance or billing error")
						setCause("billing_insufficient")
						return
					}
				}
			}
			if err := writeRealtimeFrame(clientConn, mt, msg); err != nil {
				setCause("client_write_failed")
				return
			}
		}
	}()
	// ping ticker（保活；两端 idle 时防中间设备断连 + 探活假死）+ 会话硬上限。
	done := make(chan struct{})
	maxTimer := time.NewTimer(RealtimeMaxSession)
	go func() {
		tk := time.NewTicker(RealtimePingPeriod)
		defer tk.Stop()
		defer maxTimer.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				setCause("client_context_done")
				closeBoth()
				return
			case <-maxTimer.C:
				setCause("max_session_duration") // 会话超硬上限：主动关（收口零计费长驻 DoS）
				closeBoth()
				return
			case <-tk.C:
				// 控制帧写截止用 wall-clock（net.Conn deadline 不受 r.Now mock 影响）
				dl := time.Now().Add(5 * time.Second)
				_ = clientConn.WriteControl(websocket.PingMessage, nil, dl)
				_ = upConn.WriteControl(websocket.PingMessage, nil, dl)
			}
		}
	}()
	wg.Wait()
	close(done)
	if ctx.Err() != nil {
		setCause("client_context_done")
	}
	cause.Lock()
	defer cause.Unlock()
	if cause.s == "" {
		return "closed"
	}
	return cause.s
}

// chargeRealtimeTurn：一轮 response.done 的 capture。CreateHoldSharded+SettleHoldExact 同事务
// （走唯一写余额点 applyBalanceDelta，红线1；幂等键 rt:<reqID>:<seq> 是这里的幂等闸，红线2）。
// 余额不足冒泡 ErrInsufficientBalance。
func (r *Relay) chargeRealtimeTurn(sess *realtimeSession, u RealtimeUsage) error {
	sess.seq++
	// 累计（日志/记录）
	sess.total.TextInput += u.TextInput
	sess.total.TextOutput += u.TextOutput
	sess.total.AudioInput += u.AudioInput
	sess.total.AudioOutput += u.AudioOutput
	sess.total.CachedInput += u.CachedInput
	// Round(8) 对齐 DECIMAL(20,8) 存储口径（对抗 review CONFIRMED LOW）：cost 保留全精度时
	// MySQL 上 hold.amount 存值会被 ROUND(,8) 截断，破坏"hold==settle"等式→overage 分支 RowsAffected=0
	// →误报 insufficient 拆连；<0.5e-8 的微额 round 到 0 held 同样误报。Round 后内存与存储一致。
	cost := sess.price.RealtimeCost(u).Mul(sess.ratio).Round(8)
	if !cost.IsPositive() {
		return nil // 该轮无计费 token（如纯 session.update 触发的空 done）
	}
	idem := "rt:" + sess.reqID + ":" + itoa(sess.seq)
	err := r.DB.Transaction(func(tx *gorm.DB) error {
		h, already, herr := ledger.CreateHoldSharded(tx, sess.orgID, ledger.AccountUserCredit, sess.reqID, ledger.HoldInput{
			IdempotencyKey: idem, Amount: cost, RefType: "realtime", RefID: sess.reqID,
		})
		if herr != nil {
			return herr
		}
		if already {
			return nil // 幂等命中：本轮已扣（含 settle），不重复
		}
		_, serr := ledger.SettleHoldExact(tx, h.ID, cost, "realtime", sess.reqID,
			"realtime turn "+itoa(sess.seq))
		return serr
	})
	if err == nil {
		sess.cost = sess.cost.Add(cost)
	}
	return err
}

// parseRealtimeDone：从上游帧解析 response.done 的权威 usage。非 done / 无 usage → (,false)。
func parseRealtimeDone(msg []byte) (RealtimeUsage, bool) {
	var ev struct {
		Type     string `json:"type"`
		Response struct {
			Usage *struct {
				InputTokenDetails struct {
					TextTokens   int64 `json:"text_tokens"`
					AudioTokens  int64 `json:"audio_tokens"`
					CachedTokens int64 `json:"cached_tokens"`
				} `json:"input_token_details"`
				OutputTokenDetails struct {
					TextTokens  int64 `json:"text_tokens"`
					AudioTokens int64 `json:"audio_tokens"`
				} `json:"output_token_details"`
			} `json:"usage"`
		} `json:"response"`
	}
	if json.Unmarshal(msg, &ev) != nil || ev.Type != "response.done" || ev.Response.Usage == nil {
		return RealtimeUsage{}, false
	}
	d := ev.Response.Usage
	return RealtimeUsage{
		TextInput:   d.InputTokenDetails.TextTokens,
		AudioInput:  d.InputTokenDetails.AudioTokens,
		CachedInput: d.InputTokenDetails.CachedTokens,
		TextOutput:  d.OutputTokenDetails.TextTokens,
		AudioOutput: d.OutputTokenDetails.AudioTokens,
	}, true
}

// writeRealtimeError：按 Realtime 协议回写 error 事件（补 New API 缺口——不静默关闭）。带写截止。
func writeRealtimeError(conn *websocket.Conn, msg string) {
	b, _ := json.Marshal(gin.H{"type": "error", "error": gin.H{"type": "laiai_error", "message": msg}})
	_ = writeRealtimeFrame(conn, websocket.TextMessage, b)
}

// classifyWSClose：断连原因分类（明确日志——验收项）。正常关闭与异常/超时区分。
func classifyWSClose(err error, side string) string {
	if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
		return side + "_closed"
	}
	if e, ok := err.(net.Error); ok && e.Timeout() {
		return side + "_idle_timeout" // 心跳超时（假死）——补 New API 缺口
	}
	return side + "_read_error"
}

func itoa(n int) string { return strconv.Itoa(n) }

// wsProxyFor：ws 拨号代理——渠道配了 proxy 用它，否则回退环境代理（与 HTTP 路径口径一致）。
func wsProxyFor(ch *Channel) func(*http.Request) (*url.URL, error) {
	if ch.Proxy != "" {
		if pu, err := url.Parse(ch.Proxy); err == nil && pu.Host != "" {
			return http.ProxyURL(pu)
		}
	}
	return http.ProxyFromEnvironment
}
