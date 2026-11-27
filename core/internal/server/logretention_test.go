package server

import (
	"context"
	"testing"
)

func TestLogContentRetentionPreservesInvocationRow(t *testing.T) {
	fixture, db := setupRelayServer(t)
	if err := db.Exec("INSERT INTO invocations (org_id, api_key_id, request_id, model, status, created_at) VALUES (1, 1, 'retained', 'm', 'success', '2000-01-01 00:00:00')").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.srv.CleanupLogContent(context.Background(), 7); err != nil {
		t.Fatal(err)
	}
	var n int64
	if err := db.Raw("SELECT COUNT(*) FROM invocations WHERE request_id = 'retained'").Scan(&n).Error; err != nil || n != 1 {
		t.Fatalf("retained invocation count = %d, err = %v", n, err)
	}
}

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
