package server

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/migrate"
)

func TestInvocationExportJobBatchesAndScopes(t *testing.T) {
	h, _ := invocationAcceptanceSmall(t)
	if err := h.db.AutoMigrate(&invocationExportJob{}, &invocationExportChunk{}); err != nil {
		t.Fatal(err)
	}
	if err := h.db.Exec("CREATE TABLE invocation_export_admission (id INTEGER PRIMARY KEY)").Error; err != nil {
		t.Fatal(err)
	}
	if err := h.db.Exec("INSERT INTO invocation_export_admission VALUES (1)").Error; err != nil {
		t.Fatal(err)
	}
	checkInvocationExportJobBatchesAndScopes(t, h)
}

func TestMySQLInvocationExportJobBatchesAndScopes(t *testing.T) {
	h, _ := invocationAcceptanceMySQL(t)
	sql, err := os.ReadFile(filepath.Join("..", "..", "..", "migrations", "0158_invocation_export_jobs.sql"))
	if err != nil {
		t.Fatal(err)
	}
	// MySQL can commit the admission row before the migration is registered.
	// Verify the real migrator can finish this interrupted schema expansion.
	parts := strings.SplitN(string(sql), ";", 3)
	if err := h.db.Exec(strings.Join(parts[:2], ";")).Error; err != nil {
		t.Fatal(err)
	}
	files := fstest.MapFS{"0158_invocation_export_jobs.sql": {Data: sql}}
	if applied, err := migrate.Up(h.db, files); err != nil || len(applied) != 1 {
		t.Fatalf("resume migration: %v %v", applied, err)
	}
	if applied, err := migrate.Up(h.db, files); err != nil || len(applied) != 0 {
		t.Fatalf("replay migration: %v %v", applied, err)
	}
	checkInvocationExportJobBatchesAndScopes(t, h)
	h.server.srv.expireInvocationExportJobs(context.Background(), invocationAcceptanceNow.Add(25*time.Hour))
	var n int64
	if err := h.db.Model(&invocationExportChunk{}).Count(&n).Error; err != nil || n != 0 {
		t.Fatalf("expired export left its chunks: %d, %v", n, err)
	}
}

