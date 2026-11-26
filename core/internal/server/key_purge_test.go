package server

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/laiai/laiai-next/core/internal/identity"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type keyPurgeHTTPFixture struct {
	db      *gorm.DB
	handler http.Handler
	token   string
}

func keyPurgeSQL(t *testing.T, db *gorm.DB, query string, args ...any) {
	t.Helper()
	if err := db.Exec(query, args...).Error; err != nil {
		t.Fatalf("sql %q: %v", query, err)
	}
}

func keyPurgeFingerprints(t *testing.T, db *gorm.DB, tables ...string) map[string]string {
	t.Helper()
	out := make(map[string]string, len(tables))
	for _, table := range tables {
		var rows []map[string]any
		if err := db.Table(table).Order("1").Find(&rows).Error; err != nil {
			t.Fatalf("fingerprint %s: %v", table, err)
		}
		b, _ := json.Marshal(rows)
		out[table] = string(b)
	}
	return out
}

func keyPurgeHTTP(t *testing.T, db *gorm.DB) *keyPurgeHTTPFixture {
	t.Helper()
	for _, org := range []int{1, 2} {
		keyPurgeSQL(t, db, "INSERT INTO organizations(id,slug,name) VALUES(?,?,?)", org, fmt.Sprintf("key-purge-%d", org), "Fixture")
	}
	keyPurgeSQL(t, db, "INSERT INTO users(id,email,email_canonical) VALUES(1,'purge@example.test','purge@example.test')")
	keyPurgeSQL(t, db, "INSERT INTO org_members(org_id,user_id,role) VALUES(1,1,'owner')")
	token, _, err := identity.IssueSessionToken(testSecret, 1, 1, time.Hour, time.Now())
	if err != nil {
		t.Fatal("cannot create synthetic session")
	}
	engine, _ := NewWithServer(db, Options{AuthSecret: testSecret, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	return &keyPurgeHTTPFixture{db: db, handler: engine, token: token}
}

func keyPurgeSQLite(t *testing.T) *keyPurgeHTTPFixture {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "purge.sqlite")), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	pool.SetMaxOpenConns(4)
	t.Cleanup(func() { _ = pool.Close() })
	keyPurgeSQL(t, db, testSchema)
	keyPurgeSQL(t, db, `CREATE TABLE IF NOT EXISTS tasks(
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		org_id INTEGER NOT NULL,
		api_key_id INTEGER NOT NULL DEFAULT 0,
		task_id TEXT NOT NULL DEFAULT '',
		model TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL DEFAULT 'queued'
	)`)
	return keyPurgeHTTP(t, db)
}

func (f *keyPurgeHTTPFixture) request(method string, id uint64, purge bool) *httptest.ResponseRecorder {
	path := fmt.Sprintf("/api/keys/%d", id)
	if purge {
		path += "/purge"
	}
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("Authorization", "Bearer "+f.token)
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, req)
	return w
}

func keyPurgeInsertKey(t *testing.T, db *gorm.DB, id, org uint64, active bool) {
	t.Helper()
	status := 0
	if active {
		status = 1
	}
	keyPurgeSQL(t, db, "INSERT INTO api_keys(id,org_id,name,prefix,key_hash,status) VALUES(?,?,'synthetic','test',?,?)", id, org, fmt.Sprintf("%064x", id), status)
}

func keyPurgeStatus(t *testing.T, w *httptest.ResponseRecorder, want int) {
	t.Helper()
	if w.Code != want {
		t.Fatalf("purge HTTP status=%d want=%d body=%s", w.Code, want, w.Body.String())
	}
	for _, secret := range []string{"SELECT ", "private_purge_column", "key_hash", "dsn="} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatal("unsafe purge error")
		}
	}
}

func TestKeyPurgeHTTPPreservesHistoricalReferencesAndRevocation(t *testing.T) {
	f := keyPurgeSQLite(t)
	keyPurgeInsertKey(t, f.db, 1, 1, true)
	keyPurgeInsertKey(t, f.db, 2, 1, false)
	keyPurgeInsertKey(t, f.db, 3, 2, false)
	keyPurgeSQL(t, f.db, "INSERT INTO tasks(id,org_id,api_key_id,task_id,model,status) VALUES(1,1,1,'t-active-1','seedance','queued')")
	keyPurgeStatus(t, f.request("DELETE", 1, true), 400)
	keyPurgeStatus(t, f.request("DELETE", 1, false), 200)
	before := keyPurgeFingerprints(t, f.db, "api_keys", "tasks")
	keyPurgeStatus(t, f.request("DELETE", 1, true), 409)
	keyPurgeStatus(t, f.request("DELETE", 3, true), 404)
	keyPurgeStatus(t, f.request("DELETE", 0, true), 404)
	keyPurgeStatus(t, f.request("DELETE", 999, true), 404)
	if after := keyPurgeFingerprints(t, f.db, "api_keys", "tasks"); !reflect.DeepEqual(before, after) {
		t.Fatal("denial changed historical references or revoked identity")
	}
	keyPurgeStatus(t, f.request("DELETE", 2, true), 200)
	keyPurgeStatus(t, f.request("DELETE", 2, true), 404)
	var count int64
	if err := f.db.Table("api_keys").Where("id=1 AND status=0").Count(&count).Error; err != nil || count != 1 {
		t.Fatal("referenced key was removed or reactivated")
	}
}

func TestKeyPurgeHTTPReferenceSQLFailureDoesNotDelete(t *testing.T) {
	f := keyPurgeSQLite(t)
	keyPurgeInsertKey(t, f.db, 1, 1, false)
	before := keyPurgeFingerprints(t, f.db, "api_keys")
	hits := 0
	const name = "key_purge_real_sql_failure"
	if err := f.db.Callback().Query().Before("gorm:query").Register(name, func(q *gorm.DB) {
		if q.Statement.Table == "tasks" {
			hits++
			q.Statement.Selects = []string{"private_purge_column"}
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.db.Callback().Query().Remove(name) })
	w := f.request("DELETE", 1, true)
	keyPurgeStatus(t, w, 503)
	if hits != 1 || !reflect.DeepEqual(before, keyPurgeFingerprints(t, f.db, "api_keys")) {
		t.Fatal("failed reference query deleted identity or missed SQL injection")
	}
}

func TestKeyPurgeHTTPRequiresCurrentSessionMembership(t *testing.T) {
	f := keyPurgeSQLite(t)
	keyPurgeInsertKey(t, f.db, 1, 1, false)
	for _, token := range []string{"", "sk-not-a-session"} {
		req := httptest.NewRequest(http.MethodDelete, "/api/keys/1/purge", nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		f.handler.ServeHTTP(w, req)
		keyPurgeStatus(t, w, 401)
	}
	keyPurgeSQL(t, f.db, "DELETE FROM org_members WHERE org_id=1 AND user_id=1")
	w := f.request("DELETE", 1, true)
	if w.Code < 400 {
		t.Fatal("removed member purged key")
	}
	var n int64
	if err := f.db.Table("api_keys").Count(&n).Error; err != nil || n != 1 {
		t.Fatal("denied session changed key")
	}
}
