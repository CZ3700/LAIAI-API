package videoartifact

// All media, provider, DNS and blob fixtures in this file are simulated.
import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const schema = `CREATE TABLE video_artifacts (
 org_id INTEGER NOT NULL, task_id TEXT NOT NULL, kind TEXT NOT NULL,
 status TEXT NOT NULL DEFAULT 'pending', object_key TEXT NOT NULL DEFAULT '',
 sha256 TEXT NOT NULL DEFAULT '', size INTEGER NOT NULL DEFAULT 0,
 media_type TEXT NOT NULL DEFAULT '', width INTEGER, height INTEGER,
 fence INTEGER NOT NULL DEFAULT 0, lease_token TEXT NOT NULL DEFAULT '',
 lease_until DATETIME, attempts INTEGER NOT NULL DEFAULT 0,
 error_code TEXT NOT NULL DEFAULT '', expires_at DATETIME NOT NULL,
 created_at DATETIME NOT NULL, updated_at DATETIME NOT NULL,
 deleted_at DATETIME, cleaned_at DATETIME,
 PRIMARY KEY(org_id,task_id,kind));`

var testNow = time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
var videoBytes = append([]byte{0, 0, 0, 24, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'}, bytes.Repeat([]byte{0}, 32)...)

func testRepo(t *testing.T) Repository {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err = db.Exec(schema).Error; err != nil {
		t.Fatal(err)
	}
	return Repository{DB: db}
}

func register(t *testing.T, repo Repository, org uint64, task string, kind Kind) {
	t.Helper()
	if _, err := repo.RegisterTx(repo.DB, Spec{OrgID: org, TaskID: task, Kind: kind, ExpiresAt: testNow.Add(time.Hour)}, testNow); err != nil {
		t.Fatal(err)
	}
}

func code(t *testing.T, err error, want string) {
	t.Helper()
	var safe *Error
	if !errors.As(err, &safe) || safe.Code != want {
		t.Fatalf("error=%v, want %s", err, want)
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type resolverFunc func(context.Context, uint64, string, Kind) (string, error)

func (f resolverFunc) ResolveSource(c context.Context, o uint64, t string, k Kind) (string, error) {
	return f(c, o, t, k)
}

func testDownloader(t *testing.T, data []byte, mediaType string) *Downloader {
	t.Helper()
	d, err := NewDownloader(DownloadConfig{AllowedHosts: []string{"cdn.example.com"}, MaxBytes: 1024, Timeout: time.Second, TempDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	d.client.Transport = roundTrip(func(req *http.Request) (*http.Response, error) {
		if req.Header.Get("Authorization") != "" || req.Header.Get("Cookie") != "" {
			t.Error("credentials forwarded")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{mediaType}}, Body: io.NopCloser(bytes.NewReader(data)), ContentLength: int64(len(data))}, nil
	})
	return d
}

type memoryBlobs struct {
	mu                   sync.Mutex
	objects              map[string][]byte
	puts, opens, deletes int
	failPut              bool
	beforePut            func()
	beforeOpen           func()
}

func (b *memoryBlobs) Put(ctx context.Context, key string, r io.Reader, meta Metadata) error {
	if b.beforePut != nil {
		b.beforePut()
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	hash := sha256.Sum256(data)
	if int64(len(data)) != meta.Size || hex.EncodeToString(hash[:]) != meta.SHA256 {
		return errors.New("invalid bytes")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.puts++
	if b.failPut {
		return errors.New("secret-storage-path")
	}
	if b.objects == nil {
		b.objects = make(map[string][]byte)
	}
	b.objects[key] = data
	return nil
}
func (b *memoryBlobs) Open(_ context.Context, key string) (io.ReadCloser, error) {
	if b.beforeOpen != nil {
		b.beforeOpen()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.opens++
	data, ok := b.objects[key]
	if !ok {
		return nil, errors.New("secret-storage-path")
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}
func (b *memoryBlobs) Delete(_ context.Context, key string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.deletes++
	delete(b.objects, key)
	return nil
}

func testWorker(t *testing.T, repo Repository, blobs *memoryBlobs) Worker {
	return Worker{Repo: repo, Blobs: blobs, Downloader: testDownloader(t, videoBytes, "video/mp4"), Resolver: resolverFunc(func(context.Context, uint64, string, Kind) (string, error) {
		return "https://cdn.example.com/out?signature=secret", nil
	}), LeaseDuration: time.Minute, TransferTimeout: time.Second, MaxAttempts: 3, Now: func() time.Time { return testNow }}
}

func TestTransferIdempotentAndScopedContent(t *testing.T) {
	repo := testRepo(t)
	register(t, repo, 1, "task_one", Video)
	blobs := &memoryBlobs{}
	worker := testWorker(t, repo, blobs)
	var resolutions atomic.Int32
	worker.Resolver = resolverFunc(func(_ context.Context, org uint64, task string, kind Kind) (string, error) {
		resolutions.Add(1)
		if org != 1 || task != "task_one" || kind != Video {
			t.Error("bad resolver scope")
		}
		return "https://cdn.example.com/out?signature=secret", nil
	})
	record, err := worker.Transfer(context.Background(), 1, "task_one", Video, false)
	if err != nil {
		t.Fatal(err)
	}
	if record.Status != Ready || record.Size != int64(len(videoBytes)) || len(record.SHA256) != 64 {
		t.Fatalf("record=%+v", record)
	}
	if _, err = worker.Transfer(context.Background(), 1, "task_one", Video, false); err != nil {
		t.Fatal(err)
	}
	if resolutions.Load() != 1 || blobs.puts != 1 {
		t.Fatal("duplicate transfer")
	}
	service := Service{Repo: repo, Blobs: blobs}
	_, err = service.Open(context.Background(), 2, "task_one", Video, testNow)
	code(t, err, "not_found")
	if blobs.opens != 0 {
		t.Fatal("cross-org storage access")
	}
	_, err = worker.Transfer(context.Background(), 2, "task_one", Video, false)
	code(t, err, "not_found")
	if resolutions.Load() != 1 {
		t.Fatal("cross-org source access")
	}
	content, err := service.Open(context.Background(), 1, "task_one", Video, testNow)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(content.Body)
	_ = content.Body.Close()
	if !bytes.Equal(data, videoBytes) || content.CacheControl != "private, no-store" {
		t.Fatal("bad content")
	}
	if strings.Contains(fmt.Sprintf("%+v", record), "signature") {
		t.Fatal("URL retained")
	}
	_, err = service.Open(context.Background(), 1, "task_one", Video, testNow.Add(time.Hour))
	code(t, err, "artifact_expired")
}

func TestConcurrentClaimsAndFencing(t *testing.T) {
	repo := testRepo(t)
	register(t, repo, 1, "task_concurrent", Video)
	var winners atomic.Int32
	var first Lease
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lease, err := repo.Claim(context.Background(), 1, "task_concurrent", Video, testNow, time.Minute, false, 3)
			if err == nil {
				winners.Add(1)
				mu.Lock()
				first = lease
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if winners.Load() != 1 {
		t.Fatalf("winners=%d", winners.Load())
	}
	now := testNow.Add(2 * time.Minute)
	_, err := repo.Claim(context.Background(), 1, "task_concurrent", Video, now, time.Minute, false, 3)
	code(t, err, "artifact_retry_required")
	second, err := repo.Claim(context.Background(), 1, "task_concurrent", Video, now, time.Minute, true, 3)
	if err != nil {
		t.Fatal(err)
	}
	meta := Metadata{MediaType: "video/mp4", Size: 20, SHA256: strings.Repeat("a", 64)}
	code(t, repo.Complete(context.Background(), first, meta, now), "artifact_lease_lost")
	code(t, repo.Fail(context.Background(), first, errors.New("private URL"), now), "artifact_lease_lost")
	if err = repo.Complete(context.Background(), second, meta, now); err != nil {
		t.Fatal(err)
	}
	record, err := repo.Lookup(context.Background(), 1, "task_concurrent", Video, now)
	if err != nil || record.Fence != second.Fence || record.Status != Ready {
		t.Fatalf("record=%+v err=%v", record, err)
	}
}

func TestFailureRequiresExplicitRetryAndAttemptCap(t *testing.T) {
	repo := testRepo(t)
	register(t, repo, 1, "task_failed", Video)
	blobs := &memoryBlobs{failPut: true}
	worker := testWorker(t, repo, blobs)
	worker.MaxAttempts = 2
	_, err := worker.Transfer(context.Background(), 1, "task_failed", Video, false)
	code(t, err, "artifact_blob_unavailable")
	record, _ := repo.Lookup(context.Background(), 1, "task_failed", Video, testNow)
	if record.Status != Failed || record.ErrorCode != "artifact_blob_unavailable" {
		t.Fatal(record)
	}
	_, err = worker.Transfer(context.Background(), 1, "task_failed", Video, false)
	code(t, err, "artifact_retry_required")
	_, err = worker.Transfer(context.Background(), 1, "task_failed", Video, true)
	code(t, err, "artifact_blob_unavailable")
	_, err = worker.Transfer(context.Background(), 1, "task_failed", Video, true)
	code(t, err, "artifact_attempt_limit")
	if blobs.puts != 2 {
		t.Fatalf("puts=%d", blobs.puts)
	}
}

func TestDeletionDuringUploadFencesPublication(t *testing.T) {
	repo := testRepo(t)
	register(t, repo, 1, "task_delete", Video)
	register(t, repo, 1, "task_delete", LastFrame)
	blobs := &memoryBlobs{}
	worker := testWorker(t, repo, blobs)
	blobs.beforePut = func() {
		if err := repo.DB.Transaction(func(tx *gorm.DB) error { return repo.HideTaskTx(tx, 1, "task_delete", testNow) }); err != nil {
			t.Fatal(err)
		}
	}
	_, err := worker.Transfer(context.Background(), 1, "task_delete", Video, false)
	code(t, err, "artifact_lease_lost")
	if len(blobs.objects) != 0 || blobs.deletes != 1 {
		t.Fatal("stale blob leaked")
	}
	service := Service{Repo: repo, Blobs: blobs}
	for _, kind := range []Kind{Video, LastFrame} {
		_, err = service.Open(context.Background(), 1, "task_delete", kind, testNow)
		code(t, err, "not_found")
	}
	if err = service.Cleanup(context.Background(), 1, "task_delete", Video, testNow); err != nil {
		t.Fatal(err)
	}
	if err = service.Cleanup(context.Background(), 1, "task_delete", Video, testNow); err != nil {
		t.Fatal(err)
	}
	_, err = repo.RegisterTx(repo.DB, Spec{OrgID: 1, TaskID: "task_delete", Kind: Video, ExpiresAt: testNow.Add(24 * time.Hour)}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	_, err = repo.Lookup(context.Background(), 1, "task_delete", Video, testNow)
	code(t, err, "not_found")
}

func TestTransferTimeoutStoredAndTempRemoved(t *testing.T) {
	repo := testRepo(t)
	register(t, repo, 1, "task_timeout", Video)
	worker := testWorker(t, repo, &memoryBlobs{})
	worker.TransferTimeout = 10 * time.Millisecond
	worker.Downloader.client.Transport = roundTrip(func(req *http.Request) (*http.Response, error) {
		<-req.Context().Done()
		return nil, req.Context().Err()
	})
	_, err := worker.Transfer(context.Background(), 1, "task_timeout", Video, false)
	code(t, err, "artifact_download_timeout")
	record, _ := repo.Lookup(context.Background(), 1, "task_timeout", Video, testNow)
	if record.Status != Failed || record.ErrorCode != "artifact_download_timeout" {
		t.Fatal(record)
	}
	entries, err := os.ReadDir(worker.Downloader.config.TempDir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("temp files=%v err=%v", entries, err)
	}
}

func TestDownloadPolicyAndBounds(t *testing.T) {
	for _, url := range []string{"http://cdn.example.com/a", "https://127.0.0.1/a", "https://cdn.example.com.evil/a", "https://u:p@cdn.example.com/a", "https://cdn.example.com:8443/a", "https://cdn.example.com/a#fragment", "https://cdn.example.com./a"} {
		t.Run(url, func(t *testing.T) {
			d := testDownloader(t, videoBytes, "video/mp4")
			_, err := d.Fetch(context.Background(), url, Video)
			code(t, err, "artifact_url_blocked")
		})
	}
	for _, tc := range []struct {
		name, media string
		data        []byte
		code        string
	}{{"html", "text/html", []byte("secret html"), "artifact_invalid_media"}, {"magic", "video/mp4", []byte("not video content"), "artifact_invalid_media"}, {"empty", "video/mp4", nil, "artifact_invalid_media"}, {"too big", "video/mp4", bytes.Repeat([]byte{1}, 2048), "artifact_too_large"}} {
		t.Run(tc.name, func(t *testing.T) {
			d := testDownloader(t, tc.data, tc.media)
			_, err := d.Fetch(context.Background(), "https://cdn.example.com/a", Video)
			code(t, err, tc.code)
			entries, _ := os.ReadDir(d.config.TempDir)
			if len(entries) != 0 {
				t.Fatal("temp leak")
			}
		})
	}
	d := testDownloader(t, bytes.Repeat([]byte{1}, 2048), "video/mp4")
	d.client.Transport = roundTrip(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"video/mp4"}}, ContentLength: -1, Body: io.NopCloser(bytes.NewReader(bytes.Repeat([]byte{1}, 2048)))}, nil
	})
	_, err := d.Fetch(context.Background(), "https://cdn.example.com/a", Video)
	code(t, err, "artifact_too_large")
	var requests int
	d.client.Transport = roundTrip(func(*http.Request) (*http.Response, error) {
		requests++
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"https://evil.example.com/secret"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	_, err = d.Fetch(context.Background(), "https://cdn.example.com/a", Video)
	code(t, err, "artifact_download_failed")
	if requests != 1 {
		t.Fatal("redirect followed")
	}
}

func TestJPEGAndTemporaryFileLifecycle(t *testing.T) {
	var data bytes.Buffer
	if err := jpeg.Encode(&data, image.NewRGBA(image.Rect(0, 0, 3, 4)), nil); err != nil {
		t.Fatal(err)
	}
	d := testDownloader(t, data.Bytes(), "image/jpeg")
	download, err := d.Fetch(context.Background(), "https://cdn.example.com/frame", LastFrame)
	if err != nil {
		t.Fatal(err)
	}
	if *download.Metadata.Width != 3 || *download.Metadata.Height != 4 {
		t.Fatal(download.Metadata)
	}
	entries, _ := os.ReadDir(d.config.TempDir)
	if len(entries) != 1 {
		t.Fatal("missing temporary file")
	}
	if err = download.Body.Close(); err != nil {
		t.Fatal(err)
	}
	entries, _ = os.ReadDir(d.config.TempDir)
	if len(entries) != 0 {
		t.Fatal("temporary file not removed")
	}
}

func TestDNSGuardAndPinnedDial(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "100.100.100.200", "0.0.0.0", "224.0.0.1", "192.0.0.192", "198.18.0.1", "::1", "::ffff:127.0.0.1", "64:ff9b::7f00:1", "2002:7f00:1::", "fd00:ec2::254"} {
		if publicIP(net.ParseIP(ip)) {
			t.Errorf("allowed %s", ip)
		}
	}
	for _, ip := range []string{"8.8.8.8", "1.1.1.1", "2606:4700:4700::1111"} {
		if !publicIP(net.ParseIP(ip)) {
			t.Errorf("blocked %s", ip)
		}
	}
	d := testDownloader(t, videoBytes, "video/mp4")
	var dialed string
	d.lookup = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}, {IP: net.ParseIP("127.0.0.1")}}, nil
	}
	d.dial = func(_ context.Context, _, address string) (net.Conn, error) {
		dialed = address
		return nil, errors.New("simulated dial failure")
	}
	_, err := d.dialContext(context.Background(), "tcp", "cdn.example.com:443")
	code(t, err, "artifact_url_blocked")
	if dialed != "" {
		t.Fatal("mixed DNS unsafe dial")
	}
	d.lookup = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}, nil
	}
	_, err = d.dialContext(context.Background(), "tcp", "cdn.example.com:443")
	code(t, err, "artifact_download_failed")
	if dialed != "8.8.8.8:443" {
		t.Fatalf("unpinned dial %s", dialed)
	}
}

func TestRegistrationRollbackAndIndependentKinds(t *testing.T) {
	repo := testRepo(t)
	rolledBack := errors.New("rollback")
	err := repo.DB.Transaction(func(tx *gorm.DB) error {
		_, err := repo.RegisterTx(tx, Spec{OrgID: 1, TaskID: "task_rollback", Kind: Video, ExpiresAt: testNow.Add(time.Hour)}, testNow)
		if err != nil {
			return err
		}
		return rolledBack
	})
	if !errors.Is(err, rolledBack) {
		t.Fatal(err)
	}
	_, err = repo.Lookup(context.Background(), 1, "task_rollback", Video, testNow)
	code(t, err, "not_found")
	register(t, repo, 1, "task_kinds", Video)
	register(t, repo, 1, "task_kinds", LastFrame)
	blobs := &memoryBlobs{}
	worker := testWorker(t, repo, blobs)
	if _, err = worker.Transfer(context.Background(), 1, "task_kinds", Video, false); err != nil {
		t.Fatal(err)
	}
	service := Service{Repo: repo, Blobs: blobs}
	_, err = service.Open(context.Background(), 1, "task_kinds", LastFrame, testNow)
	code(t, err, "artifact_not_ready")
	if err = service.Cleanup(context.Background(), 1, "task_kinds", Video, testNow); err == nil {
		t.Fatal("cleaned visible unexpired object")
	}
	if err = service.Cleanup(context.Background(), 1, "task_kinds", Video, testNow.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	_, err = service.Open(context.Background(), 1, "task_kinds", Video, testNow.Add(time.Hour))
	code(t, err, "artifact_expired")
}

func TestContentRechecksDeletionAfterBlobOpen(t *testing.T) {
	repo := testRepo(t)
	register(t, repo, 1, "task_race", Video)
	blobs := &memoryBlobs{}
	worker := testWorker(t, repo, blobs)
	if _, err := worker.Transfer(context.Background(), 1, "task_race", Video, false); err != nil {
		t.Fatal(err)
	}
	blobs.beforeOpen = func() {
		if err := repo.HideTaskTx(repo.DB, 1, "task_race", testNow); err != nil {
			t.Fatal(err)
		}
	}
	_, err := (Service{Repo: repo, Blobs: blobs}).Open(context.Background(), 1, "task_race", Video, testNow)
	code(t, err, "not_found")
}

func TestSourceFailureIsSafeAndDoesNotDownload(t *testing.T) {
	repo := testRepo(t)
	register(t, repo, 1, "task_source", Video)
	worker := testWorker(t, repo, &memoryBlobs{})
	worker.Resolver = resolverFunc(func(context.Context, uint64, string, Kind) (string, error) {
		return "", errors.New("https://cdn.example.com/path?secret=private")
	})
	worker.Downloader.client.Transport = roundTrip(func(*http.Request) (*http.Response, error) { t.Fatal("unexpected download"); return nil, nil })
	_, err := worker.Transfer(context.Background(), 1, "task_source", Video, false)
	code(t, err, "artifact_source_unavailable")
	record, _ := repo.Lookup(context.Background(), 1, "task_source", Video, testNow)
	if record.Status != Failed || record.ErrorCode != "artifact_source_unavailable" {
		t.Fatal(record)
	}
}
