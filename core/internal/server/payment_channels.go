package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/payment"
)

// PaymentChannel：灵活支付渠道（0018）。多实例、可启停、按优先级路由。
// config_enc 是信封加密的 JSON（密钥等敏感字段），明文永不回显。
type PaymentChannel struct {
	ID          uint64 `gorm:"primaryKey" json:"id"`
	Code        string `gorm:"column:code" json:"code"`
	DisplayName string `gorm:"column:display_name" json:"display_name"`
	Protocol    string `gorm:"column:protocol" json:"protocol"`
	ConfigEnc   string `gorm:"column:config_enc" json:"-"` // 永不出 JSON
	Enabled     int8   `gorm:"column:enabled" json:"enabled"`
	Priority    int    `gorm:"column:priority" json:"priority"`
	PayMethods  string `gorm:"column:pay_methods" json:"pay_methods"`
	Currency    string `gorm:"column:currency" json:"currency"`
	LastTestAt  int64  `gorm:"column:last_test_at" json:"last_test_at"`
	LastTestOK  int8   `gorm:"column:last_test_ok" json:"last_test_ok"`
	LastTestMsg string `gorm:"column:last_test_msg" json:"last_test_msg"`
}

func (PaymentChannel) TableName() string { return "payment_channels" }

// paymentChannelInfo：按 code 解析（协议, 是否表渠道, 是否启用）。
// 设置回退时 code 即 protocol、视为启用。用于 createTopup 判断币种换算与停用拦截，
// 不能再用「code == 'epay'」硬判（表渠道 code 可自定义如 epay_main）。
func (s *Server) paymentChannelInfo(code string) (protocol string, isTable, enabled bool) {
	if s.keyring != nil {
		var ch PaymentChannel
		if s.db.Where("code = ?", code).First(&ch).Error == nil {
			return ch.Protocol, true, ch.Enabled == 1
		}
	}
	return code, false, true
}

// buildPaymentEngine：按协议 + 解密后的配置 map 构造引擎。返回 nil = 必填项缺失。
func buildPaymentEngine(protocol string, cfg map[string]string) payment.Engine {
	switch protocol {
	case "stripe":
		if cfg["secret_key"] == "" {
			return nil
		}
		return &payment.Stripe{SecretKey: cfg["secret_key"], WebhookSecret: cfg["webhook_secret"]}
	case "epay":
		if cfg["pid"] == "" {
			return nil
		}
		return &payment.EPay{PID: cfg["pid"], Key: cfg["key"], APIURL: cfg["api_url"]}
	case "crypto":
		// 通用加密货币网关骨架（处理商无关，admin 后台热配置——EBPay/OxaPay/NOWPayments 等三段式处理商）。
		// 渠道 config 提供 processor/api_base/api_key/webhook_secret/sign_algo/create_path/query_path。
		// ⚠ 默认禁用——渠道 enabled 由 admin 验真后手动开；验签 fail-closed + 金额校验防误入金（见 crypto_gateway.go 红线）。
		if cfg["api_key"] == "" || cfg["webhook_secret"] == "" {
			return nil
		}
		return &payment.CryptoGateway{
			Processor: cfg["processor"], APIBase: cfg["api_base"], APIKey: cfg["api_key"],
			WebhookSecret: cfg["webhook_secret"], SignAlgo: cfg["sign_algo"],
			CreatePath: cfg["create_path"], QueryPath: cfg["query_path"],
		}
	case "coinbase":
		// 加密货币主通道（WS-4）：api_key + webhook_secret 两个独立密钥齐全才构建。
		// ⚠ 上线前须接真实账号抓 raw 写 fixture 验真（见 coinbase_commerce.go 红线）。
		if cfg["api_key"] == "" || cfg["webhook_secret"] == "" {
			return nil
		}
		return &payment.CoinbaseCommerce{APIKey: cfg["api_key"], WebhookSecret: cfg["webhook_secret"]}
	case "x402":
		// 默认禁用：facilitator/收款地址/资产三者齐全才构建（见 x402.go 合约校验注记）
		if cfg["facilitator_url"] == "" || cfg["pay_to"] == "" || cfg["asset"] == "" {
			return nil
		}
		return &payment.X402{
			FacilitatorURL: cfg["facilitator_url"], PayTo: cfg["pay_to"],
			Asset: cfg["asset"], Network: cfg["network"],
		}
	}
	return nil
}

