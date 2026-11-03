package watermark

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const schema = `
CREATE TABLE video_watermark_jobs (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 org_id INTEGER NOT NULL,
 user_id INTEGER,
 task_id TEXT NOT NULL UNIQUE,
 state TEXT NOT NULL DEFAULT 'pending',
 attempts INTEGER NOT NULL DEFAULT 0,
 lease_until DATETIME,
 processed_media_id TEXT,
 processed_expires_at DATETIME,
 last_error TEXT,
 created_at DATETIME NOT NULL,
 updated_at DATETIME NOT NULL
);
CREATE TABLE video_watermark_settings (
 id INTEGER PRIMARY KEY,
 enabled INTEGER NOT NULL DEFAULT 0,
 updated_by INTEGER NOT NULL DEFAULT 0,
 updated_at DATETIME NOT NULL
);
INSERT INTO video_watermark_settings (id, enabled, updated_by, updated_at) VALUES (1, 0, 0, '2026-01-01');
CREATE TABLE video_watermark_users (user_id INTEGER PRIMARY KEY, granted_by INTEGER NOT NULL, granted_at DATETIME NOT NULL);
`

var fixtureUserID uint64 = 7

func fixture(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Exec(schema).Error; err != nil {
		t.Fatal(err)
	}
	return db
}

func setEnabled(db *gorm.DB, on bool) {
	t := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	db.Model(&Settings{}).Where("id = 1").Updates(map[string]any{"enabled": on, "updated_at": t})
	if on {
		db.Create(&UserGrant{UserID: fixtureUserID, GrantedBy: 1, GrantedAt: t})
	}
}

func TestRegisterTxRespectsToggleAndReplays(t *testing.T) {
	db := fixture(t)
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := db.Transaction(func(tx *gorm.DB) error { return RegisterTx(tx, 45, &fixtureUserID, "task_a", now) }); err != nil {
		t.Fatal(err)
	}
	var count int64
	db.Model(&Job{}).Count(&count)
	if count != 0 {
		t.Fatal("registered while toggle off")
	}
	setEnabled(db, true)
	for i := 0; i < 2; i++ {
		if err := db.Transaction(func(tx *gorm.DB) error { return RegisterTx(tx, 45, &fixtureUserID, "task_a", now) }); err != nil {
			t.Fatal(err)
		}
	}
	db.Model(&Job{}).Count(&count)
	if count != 1 {
		t.Fatalf("replay duplicated job: %d", count)
	}
}

func TestLeaseClaimsOnceAndReclaimsExpired(t *testing.T) {
	db := fixture(t)
	setEnabled(db, true)
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := db.Transaction(func(tx *gorm.DB) error { return RegisterTx(tx, 45, &fixtureUserID, "task_a", now) }); err != nil {
		t.Fatal(err)
	}
	jobs, err := Lease(db, 5, time.Minute, now)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("first lease: %v %v", jobs, err)
	}
	if jobs[0].State != StateProcessing || jobs[0].Attempts != 1 {
		t.Fatalf("lease state wrong: %+v", jobs[0])
	}
	// Still leased: a second claim inside the window must not hand it out.
	again, err := Lease(db, 5, time.Minute, now.Add(time.Second))
	if err != nil || len(again) != 0 {
		t.Fatalf("leased job re-claimed: %v %v", again, err)
	}
	// After expiry the same worker pool picks it up with an incremented attempt.
	expired, err := Lease(db, 5, time.Minute, now.Add(2*time.Minute))
	if err != nil || len(expired) != 1 || expired[0].Attempts != 2 {
		t.Fatalf("expired lease not reclaimed: %v %v", expired, err)
	}
}

