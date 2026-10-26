package task

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/laiai/laiai-next/core/internal/crypto"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestTaskHeaderIsolationDefaultClient(t *testing.T) {
	var targetCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { targetCalls.Add(1); w.WriteHeader(200) }))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Custom-Key") != "synthetic" {
			t.Error("first hop missing credential")
		}
		http.Redirect(w, r, target.URL, 307)
	}))
	defer origin.Close()
	s := NewService(nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer s.Client.CloseIdleConnections()
	q, _ := http.NewRequest(http.MethodPost, origin.URL, strings.NewReader("private-body"))
	q.Header.Set("X-Custom-Key", "synthetic")
	resp, err := s.Client.Do(q)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if targetCalls.Load() != 0 || resp.StatusCode != 307 || s.Client.Jar != nil {
		t.Fatal("task factory sent a cross-origin request")
	}
}

func TestTaskHeaderIsolationMediaHandlers(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	defer sqlDB.Close()
	for _, sql := range []string{
		`CREATE TABLE organizations (id INTEGER PRIMARY KEY, status INTEGER)`,
		`CREATE TABLE api_keys (id INTEGER PRIMARY KEY, org_id INTEGER, key_hash TEXT, status INTEGER, model_access_mode TEXT, policy_version INTEGER)`,
		`CREATE TABLE tasks (id INTEGER PRIMARY KEY, task_id TEXT, org_id INTEGER, provider TEXT, status TEXT, channel_id INTEGER, credential_id INTEGER, result_ref TEXT, result_url TEXT)`,
		`CREATE TABLE channels (id INTEGER PRIMARY KEY, provider TEXT, base_url TEXT, credential_enc TEXT)`,
		`CREATE TABLE ext_settings (name TEXT PRIMARY KEY, value TEXT)`,
		`INSERT INTO organizations VALUES (1,1)`,
		`INSERT INTO ext_settings VALUES ('advanced_custom_allow_private','true')`,
	} {
		if err := db.Exec(sql).Error; err != nil {
			t.Fatal(err)
		}
	}
	key := "sk-laiai-synthetic-media-test"
	sum := sha256.Sum256([]byte(key))
	if err := db.Exec(`INSERT INTO api_keys VALUES (1,1,?,1,'legacy',1)`, hex.EncodeToString(sum[:])).Error; err != nil {
		t.Fatal(err)
	}
	kr, err := crypto.NewKeyring("test:"+base64.StdEncoding.EncodeToString(make([]byte, 32)), "test")
	if err != nil {
		t.Fatal(err)
	}
	enc, err := kr.Encrypt("synthetic-provider-key")
	if err != nil {
		t.Fatal(err)
	}
	var cdnCalls, firstAuth atomic.Int32
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cdnCalls.Add(1)
		for _, name := range []string{"Authorization", "X-Goog-Api-Key", "Mj-Api-Secret", "Cookie", "Referer"} {
			if r.Header.Get(name) != "" {
				t.Errorf("media leaked %s", name)
			}
		}
		_, _ = w.Write([]byte("media-bytes"))
	}))
	defer cdn.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "sora") {
			if r.Header.Get("Authorization") != "Bearer synthetic-provider-key" {
				t.Error("Sora first-hop credential lost")
			}
			firstAuth.Add(1)
		} else if r.URL.Path == "/veo" {
			if r.Header.Get("X-Goog-Api-Key") != "synthetic-provider-key" {
				t.Error("Veo first-hop credential lost")
			}
			firstAuth.Add(1)
		} else if r.Header.Get("Authorization") != "" {
			t.Error("user key forwarded to media")
		}
		http.Redirect(w, r, cdn.URL+"/file?signature=synthetic", 302)
	}))
	defer origin.Close()
	if err := db.Exec(`INSERT INTO channels VALUES (1,'sora',?,?)`, origin.URL, enc).Error; err != nil {
		t.Fatal(err)
	}
	s := NewService(db, kr, slog.New(slog.NewTextHandler(io.Discard, nil)))
	for i, tc := range []struct {
		name, provider, ref, url string
		mj                       bool
	}{
		{"direct", "sora", "", origin.URL + "/direct", false},
		{"sora", "sora", "sora-id", "", false},
		{"veo", "veo", origin.URL + "/veo", "", false},
		{"mj", "midjourney", "", origin.URL + "/mj", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := db.Exec(`INSERT INTO tasks VALUES (?,?,1,?,'succeeded',1,0,?,?)`, i+1, tc.name, tc.provider, tc.ref, tc.url).Error; err != nil {
				t.Fatal(err)
			}
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Params = gin.Params{{Key: "id", Value: tc.name}}
			c.Request = httptest.NewRequest(http.MethodGet, "/content", nil)
			c.Request.Header.Set("Authorization", "Bearer "+key)
			c.Request.Header.Set("Cookie", "private-user-cookie")
			if tc.mj {
				c.Request.Header.Set("mj-api-secret", key)
				s.HandleMJImage(c)
			} else {
				s.HandleContent(c)
			}
			if w.Code != 200 || w.Body.String() != "media-bytes" {
				t.Fatalf("real media handler failed: %d %s", w.Code, w.Body.String())
			}
		})
	}
	if cdnCalls.Load() != 4 || firstAuth.Load() != 2 {
		t.Fatalf("expected real handler paths: downloads=%d auth=%d", cdnCalls.Load(), firstAuth.Load())
	}
}
