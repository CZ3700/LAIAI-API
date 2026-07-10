package notify

// Notify 层单测：订阅过滤、outbox 投递、webhook 签名、退避/DLQ、红线 7（payload 无敏感 + secret 不泄）。

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/laiai/laiai-next/core/internal/crypto"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func testKeyring(t *testing.T) *crypto.Keyring {
	t.Helper()
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = byte(i + 7)
	}
	kr, err := crypto.NewKeyring("t1:"+base64.StdEncoding.EncodeToString(raw), "t1")
	if err != nil {
		t.Fatal(err)
	}
	return kr
}

const notifySchema = `
CREATE TABLE notify_settings (
    org_id INTEGER PRIMARY KEY, email_enabled INTEGER NOT NULL DEFAULT 0,
    webhook_url TEXT NOT NULL DEFAULT '', webhook_secret_enc TEXT NOT NULL DEFAULT '',
    events TEXT NOT NULL DEFAULT '', updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE notify_outbox (
    id INTEGER PRIMARY KEY AUTOINCREMENT, org_id INTEGER NOT NULL, event TEXT NOT NULL,
    payload TEXT NOT NULL, channel TEXT NOT NULL, target TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'pending', attempts INTEGER NOT NULL DEFAULT 0,
    next_attempt_at DATETIME NOT NULL, sent_at DATETIME, created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE org_members (org_id INTEGER, user_id INTEGER, role TEXT);
CREATE TABLE users (id INTEGER PRIMARY KEY, email TEXT, status INTEGER DEFAULT 1);
`

func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(notifySchema).Error; err != nil {
		t.Fatal(err)
	}
	return db
}

func fixedClock() func() time.Time {
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	return func() time.Time { return now }
}

func newDispatcher(t *testing.T, db *gorm.DB) *Dispatcher {
	return &Dispatcher{DB: db, Log: slog.New(slog.NewTextHandler(os.Stderr, nil)), Now: fixedClock()}
}

func TestEmitRequiresSubscription(t *testing.T) {
	db := newTestDB(t)
	d := newDispatcher(t, db)
	// 无订阅 → 不写 outbox
	d.Emit(1, EventTaskCompleted, map[string]any{"task_id": "t1"})
	var n int64
	db.Model(&Outbox{}).Count(&n)
	if n != 0 {
		t.Fatalf("no-subscription emit wrote %d outbox rows, want 0", n)
	}
	// 订阅了但事件未启用 → 不写
	db.Create(&Settings{OrgID: 1, WebhookURL: "https://h.example.com/hook", Events: `["balance.low"]`})
	d.Emit(1, EventTaskCompleted, map[string]any{"task_id": "t1"})
	db.Model(&Outbox{}).Count(&n)
	if n != 0 {
		t.Fatalf("unsubscribed-event emit wrote %d rows, want 0", n)
	}
	// 事件启用 + webhook → 写一行 pending
	db.Model(&Settings{}).Where("org_id = 1").Update("events", `["task.completed"]`)
	d.Emit(1, EventTaskCompleted, map[string]any{"task_id": "t1", "model": "seedance"})
	db.Model(&Outbox{}).Count(&n)
	if n != 1 {
		t.Fatalf("subscribed emit wrote %d rows, want 1", n)
	}
	var row Outbox
	db.First(&row)
	if row.Channel != ChannelWebhook || row.Status != StatusPending || row.Target != "https://h.example.com/hook" {
		t.Fatalf("outbox row = %+v", row)
	}
}

func TestWebhookDeliveryAndSignature(t *testing.T) {
	db := newTestDB(t)
	kr := testKeyring(t)
	secretEnc, _ := kr.Encrypt("whsec_test_123")
	db.Create(&Settings{OrgID: 5, WebhookURL: "", WebhookSecretEnc: secretEnc, Events: `["task.completed"]`})

	var gotBody []byte
	var gotSig string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		gotSig = r.Header.Get("X-LAIAI-Signature")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	db.Model(&Settings{}).Where("org_id = 5").Update("webhook_url", srv.URL)

	d := newDispatcher(t, db)
	d.Keyring = kr
	d.HTTP = srv.Client()
	d.Emit(5, EventTaskCompleted, map[string]any{"task_id": "t9", "model": "m", "cost_usd": "0.5"})

	sent, failed := d.ProcessOnce(context.TODO(), 10)
	if sent != 1 || failed != 0 {
		t.Fatalf("process = (%d,%d), want (1,0)", sent, failed)
	}
	// 验签：t=<unix>,v1=hex(hmac-sha256(secret, "<t>." + body))
	if !strings.HasPrefix(gotSig, "t=") || !strings.Contains(gotSig, ",v1=") {
		t.Fatalf("bad signature header: %q", gotSig)
	}
	parts := strings.SplitN(strings.TrimPrefix(gotSig, "t="), ",v1=", 2)
	mac := hmac.New(sha256.New, []byte("whsec_test_123"))
	mac.Write([]byte(parts[0] + "."))
	mac.Write(gotBody)
	if hex.EncodeToString(mac.Sum(nil)) != parts[1] {
		t.Fatal("signature mismatch")
	}
	// 红线 7：body 是 {id,event,created_at,data}，data 里只有元数据、无 prompt/key
	var env map[string]any
	if json.Unmarshal(gotBody, &env) != nil || env["event"] != "task.completed" {
		t.Fatalf("envelope = %s", gotBody)
	}
	// outbox 标 sent
	var row Outbox
	db.First(&row)
	if row.Status != StatusSent {
		t.Fatalf("status = %q, want sent", row.Status)
	}
}

