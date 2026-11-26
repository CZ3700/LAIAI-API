package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/dbopen"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/migrate"
	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func openIsolatedPGTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	baseDSN := os.Getenv("LAIAI_PG_TEST_DSN")
	if baseDSN == "" {
		t.Skip("LAIAI_PG_TEST_DSN not set")
	}

	adminDB, err := dbopen.Open(baseDSN, &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Fatalf("open base pg: %v", err)
	}
	sqlAdmin, err := adminDB.DB()
	if err != nil {
		t.Fatalf("pg handle: %v", err)
	}

	schemaName := fmt.Sprintf("laiai_pg_test_%d", time.Now().UnixNano())
	if err := adminDB.Exec("CREATE SCHEMA " + schemaName).Error; err != nil {
		_ = sqlAdmin.Close()
		t.Fatalf("create schema %s: %v", schemaName, err)
	}
	t.Cleanup(func() {
		_ = adminDB.Exec("DROP SCHEMA IF EXISTS " + schemaName + " CASCADE").Error
		_ = sqlAdmin.Close()
	})

	sep := "?"
	if strings.Contains(baseDSN, "?") {
		sep = "&"
	}
	scopedDSN := baseDSN + sep + "search_path=" + schemaName
	db, err := dbopen.Open(scopedDSN, &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Fatalf("open scoped pg: %v", err)
	}
	sqlScoped, err := db.DB()
	if err != nil {
		t.Fatalf("scoped handle: %v", err)
	}
	t.Cleanup(func() { _ = sqlScoped.Close() })
	return db
}

func pgReq(t *testing.T, handler http.Handler, method, path, token string, body any) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode body: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w, out
}