// decryptChannelConfig：信封解密 + JSON 反序列化成配置 map。
func (s *Server) decryptChannelConfig(enc string) (map[string]string, error) {
	plain, err := s.keyring.Decrypt(enc)
	if err != nil {
		return nil, err
	}
	cfg := map[string]string{}
	if err := json.Unmarshal([]byte(plain), &cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// enabledPaymentChannels：启用的渠道按优先级升序（数小者先）。表不存在/查询失败时返回空。
func (s *Server) enabledPaymentChannels() []PaymentChannel {
	var chs []PaymentChannel
	if err := s.db.Where("enabled = 1").Order("priority, id").Find(&chs).Error; err != nil {
		return nil
	}
	return chs
}

// adminListPaymentChannels：GET /api/admin/payment-channels —— 渠道列表（脱敏，不含密文）。
func (s *Server) adminListPaymentChannels(c *gin.Context) {
	var chs []PaymentChannel
	s.db.Order("priority, id").Find(&chs)
	if chs == nil {
		chs = []PaymentChannel{}
	}
	c.JSON(http.StatusOK, gin.H{"channels": chs})
}

type paymentChannelReq struct {
	Code        string            `json:"code" binding:"required"`
	DisplayName string            `json:"display_name"`
	Protocol    string            `json:"protocol" binding:"required"`
	Config      map[string]string `json:"config"` // 明文密钥；省略=保留原配置（编辑时）
	Enabled     *bool             `json:"enabled"`
	Priority    int               `json:"priority"`
	PayMethods  string            `json:"pay_methods"`
	Currency    string            `json:"currency"`
}

// adminUpsertPaymentChannel：POST /api/admin/payment-channels —— 建/改渠道（按 code upsert）。
func (s *Server) adminUpsertPaymentChannel(c *gin.Context) {
	if s.keyring == nil {
		fail(c, http.StatusServiceUnavailable, "LAIAI_DATA_KEYS not configured")
		return
	}
	var req paymentChannelReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "code and protocol required")
		return
	}
	code := strings.TrimSpace(req.Code)
	// crypto = 通用加密货币网关骨架，开放表渠道热接入（admin 配置处理商 + enabled 即生效）：验签
	// fail-closed + 实付金额校验使其即便误启用也不致误入金。coinbase 仍仅经 ext_settings+coinbase_enabled
	// 显式启用（其字段层级待真实 fixture 验真）。
	if code == "" || (req.Protocol != "stripe" && req.Protocol != "epay" && req.Protocol != "crypto") {
		fail(c, http.StatusBadRequest, "invalid code or protocol (stripe|epay|crypto)")
		return
	}

	var existing PaymentChannel
	found := s.db.Where("code = ?", code).First(&existing).Error == nil

	// 护栏：禁止改写既有 code 的协议。订单只存 code（=provider），callback/reconcile 按当前行
	// 重解析引擎；若把 epay 改成 stripe，在途订单回调会被新引擎拒签→无法入账（在途支付黑洞红线）。
	// 换协议必须用新 code（每协议一 code，如 epay_main / stripe_main）。
	if found && req.Protocol != existing.Protocol {
		fail(c, http.StatusConflict, "cannot change protocol of an existing channel code; create a new code instead")
		return
	}

	// 配置加密：提供了 config 则加密新配置；编辑时省略 config = 保留原密文
	configEnc := ""
	if len(req.Config) > 0 {
		raw, _ := json.Marshal(req.Config)
		enc, err := s.keyring.Encrypt(string(raw))
		if err != nil {
			fail(c, http.StatusInternalServerError, "config encrypt failed")
			return
		}
		configEnc = enc
	} else if found {
		configEnc = existing.ConfigEnc
	} else {
		fail(c, http.StatusBadRequest, "config required for a new channel")
		return
	}

	enabled := int8(1)
	if req.Enabled != nil && !*req.Enabled {
		enabled = 0
	} else if found && req.Enabled == nil {
		enabled = existing.Enabled
	}
	currency := req.Currency
	if currency == "" {
		currency = "USD"
	}

	ch := PaymentChannel{
		ID: existing.ID, Code: code, DisplayName: req.DisplayName, Protocol: req.Protocol,
		ConfigEnc: configEnc, Enabled: enabled, Priority: req.Priority,
		PayMethods: req.PayMethods, Currency: currency,
		LastTestAt: existing.LastTestAt, LastTestOK: existing.LastTestOK, LastTestMsg: existing.LastTestMsg,
	}
	if err := s.db.Save(&ch).Error; err != nil {
		fail(c, http.StatusInternalServerError, "save failed")
		return
	}
	action := "payment_channel_create"
	if found {
		action = "payment_channel_update"
	}
	s.writeAudit(c, action, code, "protocol="+req.Protocol)
	c.JSON(http.StatusOK, gin.H{"id": ch.ID, "code": ch.Code})
}

