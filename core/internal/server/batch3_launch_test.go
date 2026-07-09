package server

// 上线冲刺批次 3 行为测试：
//   - per-key RPM 限流（0041）：key 级窗口独立于模型级限流；429 带 Retry-After（relayErr 统一出口）
//   - 余额低预警（0042）：端点权限（owner/admin 可设、member 403）+ 扫描（低于阈值发 owner 邮件、
//     24h 防重发、阈值以上不发、mailer 未激活整段跳过）

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/laiai/laiai-next/core/internal/identity"
)

// recordMailer：批次 3 测试用——记录全部外发邮件。
type recordMailer struct {
	tos    []string
	bodies []string
}

func (m *recordMailer) Active() bool { return true }
func (m *recordMailer) Send(to, _, body string) error {
	m.tos = append(m.tos, to)
	m.bodies = append(m.bodies, body)
	return nil
}

func TestPerKeyRPMLimit(t *testing.T) {
	srv, _ := setupRelayServer(t)
	up := mockUpstreamJSON(t, 5, 7)
	setupChannelAndPrice(t, srv, up.URL)
	_, _ = seedUserWithCredit(t, srv, "rpmkey@example.com", "10")
	_, login := srv.do(t, "POST", "/api/auth/login", "", map[string]string{"email": "rpmkey@example.com", "password": "longenough1"})
	token := login["token"].(string)

	// 建一把 rpm_limit=2 的 key（同 org 的第二把 key）
	w, out := srv.do(t, "POST", "/api/keys", token, map[string]any{"name": "limited", "rpm_limit": 2})
	if w.Code != http.StatusCreated {
		t.Fatalf("create key = %d %v", w.Code, out)
	}
	limited := out["key"].(string)

	call := func(k string) *int {
		w, _ := srv.do(t, "POST", "/v1/chat/completions", k, map[string]any{
			"model": "test-model", "messages": []map[string]string{{"role": "user", "content": "hi"}},
		})
		return &w.Code
	}
	if c := call(limited); *c != 200 {
		t.Fatalf("1st call = %d, want 200", *c)
	}
	if c := call(limited); *c != 200 {
		t.Fatalf("2nd call = %d, want 200", *c)
	}
	// 第 3 次触顶 → 429 + Retry-After（relayErr 统一出口）
	w3, _ := srv.do(t, "POST", "/v1/chat/completions", limited, map[string]any{
		"model": "test-model", "messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	if w3.Code != http.StatusTooManyRequests {
		t.Fatalf("3rd call = %d, want 429", w3.Code)
	}
	if w3.Header().Get("Retry-After") == "" {
		t.Fatalf("429 must carry Retry-After header")
	}
	// 列表回显 rpm_limit
	_, keysOut := srv.do(t, "GET", "/api/keys", token, nil)
	found := false
	for _, item := range keysOut["keys"].([]any) {
		if item.(map[string]any)["name"] == "limited" {
			found = true
			if item.(map[string]any)["rpm_limit"].(float64) != 2 {
				t.Fatalf("rpm_limit not echoed: %v", item)
			}
		}
	}
	if !found {
		t.Fatal("limited key not in list")
	}
	// 负数拒绝
	if w, _ := srv.do(t, "POST", "/api/keys", token, map[string]any{"name": "bad", "rpm_limit": -1}); w.Code != http.StatusBadRequest {
		t.Fatalf("negative rpm_limit = %d, want 400", w.Code)
	}
}

func TestBalanceAlertEndpointPermissions(t *testing.T) {
	srv, _ := setupRelayServer(t)
	// owner（个人 org）：可读可设
	srv.do(t, "POST", "/api/auth/register", "", map[string]string{"email": "alertowner@example.com", "password": "longenough1"})
	_, login := srv.do(t, "POST", "/api/auth/login", "", map[string]string{"email": "alertowner@example.com", "password": "longenough1"})
	ownerTok := login["token"].(string)
	ownerOrg := login["org_id"].(float64)

	if w, _ := srv.do(t, "PUT", "/api/org/balance-alert", ownerTok, map[string]string{"low_balance_alert_usd": "5"}); w.Code != 200 {
		t.Fatalf("owner set alert = %d, want 200", w.Code)
	}
	_, got := srv.do(t, "GET", "/api/org/balance-alert", ownerTok, nil)
	if got["low_balance_alert_usd"].(string) != "5" {
		t.Fatalf("get alert = %v, want 5", got["low_balance_alert_usd"])
	}
	// 非法值
	if w, _ := srv.do(t, "PUT", "/api/org/balance-alert", ownerTok, map[string]string{"low_balance_alert_usd": "-1"}); w.Code != 400 {
		t.Fatalf("negative threshold = %d, want 400", w.Code)
	}

	// member：邀请→接受→切换到 owner 的 org，PUT 应 403（GET 应 200——成员可见）
	srv.do(t, "POST", "/api/auth/register", "", map[string]string{"email": "alertmate@example.com", "password": "longenough1"})
	w, inv := srv.do(t, "POST", "/api/org/members", ownerTok, map[string]string{"email": "alertmate@example.com", "role": "member"})
	if w.Code != 201 {
		t.Fatalf("invite member = %d %v", w.Code, inv)
	}
	inviteID := inv["invitation_id"].(float64)
	_, mateLogin := srv.do(t, "POST", "/api/auth/login", "", map[string]string{"email": "alertmate@example.com", "password": "longenough1"})
	mateBaseTok := mateLogin["token"].(string)
	if w, out := srv.do(t, "POST", fmt.Sprintf("/api/invitations/%.0f/accept", inviteID), mateBaseTok, nil); w.Code != 200 {
		t.Fatalf("accept invite = %d %v", w.Code, out)
	}
	_, sw := srv.do(t, "POST", "/api/auth/switch", mateBaseTok, map[string]any{"org_id": ownerOrg})
	mateTok := sw["token"].(string)
	if w, _ := srv.do(t, "GET", "/api/org/balance-alert", mateTok, nil); w.Code != 200 {
		t.Fatalf("member get alert = %d, want 200", w.Code)
	}
	if w, _ := srv.do(t, "PUT", "/api/org/balance-alert", mateTok, map[string]string{"low_balance_alert_usd": "9"}); w.Code != http.StatusForbidden {
		t.Fatalf("member set alert = %d, want 403", w.Code)
	}
}

func TestBalanceAlertSweep(t *testing.T) {
	srv, db := setupRelayServer(t)
	// owner + $2 余额 + 阈值 $5 → 低于阈值应发信
	_, orgID := seedUserWithCredit(t, srv, "alertsweep@example.com", "2")
	_, login := srv.do(t, "POST", "/api/auth/login", "", map[string]string{"email": "alertsweep@example.com", "password": "longenough1"})
	token := login["token"].(string)
	if w, _ := srv.do(t, "PUT", "/api/org/balance-alert", token, map[string]string{"low_balance_alert_usd": "5"}); w.Code != 200 {
		t.Fatalf("set alert failed")
	}

	rec := &recordMailer{}
	srv.srv.mailer = rec // 同包测试直接替换（生产为 settingsMailer）

	checked, sent := srv.srv.SweepLowBalanceAlerts(context.Background(), 100)
	if checked < 1 || sent != 1 {
		t.Fatalf("sweep = (checked %d, sent %d), want (≥1, 1)", checked, sent)
	}
	if len(rec.tos) != 1 || rec.tos[0] != "alertsweep@example.com" {
		t.Fatalf("mail to = %v, want owner email", rec.tos)
	}
	if !strings.Contains(rec.bodies[0], "$2.00") || !strings.Contains(rec.bodies[0], "$5.00") {
		t.Fatalf("mail body missing amounts: %q", rec.bodies[0])
	}

	// 24h 防重发：立刻再扫不发
	if _, sent2 := srv.srv.SweepLowBalanceAlerts(context.Background(), 100); sent2 != 0 {
		t.Fatalf("second sweep sent %d, want 0 (24h cooldown)", sent2)
	}

	// 改阈值会清 alerted_at（新阈值立即生效）——降到 $1（高于余额 $2 → 不发）
	if w, _ := srv.do(t, "PUT", "/api/org/balance-alert", token, map[string]string{"low_balance_alert_usd": "1"}); w.Code != 200 {
		t.Fatalf("reset threshold failed")
	}
	var stamped int64
	db.Model(&identity.Organization{}).Where("id = ? AND low_balance_alerted_at IS NOT NULL", uint64(orgID)).Count(&stamped)
	if stamped != 0 {
		t.Fatalf("alerted_at not cleared on threshold change")
	}
	if _, sent3 := srv.srv.SweepLowBalanceAlerts(context.Background(), 100); sent3 != 0 {
		t.Fatalf("above-threshold sweep sent %d, want 0", sent3)
	}
}