func TestFailRequeuesThenTerminates(t *testing.T) {
	db := fixture(t)
	setEnabled(db, true)
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := db.Transaction(func(tx *gorm.DB) error { return RegisterTx(tx, 45, &fixtureUserID, "task_a", now) }); err != nil {
		t.Fatal(err)
	}
	jobs, _ := Lease(db, 5, time.Minute, now)
	if err := Fail(db, jobs[0], "boom", now.Add(time.Minute), time.Minute); err != nil {
		t.Fatal(err)
	}
	var row Job
	db.Take(&row, jobs[0].ID)
	if row.State != StatePending || row.Attempts != 1 || row.LeaseUntil == nil {
		t.Fatalf("expected requeue with backoff lease: %+v", row)
	}
	if early, err := Lease(db, 5, time.Minute, row.LeaseUntil.Add(-time.Nanosecond)); err != nil || len(early) != 0 {
		t.Fatalf("requeue ignored backoff: %v %v", early, err)
	}
	// Burn the remaining budget: attempts reach MaxAttempts -> failed.
	for row.Attempts < MaxAttempts {
		due := *row.LeaseUntil
		next, err := Lease(db, 5, time.Minute, due)
		if err != nil || len(next) != 1 {
			t.Fatalf("requeue lease: %v %v", next, err)
		}
		if err := Fail(db, next[0], "boom", due.Add(time.Minute), time.Minute); err != nil {
			t.Fatal(err)
		}
		db.Take(&row, jobs[0].ID)
	}
	if row.State != StateFailed {
		t.Fatalf("expected terminal failure, got %s", row.State)
	}
}

func TestStaleAttemptCannotOverwriteNewLease(t *testing.T) {
	db := fixture(t)
	setEnabled(db, true)
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := RegisterTx(db, 45, &fixtureUserID, "task_a", now); err != nil {
		t.Fatal(err)
	}
	first, err := Lease(db, 1, time.Minute, now)
	if err != nil || len(first) != 1 {
		t.Fatal(first, err)
	}
	second, err := Lease(db, 1, time.Minute, now.Add(2*time.Minute))
	if err != nil || len(second) != 1 {
		t.Fatal(second, err)
	}
	if err := Complete(db, first[0], "media_stale", now.Add(time.Hour), now); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale completion: %v", err)
	}
	if err := Fail(db, first[0], "late failure", now, time.Minute); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale failure: %v", err)
	}
	if err := Complete(db, second[0], "media_current", now.Add(time.Hour), now); err != nil {
		t.Fatal(err)
	}
	ready, err := Ready(db, 45, "task_a", now)
	if err != nil || ready == nil || ready.ProcessedMediaID != "media_current" {
		t.Fatalf("new lease lost: %+v %v", ready, err)
	}
}

func TestRegistrationPropagatesStorageFailure(t *testing.T) {
	db := fixture(t)
	setEnabled(db, true)
	if err := db.Exec("DROP TABLE video_watermark_jobs").Error; err != nil {
		t.Fatal(err)
	}
	if err := RegisterTx(db, 45, &fixtureUserID, "task_a", time.Now()); err == nil {
		t.Fatal("job write failure was swallowed")
	}
}

func TestRetainedMediaRespectsExpiry(t *testing.T) {
	db := fixture(t)
	now := time.Now().UTC()
	row := Job{OrgID: 45, TaskID: "task_a", State: StateReady, ProcessedMediaID: "media_wm_x", ProcessedExpiresAt: ptrTime(now.Add(time.Hour)), CreatedAt: now, UpdatedAt: now}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	if kept, err := RetainedMedia(db, row.ProcessedMediaID, now); err != nil || !kept {
		t.Fatalf("live object not retained: %v %v", kept, err)
	}
	if kept, err := RetainedMedia(db, row.ProcessedMediaID, now.Add(2*time.Hour)); err != nil || kept {
		t.Fatalf("expired object retained: %v %v", kept, err)
	}
}