// adminDeletePaymentChannel：DELETE /api/admin/payment-channels/:id
func (s *Server) adminDeletePaymentChannel(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid id")
		return
	}
	res := s.db.Delete(&PaymentChannel{}, id)
	if res.Error != nil || res.RowsAffected == 0 {
		fail(c, http.StatusNotFound, "channel not found")
		return
	}
	s.writeAudit(c, "payment_channel_delete", strconv.FormatUint(id, 10), "payment channel deleted")
	c.JSON(http.StatusOK, gin.H{"deleted": id})
}

// adminTestPaymentChannel：POST /api/admin/payment-channels/:id/test —— 连通性自检。
// 当前为「可构造性」校验：解密配置 + 必填项齐全即视为通过，记录 last_test_*。
// （不主动打外部支付 API，避免误触真实计费/限频；构造失败即配置无效。）
func (s *Server) adminTestPaymentChannel(c *gin.Context) {
	if s.keyring == nil {
		fail(c, http.StatusServiceUnavailable, "LAIAI_DATA_KEYS not configured")
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid id")
		return
	}
	var ch PaymentChannel
	if s.db.First(&ch, id).Error != nil {
		fail(c, http.StatusNotFound, "channel not found")
		return
	}
	ok, msg := true, "config valid"
	cfg, e := s.decryptChannelConfig(ch.ConfigEnc)
	if e != nil {
		ok, msg = false, "config decrypt failed"
	} else if buildPaymentEngine(ch.Protocol, cfg) == nil {
		ok, msg = false, "missing required config fields"
	}
	okv := int8(0)
	if ok {
		okv = 1
	}
	s.db.Model(&PaymentChannel{}).Where("id = ?", id).Updates(map[string]any{
		"last_test_at": s.now().Unix(), "last_test_ok": okv, "last_test_msg": msg,
	})
	c.JSON(http.StatusOK, gin.H{"ok": ok, "message": msg})
}

// adminPaymentRoutePreview：GET /api/admin/payment-channels/route-preview ——
// 各支付方式 → 实际命中的渠道（启用 + 支持该方式 + 优先级最高者）。运营排障用。
func (s *Server) adminPaymentRoutePreview(c *gin.Context) {
	chs := s.enabledPaymentChannels()
	methods := []string{"card", "alipay", "wechat", "usdt"}
	type route struct {
		Method      string `json:"method"`
		ChannelCode string `json:"channel_code"`
		ChannelName string `json:"channel_name"`
		Currency    string `json:"currency"`
	}
	out := make([]route, 0, len(methods))
	for _, m := range methods {
		r := route{Method: m}
		for _, ch := range chs { // chs 已按优先级升序，首个支持者即命中
			if ch.PayMethods == "" || containsCSV(ch.PayMethods, m) {
				if s.engineFor(ch.Code) != nil {
					r.ChannelCode, r.ChannelName, r.Currency = ch.Code, ch.DisplayName, ch.Currency
					break
				}
			}
		}
		out = append(out, r)
	}
	c.JSON(http.StatusOK, gin.H{"routes": out})
}

func containsCSV(csv, item string) bool {
	for _, p := range strings.Split(csv, ",") {
		if strings.TrimSpace(p) == item {
			return true
		}
	}
	return false
}
