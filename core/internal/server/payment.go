package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/payment"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 订单状态（与旧平台同枚举，运维直觉）
const (
	topupPending int8 = 0
	topupPaid    int8 = 1
	topupFailed  int8 = 2
	topupExpired int8 = 4
)

// TopupOrder：充值订单（0015）。入金事实源仍是 ledger journal，此表是支付侧状态机。
type TopupOrder struct {
	ID          uint64          `gorm:"primaryKey" json:"id"`
	OrderNo     string          `gorm:"column:order_no" json:"order_no"`
	OrgID       uint64          `gorm:"column:org_id" json:"org_id"`
	UserID      *uint64         `gorm:"column:user_id" json:"-"`
	Provider    string          `gorm:"column:provider" json:"provider"`
	AmountUSD   decimal.Decimal `gorm:"column:amount_usd" json:"amount_usd"`
	PayAmount   decimal.Decimal `gorm:"column:pay_amount" json:"pay_amount"`
	PayCurrency string          `gorm:"column:pay_currency" json:"pay_currency"`
	Status      int8            `gorm:"column:status" json:"status"`
	TradeNo     string          `gorm:"column:trade_no" json:"trade_no"`
	ProviderRef string          `gorm:"column:provider_ref" json:"-"` // 网关会话引用（Stripe session id），对账用
	CreatedAt   int64           `gorm:"column:created_at" json:"created_at"`
	PaidAt      int64           `gorm:"column:paid_at" json:"paid_at"`
}

func (TopupOrder) TableName() string { return "topup_orders" }

// engineForTest：测试钩子，非 nil 时 engineFor 优先返回它（注入 mock 引擎）。
var engineForTest func(provider string) payment.Engine

// engineFor：按渠道 code/protocol 构造支付引擎。解析顺序（向后兼容）：
//  1. 测试钩子（注入 mock）
//  2. payment_channels 表里启用的同 code 渠道（信封加密配置，#5 灵活渠道）
//  3. 退回扁平 ext_settings 单套 stripe/epay 配置（旧路径，未建表时仍可用）
//
// 返回 nil 表示未配置/未启用。
func (s *Server) engineFor(provider string) payment.Engine {
	if engineForTest != nil {
		return engineForTest(provider)
	}
	// 表渠道优先。注意：不过滤 enabled —— 渠道被停用后，已发起的支付仍需能在
	// callback 里解析引擎完成入金（否则真金付款无法到账，资金黑洞）。新建充值的
	// enabled 校验在 createTopup 里单独做。
	if s.keyring != nil {
		var ch PaymentChannel
		if err := s.db.Where("code = ?", provider).First(&ch).Error; err == nil {
			if cfg, e := s.decryptChannelConfig(ch.ConfigEnc); e == nil {
				if eng := buildPaymentEngine(ch.Protocol, cfg); eng != nil {
					return eng
				}
			}
		}
	}
	// 设置回退（旧路径）
	switch provider {
	case "stripe":
		sk := s.getSetting("stripe_secret_key")
		if sk == "" {
			return nil
		}
		return &payment.Stripe{SecretKey: sk, WebhookSecret: s.getSetting("stripe_webhook_secret")}
	case "epay":
		pid := s.getSetting("epay_pid")
		if pid == "" {
			return nil
		}
		return &payment.EPay{PID: pid, Key: s.getSetting("epay_key"), APIURL: s.getSetting("epay_api_url")}
	case "coinbase":
		// 加密货币主通道（WS-4）：默认禁用——字段层级/枚举尚未接真实账号 fixture 验真
		// （见 coinbase_commerce.go 红线）。验真后置 coinbase_enabled="true" 方可进真金路径。
		if s.getSetting("coinbase_enabled") != "true" {
			return nil
		}
		ak := s.getSetting("coinbase_api_key")
		if ak == "" || s.getSetting("coinbase_webhook_secret") == "" {
			return nil
		}
		return &payment.CoinbaseCommerce{APIKey: ak, WebhookSecret: s.getSetting("coinbase_webhook_secret")}
	}
	return nil
}

