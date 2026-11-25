package forward

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func setupForwardTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatal(err)
	}
	ddls := []string{
		`CREATE TABLE forward_rules (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			eid TEXT NOT NULL,
			name TEXT NOT NULL,
			rule_type TEXT NOT NULL DEFAULT 'openai',
			category TEXT NOT NULL DEFAULT '聊天',
			config_json TEXT NOT NULL DEFAULT '{}',
			description TEXT NOT NULL DEFAULT '',
			remark TEXT NOT NULL DEFAULT '',
			is_active INTEGER NOT NULL DEFAULT 1,
			is_system INTEGER NOT NULL DEFAULT 0,
			sort_order INTEGER NOT NULL DEFAULT 0,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE model_rule_bindings (
			model TEXT PRIMARY KEY,
			display_name TEXT NOT NULL DEFAULT '',
			model_alias TEXT NOT NULL DEFAULT '',
			billing_rule_id INTEGER NOT NULL DEFAULT 0,
			forward_rule_ids TEXT NOT NULL DEFAULT '[]',
			pre_deduction NUMERIC NOT NULL DEFAULT 0,
			enable_log_content INTEGER NOT NULL DEFAULT 0,
			site_discount NUMERIC NOT NULL DEFAULT 1,
			site_discount_enabled INTEGER NOT NULL DEFAULT 0,
			global_discount NUMERIC NOT NULL DEFAULT 1,
			global_discount_enabled INTEGER NOT NULL DEFAULT 0,
			discount_schedule TEXT NOT NULL DEFAULT '',
			is_listed INTEGER NOT NULL DEFAULT 1,
			is_active INTEGER NOT NULL DEFAULT 1,
			sort_order INTEGER NOT NULL DEFAULT 0,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
	}
	for _, s := range ddls {
		if err := db.Exec(s).Error; err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func TestPathMatchingAndScoringAndExpansion(t *testing.T) {
	if !PathMatches("/v1beta/models/gemini-2.5-pro:generateContent", "/v1beta/models/${model}:generateContent") {
		t.Fatal("expected ${model} placeholder path match")
	}
	if ExpandUpstreamPath("/v1beta/models/${model}:generateContent", "gemini-2.5-pro", "") != "/v1beta/models/gemini-2.5-pro:generateContent" {
		t.Fatal("expected model template expansion")
	}
	if ExpandUpstreamPath("/api/v3/contents/generations/tasks/${task_id}", "", "task_123") != "/api/v3/contents/generations/tasks/task_123" {
		t.Fatal("expected task_id template expansion")
	}

	rConvert := ForwardRule{
		ID:       1,
		EID:      "10001",
		Name:     "Volc Seedance Native",
		Category: "视频",
		IsActive: 1,
		ConfigJSON: `{
			"target_type": "volcengine",
			"auth_type": "bearer",
			"path_rewrite": {"old": "/v1/videos", "new": "/api/v3/contents/generations/tasks"},
			"path_passthrough": ["/api/v3/contents/generations/tasks"],
			"poll_path": "/api/v3/contents/generations/tasks/${task_id}",
			"asset_convert": true
		}`,
	}
	rPassOpenAI := ForwardRule{
		ID:       2,
		EID:      "10002",
		Name:     "OpenAI Video Passthrough",
		Category: "视频",
		IsActive: 1,
		ConfigJSON: `{
			"target_type": "openai",
			"auth_type": "bearer",
			"path_rewrite": {"old": "/v1/videos", "new": "/v1/videos"}
		}`,
	}

	// Native request hits path_passthrough (score 3)
	best, score := SelectBestForwardRule([]ForwardRule{rPassOpenAI, rConvert}, "视频", "/api/v3/contents/generations/tasks")
	if best == nil || best.ID != rConvert.ID || score != 3 {
		t.Fatalf("expected volc rule with score 3, got id=%v score=%d", best, score)
	}

	// OpenAI request prefers pure passthrough (score 2 > score 1)
	bestOpenAI, scoreOpenAI := SelectBestForwardRule([]ForwardRule{rConvert, rPassOpenAI}, "视频", "/v1/videos")
	if bestOpenAI == nil || bestOpenAI.ID != rPassOpenAI.ID || scoreOpenAI != 2 {
		t.Fatalf("expected openai passthrough score 2, got %+v score=%d", bestOpenAI, scoreOpenAI)
	}
}

func TestResolverAndCacheInvalidation(t *testing.T) {
	db := setupForwardTestDB(t)
	rule := ForwardRule{
		EID:      "10010",
		Name:     "Anthropic Messages",
		Category: "聊天",
		IsActive: 1,
		ConfigJSON: `{
			"target_type": "anthropic",
			"auth_type": "x-api-key",
			"path_passthrough": ["/v1/messages"]
		}`,
	}
	if err := db.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(
		`INSERT INTO model_rule_bindings (model, display_name, billing_rule_id, forward_rule_ids, is_listed, is_active) VALUES (?, ?, 0, ?, 1, 1)`,
		"claude-sonnet-4-5", "Claude Sonnet 4.5", fmt.Sprintf("[%d]", rule.ID),
	).Error; err != nil {
		t.Fatal(err)
	}

	res := NewResolver(time.Minute)
	ctx := context.Background()

	got, err := res.ResolveForModel(ctx, db, "claude-sonnet-4-5", "聊天", "/v1/messages")
	if err != nil {
		t.Fatalf("ResolveForModel error: %v", err)
	}
	if got == nil || got.TargetType != "anthropic" || !got.IsPassthrough || got.AuthType != "x-api-key" || got.EID != "10010" {
		t.Fatalf("unexpected resolved forward: %+v", got)
	}

	// Update upstream auth_type in DB -> cached until Invalidate()
	if err := db.Model(&ForwardRule{}).Where("id = ?", rule.ID).Update("config_json", `{
		"target_type": "anthropic",
		"auth_type": "bearer",
		"path_passthrough": ["/v1/messages"]
	}`).Error; err != nil {
		t.Fatal(err)
	}

	cachedGot, _ := res.ResolveForModel(ctx, db, "claude-sonnet-4-5", "聊天", "/v1/messages")
	if cachedGot.AuthType != "x-api-key" {
		t.Fatalf("expected cached auth_type x-api-key, got %s", cachedGot.AuthType)
	}

	res.Invalidate()
	freshGot, _ := res.ResolveForModel(ctx, db, "claude-sonnet-4-5", "聊天", "/v1/messages")
	if freshGot.AuthType != "bearer" {
		t.Fatalf("expected refreshed auth_type bearer after Invalidate, got %s", freshGot.AuthType)
	}
}

func TestJoinUpstreamURLAndConfigSecurityGuards(t *testing.T) {
	u, err := JoinUpstreamURL("https://ark.cn-beijing.volces.com/api/v3", "/api/v3/contents/generations/tasks?foo=1")
	if err != nil || u.String() != "https://ark.cn-beijing.volces.com/api/v3/contents/generations/tasks?foo=1" {
		t.Fatalf("unexpected deduped URL: %v err=%v", u, err)
	}

	for _, bad := range []string{
		"https://evil.example.com/steal",
		"//evil.example.com/steal",
		"/v1/../../etc/passwd",
	} {
		if _, err := JoinUpstreamURL("https://api.openai.com/v1", bad); err == nil {
			t.Fatalf("expected JoinUpstreamURL to reject %q", bad)
		}
	}

	if err := ValidateConfigJSON(`{"auth_type":"evil_header"}`); err == nil {
		t.Fatal("expected unsupported auth_type to fail")
	}
	if err := ValidateConfigJSON(`{"poll_path":"https://evil.com/poll"}`); err == nil {
		t.Fatal("expected full URL poll_path to fail")
	}
}

func TestResolverConcurrentStampedeAndZeroValue(t *testing.T) {
	db := setupForwardTestDB(t)
	if sqlDB, err := db.DB(); err == nil {
		sqlDB.SetMaxOpenConns(1)
	}
	rule := ForwardRule{
		EID:      "10999",
		Name:     "Concurrent Rule",
		Category: "视频",
		IsActive: 1,
		ConfigJSON: `{
			"target_type": "volcengine",
			"auth_type": "bearer",
			"path_passthrough": ["/api/v3/contents/generations/tasks"],
			"poll_path": "/api/v3/contents/generations/tasks/${task_id}"
		}`,
	}
	if err := db.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(
		`INSERT INTO model_rule_bindings (model, display_name, billing_rule_id, forward_rule_ids, is_listed, is_active) VALUES (?, ?, 0, ?, 1, 1)`,
		"seedance-conc", "Seedance Conc", fmt.Sprintf("[%d]", rule.ID),
	).Error; err != nil {
		t.Fatal(err)
	}

	var zeroRes Resolver // zero-value safety test
	var wg sync.WaitGroup
	for range 24 {
		wg.Go(func() {
			got, err := zeroRes.ResolveForModel(t.Context(), db, "seedance-conc", "视频", "/api/v3/contents/generations/tasks")
			if err != nil || got == nil || got.EID != "10999" {
				t.Errorf("concurrent ResolveForModel failed: %+v err=%v", got, err)
			}
			byEID, err := zeroRes.ResolveByEID(t.Context(), db, "10999", "/v1/videos", "seedance-conc")
			if err != nil || byEID == nil || byEID.PollPath == "" {
				t.Errorf("concurrent ResolveByEID failed: %+v err=%v", byEID, err)
			}
		})
	}
	wg.Wait()
}