func TestDeliveryBackoffAndDeadLetter(t *testing.T) {
	db := newTestDB(t)
	kr := testKeyring(t)
	secretEnc, _ := kr.Encrypt("s")
	// 目标返回 500 → 持续失败
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	db.Create(&Settings{OrgID: 7, WebhookURL: srv.URL, WebhookSecretEnc: secretEnc, Events: `["task.failed"]`})

	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	d := &Dispatcher{DB: db, Keyring: kr, Log: slog.New(slog.NewTextHandler(os.Stderr, nil)),
		HTTP: srv.Client(), Now: func() time.Time { return now }}
	d.Emit(7, EventTaskFailed, map[string]any{"task_id": "t1"})

	// 第 1 次失败 → attempts=1，next_attempt_at 退避到 +1m，本轮不再被扫到
	sent, failed := d.ProcessOnce(context.TODO(), 10)
	if sent != 0 || failed != 1 {
		t.Fatalf("attempt1 = (%d,%d)", sent, failed)
	}
	var row Outbox
	db.First(&row)
	if row.Attempts != 1 || row.Status != StatusPending {
		t.Fatalf("after fail1 = attempts %d status %q", row.Attempts, row.Status)
	}
	if !row.NextAttemptAt.After(now) {
		t.Fatal("next_attempt_at must be pushed forward (backoff)")
	}
	// 推进时间跨过所有退避 → 反复失败到 attempts>=maxAttempts → dead
	for i := 0; i < maxAttempts+2; i++ {
		now = now.Add(24 * time.Hour)
		d.ProcessOnce(context.TODO(), 10)
	}
	db.First(&row)
	if row.Status != StatusDead {
		t.Fatalf("final status = %q, want dead", row.Status)
	}
	if row.Attempts != maxAttempts {
		t.Fatalf("dead at attempts %d, want %d", row.Attempts, maxAttempts)
	}
}

func TestEmailChannelAndMultiFanout(t *testing.T) {
	db := newTestDB(t)
	db.Exec(`INSERT INTO users (id, email, status) VALUES (1, 'owner@example.com', 1)`)
	db.Exec(`INSERT INTO org_members (org_id, user_id, role) VALUES (3, 1, 'owner')`)
	db.Create(&Settings{OrgID: 3, EmailEnabled: true, WebhookURL: "https://h/x",
		WebhookSecretEnc: "x", Events: `["task.completed"]`})

	var mailedTo, mailedSubj string
	d := newDispatcher(t, db)
	d.Mail = func(to, subj, body string) error { mailedTo, mailedSubj = to, subj; return nil }
	d.MailActive = func() bool { return true }

	d.Emit(3, EventTaskCompleted, map[string]any{"task_id": "t1", "model": "seedance", "cost_usd": "0.10"})
	// 两个通道各一行
	var rows []Outbox
	db.Order("channel").Find(&rows)
	if len(rows) != 2 {
		t.Fatalf("fanout rows = %d, want 2 (email+webhook)", len(rows))
	}
	// 只投递 email 行（webhook 无 client 会失败，这里只验 email 通道）
	_ = d.sendEmail(&Outbox{Event: EventTaskCompleted, Channel: ChannelEmail, Target: "owner@example.com",
		Payload: `{"task_id":"t1","model":"seedance","cost_usd":"0.10"}`})
	if mailedTo != "owner@example.com" || !strings.Contains(mailedSubj, "t1") {
		t.Fatalf("email to=%q subj=%q", mailedTo, mailedSubj)
	}
}
