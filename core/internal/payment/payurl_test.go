package payment

import "testing"

// G4：跳转支付地址只放行 http(s) 绝对 URL（挡 javascript:/data:/相对路径注入）。
func TestValidatePayURL(t *testing.T) {
	ok := []string{
		"https://pay.example.com/checkout/abc",
		"http://gateway.local/pay?order=1",
		"https://mapi.alipay.com/gateway.do?x=y",
	}
	for _, u := range ok {
		if err := validatePayURL(u); err != nil {
			t.Errorf("valid url %q rejected: %v", u, err)
		}
	}
	bad := []string{
		"javascript:alert(document.cookie)",
		"data:text/html,<script>evil()</script>",
		"/relative/path",
		"ftp://host/x",
		"",
		"   ",
		"vbscript:msgbox",
		"//protocol-relative.example.com",
	}
	for _, u := range bad {
		if err := validatePayURL(u); err == nil {
			t.Errorf("unsafe url %q must be rejected", u)
		}
	}
}