// getPaymentMethods：GET /api/payment/methods —— 可用支付方式 + 充值档位（前端钱包页）。
// 有启用的表渠道则列表渠道；否则退回设置里的 stripe/epay（向后兼容）。
func (s *Server) getPaymentMethods(c *gin.Context) {
	methods := []gin.H{}
	chs := s.enabledPaymentChannels()
	if len(chs) > 0 {
		for _, ch := range chs {
			if s.engineFor(ch.Code) != nil {
				methods = append(methods, gin.H{
					"provider": ch.Code, "currency": ch.Currency,
					"label": ch.DisplayName, "pay_methods": ch.PayMethods,
				})
			}
		}
	} else {
		if s.engineFor("stripe") != nil {
			methods = append(methods, gin.H{"provider": "stripe", "currency": "USD", "label": "Card (Stripe)"})
		}
		if s.engineFor("epay") != nil {
			methods = append(methods, gin.H{"provider": "epay", "currency": "CNY", "label": "支付宝/微信 (EPay)"})
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"methods":        methods,
		"amount_options": []int{10, 20, 50, 100, 200, 500},     // USD 档位
		"cny_rate":       s.getSetting("cny_per_usd"),          // EPay 用：1 USD = ? CNY（空=默认 7.3）
		"bonus_tiers":    s.getSetting("recharge_bonus_tiers"), // 充值阶梯优惠（前端按金额预览赠额；空=无）
	})
}

