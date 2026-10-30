package server

import (
	"testing"

	"gorm.io/gorm"
)

// 视频模型级限额端点（superadmin，0136）：官方模型校验、未发布拒绝、
// 合法更新落库 + 审计、越界拒绝。视频表不在共享 testSchema 里，本测试自带。
func TestAdminVideoModelLimits(t *testing.T) {
	srv, db := setupRelayServer(t)
	mustExecSchema(t, db, `CREATE TABLE IF NOT EXISTS video_model_configs (
		public_model TEXT PRIMARY KEY, channel_id INTEGER, enabled INTEGER,
		max_concurrent_tasks INTEGER, create_rpm INTEGER, updated_at DATETIME)`)
	mustExecSchema(t, db, `INSERT INTO channels (id, name, provider, base_url, credential_enc, models, weight, status) VALUES (11, 'laiai-volcengine', 'volcengine', 'https://ark.cn-beijing.volces.com/api/v3', 'x', 'seedance-2.5', 1, 1)`)
	mustExecSchema(t, db, `INSERT INTO video_model_configs (public_model, channel_id, enabled, max_concurrent_tasks, create_rpm, updated_at)
		VALUES ('seedance-2.5', 11, 1, 0, 0, CURRENT_TIMESTAMP)`)

	// GET：官方模型全量列出，2.5 已发布且带渠道名，其余 published=false
	w, out := srv.doAdmin(t, "GET", "/api/admin/video/model-limits", nil)
	if w.Code != 200 {
		t.Fatalf("list = %d %s", w.Code, w.Body.String())
	}
	models := out["models"].([]any)
	if len(models) != 4 {
		t.Fatalf("models = %d, want 4 official entries", len(models))
	}
	first := models[0].(map[string]any)
	if first["model"] != "seedance-2.5" || first["published"] != true || first["channel_name"] != "laiai-volcengine" {
		t.Fatalf("first row = %v", first)
	}

	// PUT 非官方模型 → 400
	if w, _ := srv.doAdmin(t, "PUT", "/api/admin/video/model-limits/kling-1.0", map[string]any{"max_concurrent_tasks": 1, "create_rpm": 1}); w.Code != 400 {
		t.Fatalf("non-official model = %d, want 400", w.Code)
	}
	// PUT 未发布模型 → 404
	if w, _ := srv.doAdmin(t, "PUT", "/api/admin/video/model-limits/seedance-2.0-fast", map[string]any{"max_concurrent_tasks": 1, "create_rpm": 1}); w.Code != 404 {
		t.Fatalf("unpublished model = %d, want 404", w.Code)
	}
	// PUT 越界 → 400
	if w, _ := srv.doAdmin(t, "PUT", "/api/admin/video/model-limits/seedance-2.5", map[string]any{"max_concurrent_tasks": -1, "create_rpm": 1}); w.Code != 400 {
		t.Fatalf("negative concurrency = %d, want 400", w.Code)
	}

	// PUT 合法 → 200 + 落库 + 审计
	w, out = srv.doAdmin(t, "PUT", "/api/admin/video/model-limits/seedance-2.5", map[string]any{"max_concurrent_tasks": 20, "create_rpm": 30})
	if w.Code != 200 {
		t.Fatalf("update = %d %s", w.Code, w.Body.String())
	}
	var limits struct {
		Conc int
		Rpm  int
	}
	if err := db.Raw(`SELECT max_concurrent_tasks AS conc, create_rpm AS rpm FROM video_model_configs WHERE public_model = 'seedance-2.5'`).Scan(&limits).Error; err != nil {
		t.Fatal(err)
	}
	if limits.Conc != 20 || limits.Rpm != 30 {
		t.Fatalf("persisted = conc %d rpm %d, want 20/30", limits.Conc, limits.Rpm)
	}
	var audits int64
	db.Table("audit_logs").Where("action = ? AND target = ?", "video_model_limits", "seedance-2.5").Count(&audits)
	if audits != 1 {
		t.Fatalf("audit rows = %d, want 1", audits)
	}
}

func mustExecSchema(t *testing.T, db *gorm.DB, q string) {
	t.Helper()
	if err := db.Exec(q).Error; err != nil {
		t.Fatalf("schema exec: %v", err)
	}
}
