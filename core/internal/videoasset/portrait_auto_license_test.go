package videoasset

import (
	"context"
	"testing"
	"time"
)

// buildSucceededPortrait 走完整链路（创建会话→派发→核实成功→自动授权），
// 返回会话行与组行，供各用例断言。
func buildSucceededPortrait(t *testing.T, s *Service, now *time.Time, auto bool) (Portrait, Group) {
	t.Helper()
	s.options.AutoLicense = auto
	ctx := context.Background()
	if err := s.ConfigureConsole(ConsoleOptions{CallbackURL: "https://customer.example/return"}); err != nil {
		t.Fatal(err)
	}
	c := CreateContext{CallerKind: "session_user", OrgID: 1, UserID: 22, RequestID: "auto-license", IdempotencyKey: "auto-license"}
	p, _, err := s.CreateConsolePortrait(ctx, c, PortraitCreate{Name: "Portrait"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ProcessOnce(ctx); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(16 * time.Second)
	if err := s.ProcessOnce(ctx); err != nil {
		t.Fatal(err)
	}
	var row Portrait
	if err := s.db.Where("session_id = ?", p.ID).Take(&row).Error; err != nil || row.SessionStatus != "succeeded" || row.GroupID == nil {
		t.Fatalf("portrait must succeed first: %+v %v", row, err)
	}
	var g Group
	if err := s.db.Where("group_id = ?", *row.GroupID).Take(&g).Error; err != nil {
		t.Fatal(err)
	}
	return row, g
}

// TestPortraitAutoLicenseRegistersOnVerify 验证认证成功即自动登记
// video_generation 许可：license_status=verified、GetGroup 为 authorized，
// 且证据来源为官方核实响应（license_verified 事件）。
func TestPortraitAutoLicenseRegistersOnVerify(t *testing.T) {
	s, db, now, _ := setup(t)
	row, _ := buildSucceededPortrait(t, s, now, true)
	if row.LicenseStatus != "verified" || row.LicenseSource != "verified_provider_license" || row.LicenseScope != "video_generation" || row.ValidFrom == nil || row.ValidUntil == nil || row.ReverifyDueAt == nil {
		t.Fatalf("auto license not registered: %+v", row)
	}
	if s.licenseStatus(row, "video_generation") != "authorized" {
		t.Fatal("license must be authorized right after verification")
	}
	var events []Event
	if err := db.Where("resource_id = ? AND kind = ?", row.ID, "license_verified").Find(&events).Error; err != nil || len(events) != 1 {
		t.Fatalf("missing license_verified audit: %+v %v", events, err)
	}
}

// TestPortraitAutoLicenseDisabledKeepsReview 验证关闭开关时回到人工流程：
// 认证成功后许可保持 needs_review。
func TestPortraitAutoLicenseDisabledKeepsReview(t *testing.T) {
	s, _, now, _ := setup(t)
	row, _ := buildSucceededPortrait(t, s, now, false)
	if row.LicenseStatus != "unknown" || s.licenseStatus(row, "video_generation") != "needs_review" {
		t.Fatalf("disabled auto license must keep needs_review: %+v", row)
	}
}

// TestPortraitLicenseReverifyByUpstreamGroup 验证官方组状态续验：
// 复验到期→降级 needs_review→GetAssetGroup 确认组仍在→自动续验恢复
// authorized；官方组被删（404）→ 不续验，保持降级。
func TestPortraitLicenseReverifyByUpstreamGroup(t *testing.T) {
	s, db, now, provider := setup(t)
	row, _ := buildSucceededPortrait(t, s, now, true)
	ctx := context.Background()
	// 复验到期：licenseStatus 降级 needs_review。
	if err := db.Model(&Portrait{}).Where("session_id = ?", row.ID).Updates(map[string]any{"reverify_due_at": now.Add(-time.Hour), "last_verified_at": now.Add(-25 * time.Hour)}).Error; err != nil {
		t.Fatal(err)
	}
	var stale Portrait
	if err := db.Where("session_id = ?", row.ID).Take(&stale).Error; err != nil || s.licenseStatus(stale, "video_generation") != "needs_review" {
		t.Fatalf("expired reverify must degrade: %+v", stale)
	}
	for i := 0; i < 3; i++ {
		*now = now.Add(16 * time.Second)
		if err := s.ProcessOnce(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var renewed Portrait
	if err := db.Where("session_id = ?", row.ID).Take(&renewed).Error; err != nil || s.licenseStatus(renewed, "video_generation") != "authorized" {
		t.Fatalf("upstream group active must renew the license: %+v", renewed)
	}
	// 官方组被删：再次到期后不续验，保持 needs_review。
	provider.groupMissing = true
	if err := db.Model(&Portrait{}).Where("session_id = ?", row.ID).Updates(map[string]any{"reverify_due_at": now.Add(-time.Hour), "last_verified_at": now.Add(-25 * time.Hour)}).Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		*now = now.Add(16 * time.Second)
		if err := s.ProcessOnce(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var degraded Portrait
	if err := db.Where("session_id = ?", row.ID).Take(&degraded).Error; err != nil || s.licenseStatus(degraded, "video_generation") != "needs_review" {
		t.Fatalf("upstream group deleted must not renew: %+v", degraded)
	}
}