// createTopup：POST /api/payment/create {provider, amount_usd} —— 建订单 + 返回支付 URL。
func (s *Server) createTopup(c *gin.Context) {
	claims := mustClaims(c)
	var req struct {
		Provider  string `json:"provider" binding:"required"`
		AmountUSD string `json:"amount_usd" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "provider and amount_usd required")
		return
	}
	amountUSD, err := decimal.NewFromString(req.AmountUSD)
	if err != nil || !amountUSD.IsPositive() {
		fail(c, http.StatusBadRequest, "amount_usd must be a positive decimal string")
		return
	}
	// 最低充值额（运营可配，热更新）：低于阈值直接拒绝，省得建无效订单
	if min := s.getSetting("min_topup_usd"); min != "" {
		if m, e := decimal.NewFromString(min); e == nil && amountUSD.LessThan(m) {
			fail(c, http.StatusBadRequest, "amount below minimum top-up of "+min+" USD")
			return
		}
	}
	// 渠道协议 + 启用态：停用渠道不接新充值（但 callback 仍能完成在途支付，见 engineFor）
	protocol, isTable, enabled := s.paymentChannelInfo(req.Provider)
	if isTable && !enabled {
		fail(c, http.StatusBadRequest, "payment channel is disabled: "+req.Provider)
		return
	}
	engine := s.engineFor(req.Provider)
	if engine == nil {
		fail(c, http.StatusBadRequest, "payment provider not available: "+req.Provider)
		return
	}

	// 实付金额/币种：stripe 直接 USD；epay 按汇率折 CNY
	payAmount, payCurrency := amountUSD, "USD"
	if protocol == "epay" {
		rate := decimal.RequireFromString("7.3")
		if r := s.getSetting("cny_per_usd"); r != "" {
			if rr, e := decimal.NewFromString(r); e == nil && rr.IsPositive() {
				rate = rr
			}
		}
		payAmount, payCurrency = amountUSD.Mul(rate).Round(2), "CNY"
	}

	orderNo := newOrderNo(claims.OrgID)
	uid := claims.UserID
	order := &TopupOrder{
		OrderNo: orderNo, OrgID: claims.OrgID, UserID: &uid,
		Provider: req.Provider, AmountUSD: amountUSD,
		PayAmount: payAmount, PayCurrency: payCurrency,
		Status: topupPending, CreatedAt: s.now().Unix(),
	}
	if err := s.db.Create(order).Error; err != nil {
		fail(c, http.StatusInternalServerError, "could not create order")
		return
	}

	base := "https://" + c.Request.Host
	result, err := engine.CreatePayment(c.Request.Context(), &payment.CreateRequest{
		OrderNo:     orderNo,
		Amount:      payAmount,
		Currency:    payCurrency,
		Description: s.getSetting("site_name") + " top-up $" + amountUSD.String(),
		NotifyURL:   base + "/api/payment/callback/" + req.Provider,
		ReturnURL:   base + "/app",
		ClientIP:    c.ClientIP(),
	})
	if err != nil {
		s.db.Model(order).Update("status", topupFailed)
		fail(c, http.StatusBadGateway, "payment gateway error: "+err.Error())
		return
	}
	// 持久化网关会话引用（Stripe session id），供 reconcile job 主动对账。
	// P2-7：写失败必须响亮告警——否则该单 provider_ref 为空，reconcile 无从 QueryOrder，
	// 一旦 callback 也漏发即成对账盲区（运维可据日志手工回填 provider_ref）。
	if result.ProviderRef != "" {
		if e := s.db.Model(order).Update("provider_ref", result.ProviderRef).Error; e != nil {
			s.Log().Error("provider_ref persist failed — reconcile blind spot if callback misses",
				"order", orderNo, "provider", req.Provider, "provider_ref", result.ProviderRef, "err", e)
		}
	}
	out := gin.H{"order_no": orderNo, "pay_url": result.PayURL}
	if bonus := s.computeRechargeBonus(amountUSD); bonus.IsPositive() { // 预览：到账将额外赠送（实际入账时再算一次，以入账为准）
		out["bonus_usd"] = bonus.String()
	}
	if result.Requirements != "" { // x402：返回支付要求供前端钱包签名（无跳转 URL）
		out["requirements"] = result.Requirements
	}
	c.JSON(http.StatusOK, out)
}

// paymentCallback：POST /api/payment/callback/:provider —— webhook 入金。
// 红线集中（移植旧平台血泪）：验签 → 行锁取订单 → provider 匹配 → 幂等(已 paid) →
//
//	终态保护(failed 不翻转) → 金额校验 → ledger 入金(idempotency=topup:order)。
//	接受 status IN (pending, expired)：客户付了款但 callback 晚到，不能拒收（黑洞修复）。
func (s *Server) paymentCallback(c *gin.Context) {
	provider := c.Param("provider")
	engine := s.engineFor(provider)
	if engine == nil {
		fail(c, http.StatusBadRequest, "unknown provider")
		return
	}
	rawBody, _ := readAllLimited(c, 1<<20)
	headers := map[string]string{}
	for k := range c.Request.Header {
		headers[k] = c.Request.Header.Get(k)
	}
	query := map[string]string{}
	for k, v := range c.Request.URL.Query() {
		if len(v) > 0 {
			query[k] = v[0]
		}
	}
	res, err := engine.VerifyCallback(rawBody, headers, query)
	if err != nil || res == nil {
		s.Log().Warn("payment callback verify failed", "provider", provider, "err", err)
		c.String(http.StatusBadRequest, "verify failed")
		return
	}
	if !res.Success {
		c.String(http.StatusOK, engine.AckBody()) // 非成功通知，ack 但不入金
		return
	}

	already, err := s.creditPaidOrder(provider, res.OrderNo, res.TradeNo, res.Amount, res.Currency)
	if err != nil {
		s.Log().Error("payment callback credit failed", "provider", provider, "order", res.OrderNo, "err", err)
		c.String(http.StatusInternalServerError, "credit failed")
		return
	}
	if !already { // 首次入金才审计；重复 callback 静默 ack
		s.writeAudit(c, "topup_paid", res.OrderNo, "topup credited via "+provider)
	}
	c.String(http.StatusOK, engine.AckBody())
}


// creditPaidOrder：把一笔已确认支付的订单入账——**callback 与 reconcile 共用的唯一资金路径**。
// 红线集中：行锁取订单 → provider 匹配（防跨网关）→ 幂等(已 paid→already=true) →
//
//	终态保护(failed 不翻转；接受 pending|expired) → 金额校验 → ledger 入金(幂等键 topup:order)。
//
// paidAmount = 网关实付（callback 的 res.Amount 或 query 的 QueryResult.Amount），口径一致。
// paidCurrency = 网关实付币种（res.Currency/qr.Currency）；非空时必须与订单 PayCurrency 一致
// （P2-8：防"金额数字相同但币种更便宜"的隐性少付，如 73 CNY 单收到 73 USD 通知）。空=网关未报，跳过。
func (s *Server) creditPaidOrder(provider, orderNo, tradeNo string, paidAmount decimal.Decimal, paidCurrency string) (already bool, err error) {
	bonusTiersRaw := s.getSetting("recharge_bonus_tiers") // 事务外取配置：事务内读 ext_settings 会与持锁事务自死锁（单连接池）
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var order TopupOrder
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("order_no = ?", orderNo).First(&order).Error; e != nil {
			return fmt.Errorf("order not found: %s", orderNo)
		}
		if order.Provider != provider { // 防跨网关入账攻击
			s.Log().Error("SECURITY: credit provider mismatch", "order", orderNo,
				"order_provider", order.Provider, "credit_provider", provider)
			return fmt.Errorf("provider mismatch")
		}
		if order.Status == topupPaid { // 幂等：已入金
			already = true
			return nil
		}
		if order.Status == topupFailed { // 终态保护：失败订单不被翻转（pending|expired 仍可入金）
			return fmt.Errorf("order in terminal failed state")
		}
		// 币种校验（P2-8）：网关报了币种就必须与订单一致，否则金额比较失去意义（跨币种少付）
		if paidCurrency != "" && !strings.EqualFold(paidCurrency, order.PayCurrency) {
			s.Log().Error("SECURITY: credit currency mismatch", "order", orderNo,
				"expected", order.PayCurrency, "paid", paidCurrency)
			return fmt.Errorf("currency mismatch")
		}
		// 金额校验：实付不得少于订单应付（多付容忍）
		if paidAmount.LessThan(order.PayAmount) {
			s.Log().Error("SECURITY: credit amount too low", "order", orderNo,
				"expected", order.PayAmount, "paid", paidAmount)
			return fmt.Errorf("amount too low")
		}
		// 入金：按订单的 amount_usd 入账（实付币种只是支付通道，入账永远 USD）
		acc, e := ledger.EnsureAccount(tx, order.OrgID, ledger.AccountUserCredit)
		if e != nil {
			return e
		}
		if _, _, e := ledger.Post(tx, ledger.PostInput{
			IdempotencyKey: "topup:" + order.OrderNo, // 幂等：同订单永不重复入账
			AccountID:      acc.ID,
			Kind:           ledger.KindCredit,
			Delta:          order.AmountUSD,
			RefType:        "topup",
			Reason:         "topup via " + provider,
		}); e != nil {
			return e
		}
		// 充值阶梯优惠（gap P1-D）：同事务内单列赠额入账，独立幂等键，reconcile 重跑安全
		if bonus := rechargeBonusFromTiers(bonusTiersRaw, order.AmountUSD); bonus.IsPositive() {
			if _, _, e := ledger.Post(tx, ledger.PostInput{
				IdempotencyKey: "topup_bonus:" + order.OrderNo,
				AccountID:      acc.ID,
				Kind:           ledger.KindGrant,
				Delta:          bonus,
				RefType:        "topup_bonus",
				Reason:         "recharge bonus for topup " + order.OrderNo,
			}); e != nil {
				return e
			}
		}
		return tx.Model(&order).Updates(map[string]any{
			"status": topupPaid, "trade_no": tradeNo, "paid_at": s.now().Unix(),
		}).Error
	})
	return already, err
}

// ReconcileTopups：主动对账兜底 callback/webhook 漏发（红线 #5；金融级双保险）。
// 扫描 pending 且建单超过 minAge 的订单，对每单 engine.QueryOrder；网关侧已支付则走
// 统一入账路径 creditPaidOrder（幂等，与 callback 同一资金路径）。返回 (checked, credited)。
//   - minAge 防误判尚在支付中的新单（callback 通常秒级到，对账是分钟级兜底）
//   - 只扫 pending：paid 已入账、failed 终态、expired 单独清理，均不重复查
//   - 单个引擎/订单失败不阻塞其余（warn 后 continue），整轮尽力而为
func (s *Server) ReconcileTopups(ctx context.Context, minAge time.Duration, limit int) (checked, credited int, err error) {
	cutoff := s.now().Add(-minAge).Unix()
	var orders []TopupOrder
	if e := s.db.WithContext(ctx).Where("status = ? AND created_at < ?", topupPending, cutoff).
		Order("id").Limit(limit).Find(&orders).Error; e != nil {
		return 0, 0, e
	}
	for i := range orders {
		o := &orders[i]
		// P0-4：x402 无异步查询端点（QueryOrder 恒 Found=false），但 settle 成功会先把 txHash
		// 落到 trade_no。已结算(trade_no 非空)却仍 pending（入账当时失败）→ 据 trade_no 重放
		// 统一入账修复（不依赖客户端回访，也不需引擎；即便 x402 已被禁用仍能补回历史在途单）。
		if o.Provider == "x402" {
			if o.TradeNo == "" {
				continue // 未结算，无可恢复
			}
			checked++
			already, ce := s.creditPaidOrder(o.Provider, o.OrderNo, o.TradeNo, o.PayAmount, o.PayCurrency)
			if ce != nil {
				s.Log().Error("reconcile x402 credit failed", "order", o.OrderNo, "err", ce)
				continue
			}
			if !already {
				credited++
				s.Log().Warn("reconcile credited settled-but-uncredited x402 order", "order", o.OrderNo)
			}
			continue
		}
		engine := s.engineFor(o.Provider)
		if engine == nil {
			continue // 渠道配置已不可解析，跳过（不阻塞其他单）
		}
		qr, qe := engine.QueryOrder(ctx, payment.QueryRequest{OrderNo: o.OrderNo, ProviderRef: o.ProviderRef})
		checked++
		if qe != nil {
			s.Log().Warn("reconcile query failed", "provider", o.Provider, "order", o.OrderNo, "err", qe)
			continue
		}
		if qr == nil || !qr.Found || !qr.Paid {
			continue // 查不到 / 未支付：留待下轮
		}
		already, ce := s.creditPaidOrder(o.Provider, o.OrderNo, qr.TradeNo, qr.Amount, qr.Currency)
		if ce != nil {
			s.Log().Error("reconcile credit failed", "provider", o.Provider, "order", o.OrderNo, "err", ce)
			continue
		}
		if !already { // 真正补回了一笔漏发 callback 的入金
			credited++
			s.Log().Warn("reconcile credited orphan paid order", "provider", o.Provider, "order", o.OrderNo)
		}
	}
	return checked, credited, nil
}

// ExpireStaleTopups：把长期未支付的 pending 单标记为 expired（指令 P1-3；此前 topupExpired
// 全库无写入点，"expired 单独清理"的承诺是空转）。expired 只是运营/展示口径 + 收敛
// ReconcileTopups 的 pending 扫描集——**绝不是入金终态**：用户极晚支付时 callback 仍走
// creditPaidOrder（接受 pending|expired，uid=967 语义）正常入账；只有 failed 是终态。
// x402 已结算待重放的单（trade_no 非空）不标过期，留给对账重放兜底。
//
// 标过期前做**最终网关确认**（对抗 review #5：expired 单永久退出 reconcile 扫描集，若不
// 确认就标，"晚支付 + callback 丢失"的兜底双保险被 24h 悬崖静默切断）：
//   - 网关侧已付 → 走统一入账，绝不标过期；
//   - 网关查询失败 → 本轮跳过（留 pending 下轮再试），绝不盲标；
//   - 网关确认未付/查无此单/引擎不可解析（渠道已删，callback 腿依旧有效）→ 标过期。
// 逐行 UPDATE 复核完整谓词（含 x402 锚点排除，对抗 review #4 TOCTOU）：选取间隙被支付/
// 被落锚点的单不误标。
func (s *Server) ExpireStaleTopups(ctx context.Context, maxAge time.Duration, limit int) (int, error) {
	cutoff := s.now().Add(-maxAge).Unix()
	var orders []TopupOrder
	if e := s.db.WithContext(ctx).
		Where("status = ? AND created_at < ? AND NOT (provider = ? AND trade_no <> '')",
			topupPending, cutoff, "x402").
		Order("id").Limit(limit).Find(&orders).Error; e != nil {
		return 0, e
	}
	expired := 0
	for i := range orders {
		o := &orders[i]
		if ctx.Err() != nil {
			break
		}
		if engine := s.engineFor(o.Provider); engine != nil {
			qr, qe := engine.QueryOrder(ctx, payment.QueryRequest{OrderNo: o.OrderNo, ProviderRef: o.ProviderRef})
			if qe != nil {
				s.Log().Warn("expire sweep: gateway query failed, keeping pending for next round",
					"provider", o.Provider, "order", o.OrderNo, "err", qe)
				continue
			}
			if qr != nil && qr.Found && qr.Paid {
				if _, ce := s.creditPaidOrder(o.Provider, o.OrderNo, qr.TradeNo, qr.Amount, qr.Currency); ce != nil {
					s.Log().Error("expire sweep: credit of gateway-paid order failed", "order", o.OrderNo, "err", ce)
				} else {
					s.Log().Warn("expire sweep credited late-paid order (callback was lost)", "order", o.OrderNo)
				}
				continue // 已付：绝不标过期
			}
		}
		res := s.db.WithContext(ctx).Model(&TopupOrder{}).
			Where("id = ? AND status = ? AND NOT (provider = ? AND trade_no <> '')",
				o.ID, topupPending, "x402").
			Update("status", topupExpired)
		if res.Error != nil {
			return expired, res.Error
		}
		expired += int(res.RowsAffected)
	}
	return expired, nil
}

// computeRechargeBonus：按充值阶梯优惠（recharge_bonus_tiers，gap P1-D）算赠额（读 settings + 计算）。
// 注意：**不可在已开启的 DB 事务内调用**——会嵌套查 ext_settings，单连接池下与持锁事务自死锁
// （creditPaidOrder 已先取 raw 再进事务，改调 rechargeBonusFromTiers 纯函数）。
func (s *Server) computeRechargeBonus(amountUSD decimal.Decimal) decimal.Decimal {
	return rechargeBonusFromTiers(s.getSetting("recharge_bonus_tiers"), amountUSD)
}

// rechargeBonusFromTiers：纯函数——给定阶梯配置 JSON 与充值额，算赠额。
// 取「amountUSD ≥ min_usd 的最高档 min_usd」对应 rate，bonus = amountUSD × rate，四舍五入到 2 位
// （USD 分位，与前端 Wallet.vue 预览 toFixed(2) 完全一致，保证「预览=实到」）。
// 配置非法/无匹配档/rate≤0 → 返 0（静默不赠，不阻断入账）。
func rechargeBonusFromTiers(raw string, amountUSD decimal.Decimal) decimal.Decimal {
	if raw == "" {
		return decimal.Zero
	}
	var tiers []struct {
		MinUSD string `json:"min_usd"`
		Rate   string `json:"rate"`
	}
	if json.Unmarshal([]byte(raw), &tiers) != nil {
		return decimal.Zero
	}
	bestRate := decimal.Zero
	bestMin := decimal.NewFromInt(-1) // 追踪已匹配档中最高的 min_usd
	for _, t := range tiers {
		min, e1 := decimal.NewFromString(t.MinUSD)
		rate, e2 := decimal.NewFromString(t.Rate)
		if e1 != nil || e2 != nil || rate.IsNegative() || min.IsNegative() {
			continue
		}
		if amountUSD.GreaterThanOrEqual(min) && min.GreaterThan(bestMin) {
			bestMin, bestRate = min, rate
		}
	}
	if !bestRate.IsPositive() {
		return decimal.Zero
	}
	return amountUSD.Mul(bestRate).Round(2)
}

// 注：返佣/affiliate（按充值额给邀请人返佣）是旧平台迁移残留，本站不提供——已彻底移除
// payAffiliateRebate 函数及其全部调用点（见 0016_affiliate.sql）。邀请码仅驱动「被邀请人注册赠额」。

// getAffiliate：GET /api/affiliate —— 当前 org 的邀请码 + 已邀请数。
// 注：返佣（earned/rate）已移除——本站不提供按充值额给邀请人返佣。邀请码仅用于
// 「被邀请人注册赠额」(invite_grant_usd) 与统计，不产生返佣入账。
func (s *Server) getAffiliate(c *gin.Context) {
	claims := mustClaims(c)
	var org identity.Organization
	if s.db.First(&org, claims.OrgID).Error != nil {
		fail(c, http.StatusInternalServerError, "could not load org")
		return
	}
	code := ""
	if org.AffCode != nil {
		code = *org.AffCode
	}
	var invited int64
	s.db.Model(&identity.Organization{}).Where("inviter_org_id = ?", claims.OrgID).Count(&invited)
	c.JSON(http.StatusOK, gin.H{"aff_code": code, "invited": invited})
}

// listTopups：GET /api/payment/orders —— 当前 org 充值记录（最近 50 条明细 + 权威总数）。
// 返回 total 让前端"共 N 条/交易数"等计数显示真实总量，而不是被 Limit(50) 截断后的明细条数。
// 注：本月已到账金额请用 /api/payment/recharge-stats（服务端聚合），不要对这 50 条明细本地求和（会漏算）。
func (s *Server) listTopups(c *gin.Context) {
	claims := mustClaims(c)
	var total int64
	s.db.Model(&TopupOrder{}).Where("org_id = ?", claims.OrgID).Count(&total)
	var orders []TopupOrder
	s.db.Where("org_id = ?", claims.OrgID).Order("id DESC").Limit(50).Find(&orders)
	c.JSON(http.StatusOK, gin.H{"orders": orders, "total": total})
}

// getRechargeStats：GET /api/payment/recharge-stats —— 仪表盘卡片用：本月 + 近 7 个 UTC 自然日
// 已到账(status=paid)充值额(USD)，**服务端聚合**。避免前端对分页(50 条)明细本地求和静默漏算（金额红线）。
// 近 7 日窗口与仪表盘 sumDays(-6,7) 取同样 7 个 UTC 自然日，保证 net7d 被减数/减数口径一致。
func (s *Server) getRechargeStats(c *gin.Context) {
	claims := mustClaims(c)
	now := s.now().UTC()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).Unix()
	last7Start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -6).Unix()
	var out struct {
		MonthUSD decimal.Decimal `json:"month_usd"`
		Last7USD decimal.Decimal `json:"last7_usd"`
	}
	// 用主库：充值额是金额展示，避免副本滞后让用户误判刚充的钱没到（与余额读同等谨慎）。
	s.db.WithContext(c.Request.Context()).Raw(`
		SELECT COALESCE(SUM(CASE WHEN created_at >= ? THEN amount_usd ELSE 0 END), 0) AS month_usd,
		       COALESCE(SUM(CASE WHEN created_at >= ? THEN amount_usd ELSE 0 END), 0) AS last7_usd
		FROM topup_orders WHERE org_id = ? AND status = ?`,
		monthStart, last7Start, claims.OrgID, topupPaid).Scan(&out)
	c.JSON(http.StatusOK, gin.H{"month_usd": out.MonthUSD, "last7_usd": out.Last7USD})
}

// newOrderNo：订单号 = USR{org}NO{rand}{ts}（移植旧平台格式）。
func newOrderNo(orgID uint64) string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return fmt.Sprintf("USR%dNO%s", orgID, hex.EncodeToString(b))
}

// readAllLimited：读请求体（限长）。
func readAllLimited(c *gin.Context, max int64) ([]byte, error) {
	return io.ReadAll(io.LimitReader(c.Request.Body, max))
}

// 支付配置键（admin settings；密钥类不下发公开 config）
func init() {
	for _, k := range []string{
		"stripe_secret_key", "stripe_webhook_secret",
		"epay_pid", "epay_key", "epay_api_url", "cny_per_usd",
		// 注：返佣/affiliate_rate 已移除（本站无此功能，旧平台迁移残留）——不再注册为可配置项。
		// 加密货币主通道（WS-4，默认禁用，验真后置 coinbase_enabled=true）
		"coinbase_enabled", "coinbase_api_key", "coinbase_webhook_secret",
	} {
		settingDefaults[k] = ""
	}
}