func checkInvocationExportJobBatchesAndScopes(t *testing.T, h *invocationAcceptanceHarness) {
	t.Helper()
	const count = 16002
	const keyID = 9007199254740993
	model := strings.Repeat("模", 128)
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	makeRow := func(i int) invocation.Invocation {
		at := start
		if i >= count/2 {
			at = at.AddDate(0, 0, 1)
		}
		return invocation.Invocation{
			OrgID: 1001, APIKeyID: keyID, RequestID: fmt.Sprintf("async-%05d", i),
			Model: model, Status: "success", CreatedAt: at, PayloadObjectKey: "omit-from-csv",
		}
	}
	rows := make([]invocation.Invocation, 0, count)
	for i := 0; i < count; i++ {
		rows = append(rows, makeRow(i))
	}
	for i, exclude := range []func(*invocation.Invocation){
		func(r *invocation.Invocation) { r.OrgID = 1002 },
		func(r *invocation.Invocation) { r.APIKeyID = keyID + 1 },
		func(r *invocation.Invocation) { r.IsStream = true },
		func(r *invocation.Invocation) { r.Status = "upstream_error" },
		func(r *invocation.Invocation) { r.Modality = "chat" },
		func(r *invocation.Invocation) { r.CreatedAt = start.AddDate(0, 0, 2) },
	} {
		row := makeRow(count + i)
		exclude(&row)
		rows = append(rows, row)
	}
	if err := h.db.CreateInBatches(rows, 25).Error; err != nil {
		t.Fatal(err)
	}

	values := url.Values{
		"filter_version": {"1"}, "timezone": {"UTC"}, "since": {"2026-10-01"}, "until": {"2026-10-03"},
		"api_key_id": {"9007199254740993"}, "model": {model}, "result": {"success"}, "is_stream": {"false"},
		"modality": {""}, "display_currency": {"CNY"},
	}
	const endpoint = "/api/usage/export-jobs"
	create := func() string {
		t.Helper()
		r := invocationAcceptanceRequest(h, "A", endpoint+"?"+values.Encode())
		r.Method = http.MethodPost
		r.Header.Set("Idempotency-Key", "00000000-0000-4000-8000-000000000001")
		w := invocationAcceptanceCall(h, r)
		var data struct{ ID, Status string }
		if w.Code != 202 || json.Unmarshal(w.Body.Bytes(), &data) != nil || data.ID == "" || data.Status != "pending" {
			t.Fatalf("create job: %d %s", w.Code, w.Body.String())
		}
		return data.ID
	}
	id := create()
	if create() != id {
		t.Fatal("replayed submission created another job")
	}
	path := endpoint + "/" + id + "/download"
	call := func(subject, path string) int {
		return invocationAcceptanceCall(h, invocationAcceptanceRequest(h, subject, path)).Code
	}
	if call("A", path) != 409 || call("B", path) != 404 || call("admin", path) != 404 {
		t.Fatal("pending download or foreign ownership check failed")
	}
	late := makeRow(count + 10)
	if err := h.db.Create(&late).Error; err != nil {
		t.Fatal(err)
	}

	var job invocationExportJob
	for i := 0; i < 25; i++ {
		h.server.srv.processInvocationExportBatch(context.Background(), invocationAcceptanceNow)
		if err := h.db.Take(&job, "id = ?", id).Error; err != nil {
			t.Fatal(err)
		}
		if job.Status == "ready" {
			break
		}
	}
	if job.Status != "ready" || job.RowCount != count || job.ByteCount <= 8<<20 || job.ChunkCount < 2 {
		t.Fatalf("large job did not complete with full data: status=%s rows=%d bytes=%d chunks=%d", job.Status, job.RowCount, job.ByteCount, job.ChunkCount)
	}
	if call("B", path) != 404 || call("admin", path) != 404 {
		t.Fatal("completed export available outside its owning user and organization")
	}
	metadata := invocationAcceptanceCall(h, invocationAcceptanceRequest(h, "A", endpoint))
	var listed struct {
		Items []struct {
			ID, Status string
			Rows       int64 `json:"row_count"`
			Filters    struct {
				APIKeyID string `json:"api_key_id"`
			}
		}
	}
	if metadata.Code != 200 || json.Unmarshal(metadata.Body.Bytes(), &listed) != nil || len(listed.Items) != 1 ||
		listed.Items[0].ID != id || listed.Items[0].Status != "ready" || listed.Items[0].Rows != count || listed.Items[0].Filters.APIKeyID != "9007199254740993" {
		t.Fatal("job history lost its frozen filters, progress, or final status")
	}
	w := invocationAcceptanceCall(h, invocationAcceptanceRequest(h, "A", path))
	if w.Code != 200 || w.Header().Get("X-Invocation-Export-Bytes") != strconv.Itoa(w.Body.Len()) ||
		strings.Contains(w.Body.String(), "omit-from-csv") {
		t.Fatal("download missing bytes or exposed non-export fields")
	}
	r := csv.NewReader(w.Body)
	if header, err := r.Read(); err != nil || strings.Join(header, ",") != strings.Join(invocationExportTestColumns, ",") {
		t.Fatal("download CSV header did not match the existing export")
	}
	for i := count - 1; i >= 0; i-- {
		record, err := r.Read()
		if err != nil || record[1] != fmt.Sprintf("async-%05d", i) || record[5] != "9007199254740993" || record[12] != "CNY" {
			t.Fatalf("unexpected row at %d: %v", i, err)
		}
	}
	if extra, _ := r.Read(); extra != nil {
		t.Fatal("extra record in CSV")
	}
	for _, subject := range []string{"B", "admin"} {
		list := invocationAcceptanceCall(h, invocationAcceptanceRequest(h, subject, endpoint))
		if list.Code != 200 || list.Body.String() != `{"items":[]}` {
			t.Fatalf("job list leaks across owners: %d %s", list.Code, list.Body.String())
		}
	}
}
