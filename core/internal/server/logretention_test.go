package server

import (
	"context"
	"testing"

	"gorm.io/gorm"
)

// 旧日志清理：按 ext_settings 保留期删旧留新；调用日志(DATETIME) 与 审计日志(unix ms) 各自独立。
func TestRunLogRetention(t *testing.T) {
	srv, db := setupRelayServer(t)
	mustExecLogSchema(t, db) // 0139 起保留清理覆盖视频/访问日志族，测试库需同构表
	db.Exec(`INSERT INTO invocations (org_id,api_key_id,request_id,model,status,created_at) VALUES
		(1,1,'old1','m','success','2000-01-01 00:00:00'),
		(1,1,'new1','m','success', CURRENT_TIMESTAMP)`)
	db.Exec(`INSERT INTO audit_logs (actor,action,target,detail,ip,created_at) VALUES
		('a','old','','d','', 1000),
		('a','new','','d','', 9000000000000000)`)

	// 未配置（默认）→ 不删任何（invRetentionDefault=0，audit 默认 0）
	inv, aud, err := srv.srv.RunLogRetention(context.Background())
	if err != nil || inv != 0 || aud != 0 {
		t.Fatalf("default retention deleted inv=%d aud=%d err=%v, want 0/0", inv, aud, err)
	}

	// 配置 7 天保留 → 删两条旧记录，留两条新记录
	db.Exec(`INSERT INTO ext_settings (name,value) VALUES ('log_retention_days','7'),('audit_log_retention_days','7')`)
	inv, aud, err = srv.srv.RunLogRetention(context.Background())
	if err != nil || inv != 1 || aud != 1 {
		t.Fatalf("retention deleted inv=%d aud=%d err=%v, want 1/1", inv, aud, err)
	}
	var invLeft, auditLeft int64
	db.Raw("SELECT COUNT(*) FROM invocations").Scan(&invLeft)
	db.Raw("SELECT COUNT(*) FROM audit_logs").Scan(&auditLeft)
	if invLeft != 1 || auditLeft != 1 {
		t.Fatalf("after purge inv=%d audit=%d, want 1/1 (recent kept)", invLeft, auditLeft)
	}
	// 幂等：再跑一次旧记录已无 → 删 0
	inv, aud, _ = srv.srv.RunLogRetention(context.Background())
	if inv != 0 || aud != 0 {
		t.Fatalf("second run deleted inv=%d aud=%d, want 0/0", inv, aud)
	}
}

// 0139：控制台访问日志（14 天）与任务生命周期日志（30 天）保留期清理。
// outbox 只删 processed 行（pending 永不删）。sqlite 测试自建所需表。
func TestRunLogRetentionVideoTables(t *testing.T) {
	srv, db := setupRelayServer(t)
	mustExecLogSchema(t, db)
	db.Exec(`INSERT INTO api_request_logs (request_id, started_at) VALUES
		('old1', '2000-01-01 00:00:00'), ('new1', CURRENT_TIMESTAMP)`)
	db.Exec(`INSERT INTO video_request_contexts (request_id, created_at) VALUES
		('old1', '2000-01-01 00:00:00'), ('new1', CURRENT_TIMESTAMP)`)
	db.Exec(`INSERT INTO video_events (id, created_at) VALUES
		('evt-old', '2000-01-01 00:00:00'), ('evt-new', CURRENT_TIMESTAMP)`)
	db.Exec(`INSERT INTO video_attempts (id, observed_at) VALUES (1, '2000-01-01 00:00:00'), (2, CURRENT_TIMESTAMP)`)
	db.Exec(`INSERT INTO video_outbox (event_id, status, created_at) VALUES
		('obx-old-processed', 'processed', '2000-01-01 00:00:00'),
		('obx-old-pending', 'pending', '2000-01-01 00:00:00'),
		('obx-new', 'processed', CURRENT_TIMESTAMP)`)
	db.Exec(`INSERT INTO video_asset_events (event_id, created_at) VALUES
		('ae-old', '2000-01-01 00:00:00'), ('ae-new', CURRENT_TIMESTAMP)`)

	db.Exec(`INSERT INTO ext_settings (name,value) VALUES
		('request_log_retention_days','7'),('task_log_retention_days','7')`)
	if _, _, err := srv.srv.RunLogRetention(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		"SELECT COUNT(*) FROM api_request_logs WHERE request_id='old1'",
		"SELECT COUNT(*) FROM video_request_contexts WHERE request_id='old1'",
		"SELECT COUNT(*) FROM video_events WHERE event_id='evt-old'",
		"SELECT COUNT(*) FROM video_attempts WHERE observed_at < '2020-01-01'",
		"SELECT COUNT(*) FROM video_asset_events WHERE event_id='ae-old'",
	} {
		var n int64
		db.Raw(q).Scan(&n)
		if n != 0 {
			t.Fatalf("old row survived: %s = %d", q, n)
		}
	}
	var newRows int64
	for _, q := range []string{
		"SELECT COUNT(*) FROM api_request_logs WHERE request_id='new1'",
		"SELECT COUNT(*) FROM video_request_contexts WHERE request_id='new1'",
		"SELECT COUNT(*) FROM video_events WHERE id='evt-new'",
		"SELECT COUNT(*) FROM video_outbox WHERE event_id IN ('obx-new','obx-old-pending')",
		"SELECT COUNT(*) FROM video_asset_events WHERE event_id='ae-new'",
	} {
		db.Raw(q).Scan(&newRows)
		if newRows == 0 {
			t.Fatalf("recent/pending row deleted: %s = 0", q)
		}
	}
}

