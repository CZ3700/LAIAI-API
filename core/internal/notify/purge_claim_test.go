package notify

// 段三对抗 review 修：outbox 保留清理 + 抢占式认领防多 leader 重复投递。

import (
	"context"
	"log/slog"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

func TestPurgeOldOutbox(t *testing.T) {
	db := newTestDB(t)
	old := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	recent := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	// 旧终态（sent/dead）应删；旧 pending 与新终态应留
	db.Create(&Outbox{OrgID: 1, Event: "e", Payload: "{}", Channel: "email", Status: StatusSent, NextAttemptAt: old, CreatedAt: old})
	db.Create(&Outbox{OrgID: 1, Event: "e", Payload: "{}", Channel: "email", Status: StatusDead, NextAttemptAt: old, CreatedAt: old})
	db.Create(&Outbox{OrgID: 1, Event: "e", Payload: "{}", Channel: "email", Status: StatusPending, NextAttemptAt: old, CreatedAt: old})
	db.Create(&Outbox{OrgID: 1, Event: "e", Payload: "{}", Channel: "email", Status: StatusSent, NextAttemptAt: recent, CreatedAt: recent})

	cutoff := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	n, err := PurgeOldOutbox(db, cutoff, 100)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if n != 2 {
		t.Fatalf("purged %d, want 2 (only old sent+dead)", n)
	}
	var remaining int64
	db.Model(&Outbox{}).Count(&remaining)
	if remaining != 2 {
		t.Fatalf("remaining %d, want 2 (old pending + recent sent)", remaining)
	}
}

// 抢占式认领：并发 ProcessOnce 对同一行只投递一次（模拟多 leader）。
func TestProcessOnceClaimNoDoubleDeliver(t *testing.T) {
	db := newTestDB(t)
	db.Create(&Settings{OrgID: 1, EmailEnabled: true, Events: `["task.completed"]`})
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	db.Create(&Outbox{OrgID: 1, Event: EventTaskCompleted, Payload: `{"task_id":"t1"}`,
		Channel: ChannelEmail, Target: "a@example.com", Status: StatusPending, NextAttemptAt: now})

	var mailCount atomic.Int64
	mk := func() *Dispatcher {
		return &Dispatcher{DB: db, Log: slog.New(slog.NewTextHandler(os.Stderr, nil)),
			Mail: func(to, s, b string) error { mailCount.Add(1); return nil }, Now: func() time.Time { return now }}
	}
	// 两个"leader"顺序处理同一批（抢占保证只发一次；真正并发下 UPDATE ... WHERE status=pending 串行化）
	s1, _ := mk().ProcessOnce(context.TODO(), 10)
	s2, _ := mk().ProcessOnce(context.TODO(), 10)
	if s1 != 1 {
		t.Fatalf("first leader sent %d, want 1", s1)
	}
	if s2 != 0 {
		t.Fatalf("second leader sent %d, want 0 (already claimed+sent)", s2)
	}
	if mailCount.Load() != 1 {
		t.Fatalf("mail sent %d times, want exactly 1 (no double-deliver)", mailCount.Load())
	}
	var row Outbox
	db.First(&row)
	if row.Status != StatusSent {
		t.Fatalf("status = %q, want sent", row.Status)
	}
}
