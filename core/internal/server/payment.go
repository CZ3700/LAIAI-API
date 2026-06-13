package server

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"

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
	topupPending  int8 = 0
	topupPaid     int8 = 1
	topupFailed   int8 = 2
	topupExpired  int8 = 4
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
		"methods": methods,
		"amount_options": []int{10, 20, 50, 100, 200, 500}, // USD 档位
		"cny_rate":       s.getSetting("cny_per_usd"),       // EPay 用：1 USD = ? CNY（空=默认 7.3）
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

	// 实付金额/币种：stripe 直接 USD；epay 按汇率折 CNY（按 protocol 判，非 code）
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
	c.JSON(http.StatusOK, gin.H{"order_no": orderNo, "pay_url": result.PayURL})
}

// paymentCallback：POST /api/payment/callback/:provider —— webhook 入金。
// 红线集中（移植旧平台血泪）：验签 → 行锁取订单 → provider 匹配 → 幂等(已 paid) →
//   终态保护(failed 不翻转) → 金额校验 → ledger 入金(idempotency=topup:order)。
//   接受 status IN (pending, expired)：客户付了款但 callback 晚到，不能拒收（黑洞修复）。
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

	err = s.db.Transaction(func(tx *gorm.DB) error {
		var order TopupOrder
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("order_no = ?", res.OrderNo).First(&order).Error; e != nil {
			return fmt.Errorf("order not found: %s", res.OrderNo)
		}
		if order.Provider != provider { // 防跨网关回调攻击
			s.Log().Error("SECURITY: callback provider mismatch", "order", res.OrderNo,
				"order_provider", order.Provider, "callback_provider", provider)
			return fmt.Errorf("provider mismatch")
		}
		if order.Status == topupPaid { // 幂等：已入金，重复 callback 直接成功
			return nil
		}
		if order.Status == topupFailed { // 终态保护：失败订单不被晚到 callback 翻转
			return fmt.Errorf("order in terminal failed state")
		}
		// 金额校验：实付不得少于订单应付（多付容忍）
		if res.Amount.LessThan(order.PayAmount) {
			s.Log().Error("SECURITY: callback amount too low", "order", res.OrderNo,
				"expected", order.PayAmount, "paid", res.Amount)
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
		return tx.Model(&order).Updates(map[string]any{
			"status": topupPaid, "trade_no": res.TradeNo, "paid_at": s.now().Unix(),
		}).Error
	})
	if err != nil {
		s.Log().Error("payment callback credit failed", "provider", provider, "order", res.OrderNo, "err", err)
		c.String(http.StatusInternalServerError, "credit failed")
		return
	}
	s.payAffiliateRebate(res.OrderNo) // 邀请返佣（0016，尽力而为，失败不影响入金）
	s.writeAudit(c, "topup_paid", res.OrderNo, "topup credited via "+provider)
	c.String(http.StatusOK, engine.AckBody())
}

// payAffiliateRebate：充值入账后给邀请组织返佣（affiliate_rate × 充值额，settings 热更新）。
// 幂等键 affiliate:<order_no> 防重复；rate=0 或无邀请人则跳过。
func (s *Server) payAffiliateRebate(orderNo string) {
	rateStr := s.getSetting("affiliate_rate")
	if rateStr == "" {
		return
	}
	rate, err := decimal.NewFromString(rateStr)
	if err != nil || !rate.IsPositive() {
		return
	}
	var order TopupOrder
	if s.db.Where("order_no = ?", orderNo).First(&order).Error != nil {
		return
	}
	var org identity.Organization
	if s.db.First(&org, order.OrgID).Error != nil || org.InviterOrgID == 0 {
		return
	}
	rebate := order.AmountUSD.Mul(rate)
	if !rebate.IsPositive() {
		return
	}
	_ = s.db.Transaction(func(tx *gorm.DB) error {
		acc, e := ledger.EnsureAccount(tx, org.InviterOrgID, ledger.AccountUserCredit)
		if e != nil {
			return e
		}
		_, _, e = ledger.Post(tx, ledger.PostInput{
			IdempotencyKey: "affiliate:" + orderNo,
			AccountID:      acc.ID,
			Kind:           ledger.KindCredit,
			Delta:          rebate,
			RefType:        "affiliate",
			Reason:         "affiliate rebate from topup " + orderNo,
		})
		return e
	})
}

// getAffiliate：GET /api/affiliate —— 当前 org 的邀请码 + 统计（已邀请数/累计返佣）。
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
	var earned decimal.Decimal
	s.db.Raw(`SELECT COALESCE(SUM(j.delta), 0) FROM ledger_journal j
		JOIN ledger_accounts a ON a.id = j.account_id
		WHERE a.org_id = ? AND j.ref_type = 'affiliate'`, claims.OrgID).Scan(&earned)
	c.JSON(http.StatusOK, gin.H{
		"aff_code": code, "invited": invited, "earned": earned,
		"rate": s.getSetting("affiliate_rate"),
	})
}

// listTopups：GET /api/payment/orders —— 当前 org 充值记录。
func (s *Server) listTopups(c *gin.Context) {
	claims := mustClaims(c)
	var orders []TopupOrder
	s.db.Where("org_id = ?", claims.OrgID).Order("id DESC").Limit(50).Find(&orders)
	c.JSON(http.StatusOK, gin.H{"orders": orders})
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
		"affiliate_rate", // 邀请返佣比例（0016），如 "0.05" = 5%；空/0=关闭
	} {
		settingDefaults[k] = ""
	}
}

// adminPaymentConfig：GET /api/admin/payment-config —— 支付渠道状态（脱敏，运营总览）。
func (s *Server) adminPaymentConfig(c *gin.Context) {
	mask := func(v string) string {
		if v == "" {
			return ""
		}
		if len(v) <= 8 {
			return "****"
		}
		return v[:4] + "****" + v[len(v)-4:]
	}
	c.JSON(http.StatusOK, gin.H{
		"stripe": gin.H{
			"configured": s.getSetting("stripe_secret_key") != "",
			"secret":     mask(s.getSetting("stripe_secret_key")),
			"webhook":    s.getSetting("stripe_webhook_secret") != "",
		},
		"epay": gin.H{
			"configured": s.getSetting("epay_pid") != "",
			"pid":        s.getSetting("epay_pid"),
			"key":        mask(s.getSetting("epay_key")),
			"api_url":    s.getSetting("epay_api_url"),
		},
	})
}