func TestReadyHonoursStateAndExpiry(t *testing.T) {
	db := fixture(t)
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	row := Job{OrgID: 45, TaskID: "task_a", State: StateReady, ProcessedMediaID: "media_wm_x", ProcessedExpiresAt: ptrTime(now.Add(time.Hour)), CreatedAt: now, UpdatedAt: now}
	db.Create(&row)
	job, err := Ready(db, 45, "task_a", now)
	if err != nil || job == nil || job.ProcessedMediaID != "media_wm_x" {
		t.Fatalf("ready lookup: %v %v", job, err)
	}
	if got, _ := Ready(db, 45, "task_b", now); got != nil {
		t.Fatal("unknown task returned")
	}
	db.Model(&Job{}).Where("id = ?", row.ID).Update("processed_expires_at", now.Add(-time.Minute))
	if got, _ := Ready(db, 45, "task_a", now); got != nil {
		t.Fatal("expired artifact served")
	}
}

func TestDeliveryURLSwapsOnlyWhenReady(t *testing.T) {
	db := fixture(t)
	now := time.Now().UTC()
	c := &Coordinator{DB: db, Media: fakeMedia{}}
	if _, ok := c.DeliveryURL(45, &fixtureUserID, "task_a", now); ok {
		t.Fatal("delivered without ready row")
	}
	setEnabled(db, true)
	row := Job{OrgID: 45, UserID: &fixtureUserID, TaskID: "task_a", State: StateReady, ProcessedMediaID: "media_wm_x", ProcessedExpiresAt: ptrTime(now.Add(time.Hour)), CreatedAt: now, UpdatedAt: now}
	db.Create(&row)
	url, ok := c.DeliveryURL(45, &fixtureUserID, "task_a", now)
	if !ok || !strings.HasPrefix(url, "https://media.example/") {
		t.Fatalf("delivery url: %q %v", url, ok)
	}
	if err := db.Delete(&UserGrant{}, "user_id = ?", fixtureUserID).Error; err != nil {
		t.Fatal(err)
	}
	if _, ok := c.DeliveryURL(45, &fixtureUserID, "task_a", now); ok {
		t.Fatal("revoked user still received processed URL")
	}
}