func mustExecLogSchema(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS api_request_logs (request_id TEXT PRIMARY KEY, started_at DATETIME)`,
		`CREATE TABLE IF NOT EXISTS video_request_contexts (request_id TEXT PRIMARY KEY, created_at DATETIME)`,
		`CREATE TABLE IF NOT EXISTS video_events (id TEXT PRIMARY KEY, created_at DATETIME)`,
		`CREATE TABLE IF NOT EXISTS video_attempts (id INTEGER PRIMARY KEY AUTOINCREMENT, observed_at DATETIME)`,
		`CREATE TABLE IF NOT EXISTS video_outbox (event_id TEXT PRIMARY KEY, status TEXT, created_at DATETIME)`,
		`CREATE TABLE IF NOT EXISTS video_asset_events (event_id TEXT PRIMARY KEY, created_at DATETIME)`,
	} {
		if err := db.Exec(q).Error; err != nil {
			t.Fatalf("schema exec: %v", err)
		}
	}
}

// log_retention_days 空 → 回退到启动期 env 默认（invRetentionDefault）。
func TestLogRetentionEnvFallback(t *testing.T) {
	srv, db := setupRelayServer(t)
	mustExecLogSchema(t, db)
	srv.srv.invRetentionDefault = 7 // 模拟 env INVOCATION_RETENTION_DAYS=7
	db.Exec(`INSERT INTO invocations (org_id,api_key_id,request_id,model,status,created_at) VALUES
		(1,1,'old1','m','success','2000-01-01 00:00:00')`)
	// log_retention_days 未配置（默认空）→ 用回退默认 7 → 删旧
	inv, _, err := srv.srv.RunLogRetention(context.Background())
	if err != nil || inv != 1 {
		t.Fatalf("env-fallback retention inv=%d err=%v, want 1", inv, err)
	}
	// 显式设 0 覆盖 env → 关闭清理
	db.Exec(`INSERT INTO invocations (org_id,api_key_id,request_id,model,status,created_at) VALUES
		(1,1,'old2','m','success','2000-01-01 00:00:00')`)
	db.Exec(`INSERT INTO ext_settings (name,value) VALUES ('log_retention_days','0')`)
	inv, _, _ = srv.srv.RunLogRetention(context.Background())
	if inv != 0 {
		t.Fatalf("explicit 0 should disable cleanup, got inv=%d", inv)
	}
}

// 日志统计一览：总行数 + 最旧 + 生效保留期。
func TestAdminLogStats(t *testing.T) {
	srv, db := setupRelayServer(t)
	db.Exec(`INSERT INTO invocations (org_id,api_key_id,request_id,model,status,created_at) VALUES
		(1,1,'s1','m','success','2020-06-01 00:00:00'),
		(1,1,'s2','m','success', CURRENT_TIMESTAMP)`)
	db.Exec(`INSERT INTO ext_settings (name,value) VALUES ('audit_log_retention_days','30')`)

	_, out := srv.doAdmin(t, "GET", "/api/admin/log-stats", nil)
	inv := out["invocations"].(map[string]any)
	if int(inv["total"].(float64)) != 2 {
		t.Fatalf("invocations total = %v, want 2", inv["total"])
	}
	if inv["oldest_at"] == nil {
		t.Fatal("oldest_at should be set for non-empty table")
	}
	ret := out["retention"].(map[string]any)
	if int(ret["audit_log_retention_days"].(float64)) != 30 {
		t.Fatalf("audit retention = %v, want 30", ret["audit_log_retention_days"])
	}
}
