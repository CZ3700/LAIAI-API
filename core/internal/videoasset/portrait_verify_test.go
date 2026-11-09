package videoasset

import (
	"context"
	"testing"
	"time"
)

// TestPortraitVerifyEmptyGroupStaysPending 验证官方"认证尚未完成"的合法响应
// （200 + 空 GroupId）不再被判为 provider_response_invalid：会话保持 pending、
// 核实查询正常完结；官方返回非空 GroupId 后仍能正常转 succeeded。
func TestPortraitVerifyEmptyGroupStaysPending(t *testing.T) {
	s, db, now, provider := setup(t)
	ctx := context.Background()
	if err := s.ConfigureConsole(ConsoleOptions{CallbackURL: "https://customer.example/return"}); err != nil {
		t.Fatal(err)
	}
	c := CreateContext{CallerKind: "session_user", OrgID: 1, UserID: 22, RequestID: "portrait-empty-group", IdempotencyKey: "portrait-empty-group"}
	p, _, err := s.CreateConsolePortrait(ctx, c, PortraitCreate{Name: "Portrait"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ProcessOnce(ctx); err != nil {
		t.Fatal(err)
	}
	provider.emptyPortraitGroup = true
	for i := 0; i < 3; i++ {
		*now = now.Add(16 * time.Second)
		if err := s.ProcessOnce(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var row Portrait
	if err := db.Where("session_id = ?", p.ID).Take(&row).Error; err != nil || row.SessionStatus != "pending" {
		t.Fatalf("empty GroupId must keep the session pending: %+v %v", row, err)
	}
	var op Operation
	if err := db.Where("org_id = ? AND resource_id = ? AND action = ?", 1, p.ID, "GetVisualValidateResult").Order("created_at DESC").First(&op).Error; err != nil {
		t.Fatal(err)
	}
	if op.Status != "confirmed" || op.SafeErrorCode != "" {
		t.Fatalf("empty GroupId query must complete without provider_response_invalid: status=%s code=%s", op.Status, op.SafeErrorCode)
	}
	provider.emptyPortraitGroup = false
	*now = now.Add(16 * time.Second)
	if err := s.ProcessOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.Where("session_id = ?", p.ID).Take(&row).Error; err != nil || row.SessionStatus != "succeeded" || row.GroupID == nil {
		t.Fatalf("non-empty GroupId must complete the session: %+v %v", row, err)
	}
}