func TestRegistrationRequiresSelectedUser(t *testing.T) {
	db := fixture(t)
	now := time.Now().UTC()
	if err := db.Model(&Settings{}).Where("id = 1").Update("enabled", true).Error; err != nil {
		t.Fatal(err)
	}
	for _, user := range []*uint64{nil, &fixtureUserID} {
		if err := RegisterTx(db, 45, user, "task_a", now); err != nil {
			t.Fatal(err)
		}
	}
	var count int64
	db.Model(&Job{}).Count(&count)
	if count != 0 {
		t.Fatal("unselected user entered queue")
	}
	if err := db.Create(&UserGrant{UserID: fixtureUserID, GrantedBy: 1, GrantedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	if err := RegisterTx(db, 45, &fixtureUserID, "task_a", now); err != nil {
		t.Fatal(err)
	}
	db.Model(&Job{}).Count(&count)
	if count != 1 {
		t.Fatal("selected user was not registered")
	}
	if err := db.Delete(&UserGrant{}, "user_id = ?", fixtureUserID).Error; err != nil {
		t.Fatal(err)
	}
	if jobs, err := Lease(db, 1, time.Minute, now); err != nil || len(jobs) != 0 {
		t.Fatalf("revoked user was leased: %v %v", jobs, err)
	}
	if err := db.Create(&UserGrant{UserID: fixtureUserID, GrantedBy: 1, GrantedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	if jobs, err := Lease(db, 1, time.Minute, now); err != nil || len(jobs) != 1 {
		t.Fatalf("restored user was not leased: %v %v", jobs, err)
	}
}

type fakeMedia struct{}

func (fakeMedia) StoreVideo(ctx context.Context, id string, r io.Reader) (int64, error) {
	return 1, nil
}
func (fakeMedia) SignedURL(org uint64, id string, expires time.Time) string {
	return "https://media.example/" + id
}

func ptrTime(t time.Time) *time.Time { return &t }

func TestFilterForFollowsDocumentedScript(t *testing.T) {
	got := FilterFor(1280, 720)
	want := "scale=1347:758:flags=lanczos,rotate=0.026179939:ow=iw:oh=ih:c=black,crop=1280:720:(iw-ow)/2+5:(ih-oh)/2-2,setsar=1/1"
	if got != want {
		t.Fatalf("filter mismatch:\n got %s\nwant %s", got, want)
	}
	other := FilterFor(640, 640)
	if other != "scale=674:674:flags=lanczos,rotate=0.026179939:ow=iw:oh=ih:c=black,crop=640:640:(iw-ow)/2+2:(ih-oh)/2-1,setsar=1/1" {
		t.Fatalf("proportional filter wrong: %s", other)
	}
}

// Process is exercised end-to-end on Unix with fake ffmpeg/ffprobe executables,
// proving argument plumbing without a real encoder.
func TestProcessWithFakeBinaries(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake binaries are unix-only")
	}
	dir := t.TempDir()
	probe := filepath.Join(dir, "ffprobe-fake")
	ffmpeg := filepath.Join(dir, "ffmpeg-fake")
	write := func(path, body string) {
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write(probe, "#!/bin/sh\necho '{\"streams\":[{\"codec_type\":\"video\",\"width\":1280,\"height\":720}]}'\n")
	write(ffmpeg, "#!/bin/sh\nout=\"\"\nfor a in \"$@\"; do out=\"$a\"; done\nprintf 'video' > \"$out\"\n")
	p := Processor{FFmpeg: ffmpeg, FFProbe: probe, Timeout: 10 * time.Second}
	in := filepath.Join(dir, "in.mp4")
	out := filepath.Join(dir, "out.mp4")
	if err := os.WriteFile(in, []byte("source"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := p.Process(context.Background(), "", in, out); err != nil {
		t.Fatalf("process: %v", err)
	}
	got, err := os.ReadFile(out)
	if err != nil || string(got) != "video" {
		t.Fatalf("output: %v %q", err, got)
	}
}

// Exercise the real media path when ffmpeg is available locally: source probe,
// derived filter, H.264 output, copied audio, and complete decode.
func TestProcessWithRealFFmpeg(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg unavailable")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe unavailable")
	}
	dir := t.TempDir()
	in := filepath.Join(dir, "source.mp4")
	out := filepath.Join(dir, "processed.mp4")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	generate := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "testsrc2=size=1280x720:rate=24", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000", "-t", "1", "-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest", in)
	if output, err := generate.CombinedOutput(); err != nil {
		t.Fatalf("generate fixture: %v: %s", err, output)
	}
	p := Processor{FFmpeg: ffmpeg, FFProbe: ffprobe, Timeout: 20 * time.Second}
	if err := p.Process(ctx, "", in, out); err != nil {
		t.Fatal(err)
	}
	w, h, err := p.Resolution(ctx, out)
	if err != nil || w != 1280 || h != 720 {
		t.Fatalf("output dimensions: %dx%d %v", w, h, err)
	}
	audio, err := exec.CommandContext(ctx, ffprobe, "-v", "error", "-select_streams", "a:0", "-show_entries", "stream=codec_name", "-of", "default=nw=1", out).Output()
	if err != nil || !strings.Contains(string(audio), "aac") {
		t.Fatalf("audio stream lost: %q %v", audio, err)
	}
	decode := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-loglevel", "error", "-i", out, "-f", "null", os.DevNull)
	if output, err := decode.CombinedOutput(); err != nil {
		t.Fatalf("output decode: %v: %s", err, output)
	}
	p.Filter = "this_filter_does_not_exist"
	if err := p.Process(ctx, "", in, filepath.Join(dir, "override.mp4")); err == nil || !strings.Contains(err.Error(), "this_filter_does_not_exist") {
		t.Fatalf("configured filter override was ignored: %v", err)
	}
}
