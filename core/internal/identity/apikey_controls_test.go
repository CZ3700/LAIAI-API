package identity

import (
	"testing"

	"github.com/shopspring/decimal"
)

func TestAllowsModel(t *testing.T) {
	k := &APIKey{} // 空白名单 = 不限
	if !k.AllowsModel("anything") {
		t.Fatal("empty limits should allow all")
	}
	k.ModelLimits = "gpt-5.2, claude-4"
	if !k.AllowsModel("gpt-5.2") || !k.AllowsModel("claude-4") {
		t.Fatal("whitelisted models should be allowed")
	}
	if k.AllowsModel("glm-5") {
		t.Fatal("non-whitelisted model should be denied")
	}
}

func TestAllowsIP(t *testing.T) {
	k := &APIKey{} // 空 = 不限
	if !k.AllowsIP("1.2.3.4") {
		t.Fatal("empty allowlist should allow all")
	}
	k.AllowIPs = "1.2.3.4\n10.0.0.0/8"
	if !k.AllowsIP("1.2.3.4") {
		t.Fatal("exact ip should match")
	}
	if !k.AllowsIP("10.5.6.7") {
		t.Fatal("ip in CIDR should match")
	}
	if k.AllowsIP("8.8.8.8") {
		t.Fatal("ip outside allowlist should be denied")
	}
}

func TestQuotaExhausted(t *testing.T) {
	k := &APIKey{} // 0 limit = 无限
	if k.QuotaExhausted() {
		t.Fatal("zero limit means unlimited")
	}
	k.QuotaLimit = decimal.RequireFromString("10")
	k.UsedAmount = decimal.RequireFromString("9.99")
	if k.QuotaExhausted() {
		t.Fatal("under limit should not be exhausted")
	}
	k.UsedAmount = decimal.RequireFromString("10")
	if !k.QuotaExhausted() {
		t.Fatal("at limit should be exhausted")
	}
}
