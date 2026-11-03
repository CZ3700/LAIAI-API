package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/laiai/laiai-next/core/internal/watermark"
	"gorm.io/gorm"
)

func TestWatermarkUserGrantAdminFlow(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	schema := `CREATE TABLE users (id INTEGER PRIMARY KEY, email TEXT, display_name TEXT, status INTEGER, role TEXT);
CREATE TABLE video_watermark_settings (id INTEGER PRIMARY KEY, enabled INTEGER, updated_by INTEGER, updated_at DATETIME);
CREATE TABLE video_watermark_users (user_id INTEGER PRIMARY KEY, granted_by INTEGER, granted_at DATETIME);
CREATE TABLE audit_logs (id INTEGER PRIMARY KEY AUTOINCREMENT, actor TEXT, action TEXT, target TEXT, detail TEXT, ip TEXT, created_at INTEGER);
INSERT INTO users (id,email,display_name,status,role) VALUES (1,'admin@test.invalid','Admin',1,'superadmin'),(7,'user@test.invalid','Selected',1,'user'),(8,'disabled@test.invalid','Disabled',2,'user');
INSERT INTO video_watermark_settings (id,enabled,updated_by,updated_at) VALUES (1,1,1,'2026-01-01');`
	if err := db.Exec(schema).Error; err != nil {
		t.Fatal(err)
	}
	s := &Server{db: db, now: func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) }}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/video/watermark", s.getWatermarkSetting)
	r.PUT("/video/watermark/users/:user_id", func(c *gin.Context) {
		c.Set("actor_id", uint64(1))
		s.putWatermarkUser(c)
	})
	call := func(method, path, body string) (int, map[string]any) {
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		var out map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return w.Code, out
	}
	if code, _ := call(http.MethodPut, "/video/watermark/users/8", `{"enabled":true}`); code != http.StatusNotFound {
		t.Fatalf("inactive user granted: %d", code)
	}
	if code, _ := call(http.MethodPut, "/video/watermark/users/7", `{"enabled":true}`); code != http.StatusOK {
		t.Fatalf("grant failed: %d", code)
	}
	if code, out := call(http.MethodGet, "/video/watermark", ""); code != http.StatusOK || len(out["users"].([]any)) != 1 {
		t.Fatalf("grant not listed: %d %+v", code, out)
	}
	if code, _ := call(http.MethodPut, "/video/watermark/users/7", `{"enabled":false}`); code != http.StatusOK {
		t.Fatalf("revoke failed: %d", code)
	}
	if allowed, err := watermark.UserAllowed(db, 7); err != nil || allowed {
		t.Fatalf("revoke not effective: %v %v", allowed, err)
	}
	var count int64
	if err := db.Model(&AuditLog{}).Where("action = ?", "video.watermark.user").Count(&count).Error; err != nil || count != 2 {
		t.Fatalf("grant/revoke audit missing: %d %v", count, err)
	}
}
