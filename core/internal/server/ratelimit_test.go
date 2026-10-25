package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// 认证端点限流（审查 H1）：突发后第 N+1 次登录返回 429。
func TestAuthRateLimit(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if sqlDB, e := db.DB(); e == nil {
		sqlDB.SetMaxOpenConns(1)
	}
	if err := db.Exec(testSchema).Error; err != nil {
		t.Fatal(err)
	}
	// 突发 3：第 4 次同 IP 登录应 429
	engine, srv := NewWithServer(db, Options{AuthSecret: testSecret, AuthRateBurst: 3})
	// This test asserts the burst boundary, not wall-clock refill speed. The
	// real login handler performs Argon2 even for an unknown user; under -race
	// three sequential logins can take over one second and refill a token.
	// Disable refill only in this fixture, while exercising the real middleware
	// and password checks. Production retains its one-token-per-second rate.
	srv.authLimiter = newIPLimiter(0, 3)

	do := func() int {
		body, _ := json.Marshal(map[string]string{"email": "x@example.com", "password": "wrongpass1"})
		req := httptest.NewRequest("POST", "/api/auth/login", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = "203.0.113.7:12345" // 固定 IP
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, req)
		return w.Code
	}

	got401 := 0
	for i := 0; i < 3; i++ {
		if do() == http.StatusUnauthorized {
			got401++
		}
	}
	if got401 != 3 {
		t.Fatalf("first 3 attempts: %d got 401, want 3 (under burst)", got401)
	}
	// 第 4 次：限流
	if code := do(); code != http.StatusTooManyRequests {
		t.Fatalf("4th attempt = %d, want 429", code)
	}

	// 不同 IP 不受影响
	body, _ := json.Marshal(map[string]string{"email": "x@example.com", "password": "wrongpass1"})
	req := httptest.NewRequest("POST", "/api/auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "203.0.113.99:55555"
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)
	if w.Code == http.StatusTooManyRequests {
		t.Fatal("different IP should not be rate-limited")
	}
}

// 控制面体上限（审查 M2）：超大 body 被拒。
func TestControlPlaneBodyLimit(t *testing.T) {
	db, _ := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	_ = db.Exec(testSchema).Error
	engine := New(db, Options{AuthSecret: testSecret, AuthRateBurst: 100})

	huge := bytes.Repeat([]byte("a"), 2<<20) // 2MB > 1MB 上限
	req := httptest.NewRequest("POST", "/api/auth/login", bytes.NewReader(huge))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)
	// MaxBytesReader → ShouldBindJSON 失败 → 400（而非 OOM / 200）
	if w.Code != http.StatusBadRequest {
		t.Fatalf("oversized body = %d, want 400", w.Code)
	}
}
