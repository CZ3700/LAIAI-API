package server

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
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

// engineFor：按 provider 名从运营设置构造支付引擎（密钥存 settings，admin 配）。
// 返回 nil 表示该 provider 未配置/未启用。
func (s *Server) engineFor(provider string) payment.Engine {
	if engineForTest != nil {
		return engineForTest(provider)
	}
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
func (s *Server) getPaymentMethods(c *gin.Context) {
	methods := []gin.H{}
	if s.engineFor("stripe") != nil {
		methods = append(methods, gin.H{"provider": "stripe", "currency": "USD", "label": "Card (Stripe)"})
	}
	if s.engineFor("epay") != nil {
		methods = append(methods, gin.H{"provider": "epay", "currency": "CNY", "label": "支付宝/微信 (EPay)"})
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
	engine := s.engineFor(req.Provider)
	if engine == nil {
		fail(c, http.StatusBadRequest, "payment provider not available: "+req.Provider)
		return
	}

	// 实付金额/币种：stripe 直接 USD；epay 按汇率折 CNY
	payAmount, payCurrency := amountUSD, "USD"
	if req.Provider == "epay" {
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
	s.writeAudit(c, "topup_paid", res.OrderNo, "topup credited via "+provider)
	c.String(http.StatusOK, engine.AckBody())
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
	} {
		settingDefaults[k] = ""
	}
}
