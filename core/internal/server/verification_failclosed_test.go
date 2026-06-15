package server

import (
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// inactiveMailer：模拟 logMailer（未配 SMTP）——Active()=false，Send 永不返错。
type inactiveMailer struct{}

func (inactiveMailer) Active() bool              { return false }
func (inactiveMailer) Send(_, _, _ string) error { return nil }

func setupServerWithMailer(t *testing.T, m interface {
	Active() bool
	Send(to, subject, body string) error
}) (*gin999, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.Exec(testSchema).Error; err != nil {
		t.Fatalf("schema: %v", err)
	}
	engine, srv := NewWithServer(db, Options{AuthSecret: testSecret, AuthRateBurst: 100000, Mailer: m})
	return &gin999{engine: engine, srv: srv}, db
}

// P2-5 fail-closed：验证已启用但只有非真实投递 mailer → send-verification 必须 503，
// 绝不谎报已发（否则验证码只进日志、真实用户收不到，验证名存实亡）。
func TestSendVerificationFailsClosedWithoutRealMailer(t *testing.T) {
	srv, db := setupServerWithMailer(t, inactiveMailer{})
	db.Exec(`INSERT INTO ext_settings (name,value) VALUES ('email_verification_enabled','true')`)

	w, _ := srv.do(t, "POST", "/api/auth/send-verification", "", map[string]string{"email": "fc@gmail.com"})
	if w.Code != 503 {
		t.Fatalf("send-verification with inactive mailer + verification on = %d, want 503 (fail-closed)", w.Code)
	}

	// 关掉验证（开发态）→ logMailer 退化可用，不阻断
	db.Exec(`UPDATE ext_settings SET value='false' WHERE name='email_verification_enabled'`)
	if w, _ := srv.do(t, "POST", "/api/auth/send-verification", "", map[string]string{"email": "fc2@gmail.com"}); w.Code != 200 {
		t.Fatalf("send-verification with verification off = %d, want 200 (dev log fallback)", w.Code)
	}
}