func TestPGFullMigrationsRulesChannelAndLedgerFlow(t *testing.T) {
	db := openIsolatedPGTestDB(t)
	migrationsDir := filepath.Clean(filepath.Join("..", "..", "..", "migrations"))
	fsys := os.DirFS(migrationsDir)

	ran, err := migrate.Up(db, fsys)
	if err != nil {
		t.Fatalf("migrate.Up failed: %v", err)
	}
	if len(ran) != 3 {
		t.Fatalf("ran migrations = %v, want 3", ran)
	}
	ran2, err := migrate.Up(db, fsys)
	if err != nil || len(ran2) != 0 {
		t.Fatalf("second migrate.Up = (%v, %v), want empty", ran2, err)
	}

	kr := testKeyring(t)
	engine, apiSrv := NewWithServer(db, Options{
		AuthSecret:    testSecret,
		AdminToken:    []byte(testAdminToken),
		Keyring:       kr,
		AuthRateBurst: 100000,
		Mailer:        &captureMailer{},
	})
	if err := apiSrv.WarmCaches(t.Context()); err != nil {
		t.Fatalf("warm caches: %v", err)
	}

	// 1. Initialize superadmin on fresh PG schema.
	w, initOut := pgReq(t, engine, http.MethodPost, "/api/v1/auth/init-admin", "", map[string]any{
		"email":     "pg-admin@laiai.dev",
		"password":  "Admin@123456",
		"site_name": "LAIAI PG Test",
	})
	if w.Code != http.StatusOK && w.Code != http.StatusCreated {
		t.Fatalf("init-admin status=%d body=%s", w.Code, w.Body.String())
	}
	token, _ := initOut["token"].(string)
	if token == "" {
		t.Fatalf("missing token in init-admin: %+v", initOut)
	}

	// 2. Verify channel config CRUD on PostgreSQL channel_configs table.
	w, cfgOut := pgReq(t, engine, http.MethodPost, "/api/v1/channel-configs", token, map[string]any{
		"name":          "PG Volc Config",
		"provider_type": "openai",
		"base_url":      "https://api.example.com/v1",
		"api_key":       "sk-pg-upstream-1",
		"rate":          0.5,
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create channel-config status=%d body=%s", w.Code, w.Body.String())
	}
	cfgData, _ := cfgOut["data"].(map[string]any)
	cfgID := uint64(cfgData["id"].(float64))

	// 3. Create billing rule + model binding + channel.
	w, brOut := pgReq(t, engine, http.MethodPost, "/api/v1/billing-rules", token, map[string]any{
		"name":            "PG Test Billing",
		"billing_type":    "tokens",
		"billing_rule":    "standard",
		"pricing_type":    "standard",
		"prompt_rate":     2.0,
		"completion_rate": 6.0,
		"is_active":       1,
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create billing rule status=%d body=%s", w.Code, w.Body.String())
	}
	brData, _ := brOut["rule"].(map[string]any)
	brID := uint64(brData["id"].(float64))

	w, modOut := pgReq(t, engine, http.MethodPost, "/api/v1/models", token, map[string]any{
		"model_id":        "pg-test-model",
		"name":            "PG Test Model",
		"billing_rule_id": brID,
		"is_listed":       1,
		"is_active":       1,
	})
	if w.Code != http.StatusOK && w.Code != http.StatusCreated {
		t.Fatalf("create model status=%d body=%s", w.Code, w.Body.String())
	}
	modID := uint64(modOut["id"].(float64))

	w, chOut := pgReq(t, engine, http.MethodPost, "/api/v1/channels", token, map[string]any{
		"name":          "PG Test Channel",
		"provider_type": "high_availability_group",
		"models":        []string{fmt.Sprintf("m-%d", modID)},
		"status":        1,
		"config":        map[string]any{"sub_channels": []uint64{cfgID}},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create channel status=%d body=%s", w.Code, w.Body.String())
	}
	chData, _ := chOut["data"].(map[string]any)
	chID := uint64(chData["id"].(float64))

	// 4. Billing simulator via PostgreSQL numeric rules.
	w, simOut := pgReq(t, engine, http.MethodPost, fmt.Sprintf("/api/v1/channels/%d/simulate-billing", chID), token, map[string]any{
		"model": "pg-test-model",
		"usage": map[string]any{"prompt_tokens": 1000, "completion_tokens": 500},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("simulate-billing status=%d body=%s", w.Code, w.Body.String())
	}
	settlement, _ := simOut["settlement"].(map[string]any)
	if settlement == nil || settlement["actual_cost"] == nil {
		t.Fatalf("simulate-billing missing settlement.actual_cost: %+v", simOut)
	}

	// 5. Real PostgreSQL NUMERIC(20,8) ledger post, hold, capture, task row, and drift audit.
	err = db.Transaction(func(tx *gorm.DB) error {
		acc, err := ledger.EnsureAccount(tx, 1, ledger.AccountUserCredit)
		if err != nil {
			return err
		}
		if _, _, err := ledger.Post(tx, ledger.PostInput{
			IdempotencyKey: "pg-test-grant-1",
			AccountID:      acc.ID,
			Kind:           ledger.KindGrant,
			Delta:          decimal.RequireFromString("50.00000000"),
			RefType:        "test",
			RefID:          "pg-1",
			Reason:         "pg test credit",
		}); err != nil {
			return err
		}
		hold, _, err := ledger.CreateHold(tx, ledger.HoldInput{
			IdempotencyKey: "pg-test-hold-1",
			AccountID:      acc.ID,
			Amount:         decimal.RequireFromString("5.00000000"),
			RefType:        "task",
			RefID:          "pg-task-1",
		})
		if err != nil {
			return err
		}
		if err := tx.Create(&task.Task{
			TaskID:        "pg-task-1",
			OrgID:         1,
			APIKeyID:      0,
			Model:         "pg-test-model",
			ChannelID:     chID,
			Provider:      "openai",
			Status:        task.StatusQueued,
			HoldID:        hold.ID,
			HoldAmountUSD: decimal.RequireFromString("5.00000000"),
		}).Error; err != nil {
			return err
		}
		if _, err := ledger.Capture(tx, hold.ID, decimal.RequireFromString("1.25000000"), "pg capture"); err != nil {
			return err
		}
		return tx.Model(&task.Task{}).Where("task_id = ?", "pg-task-1").Updates(map[string]any{
			"status":   task.StatusSucceeded,
			"cost_usd": decimal.RequireFromString("1.25000000"),
		}).Error
	})
	if err != nil {
		t.Fatalf("pg ledger+task tx: %v", err)
	}

	posted, held, err := ledger.OrgBalances(db, 1, ledger.AccountUserCredit)
	if err != nil || !posted.Equal(decimal.RequireFromString("48.75")) || !held.IsZero() {
		t.Fatalf("pg balance = (%s, %s, %v), want (48.75, 0, nil)", posted, held, err)
	}
	drifts, err := ledger.CheckDrift(db)
	if err != nil || len(drifts) != 0 {
		t.Fatalf("pg drift = (%+v, %v), want empty", drifts, err)
	}
}
