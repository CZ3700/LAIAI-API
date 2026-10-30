package watermark

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"gorm.io/gorm"
)

// Resolver produces the fresh upstream signed URL for a delivered task. The
// signature in the delivery evidence expires, so it must be resolved at
// download time, never cached in the job row.
type Resolver interface {
	ResolveWatermarkSource(ctx context.Context, org uint64, taskID string) (string, error)
}

// MediaStorer persists the processed artifact and produces its signed delivery
// URL; implemented by videoasset's MediaStore.
type MediaStorer interface {
	StoreVideo(ctx context.Context, id string, r io.Reader) (int64, error)
	SignedURL(org uint64, id string, expires time.Time) string
}

// Coordinator runs the lease-based worker pool. Safe for concurrent use; one
// instance per process, additional processes on other hosts scale horizontally
// because leases decide ownership.
type Coordinator struct {
	DB          *gorm.DB
	Resolver    Resolver
	Media       MediaStorer
	Download    *http.Client  // bounded client for the upstream signed URL
	Processor   Processor     // Timeout must stay under the lease
	TempDir     string        // scratch dir for download+encode
	MaxBytes    int64         // input size cap
	Retention   time.Duration // processed artifact lifetime
	Concurrency int           // parallel ffmpeg jobs
	Interval    time.Duration // tick period
	Now         func() time.Time

	inflight atomic.Int64
}

// Run blocks until ctx is done, ticking the lease queue. While the toggle is
// off the loop idles — the feature stays fully bypassable without restarts.
func (c *Coordinator) Run(ctx context.Context) {
	if c.Now == nil {
		c.Now = time.Now
	}
	interval := c.Interval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	concurrency := c.Concurrency
	if concurrency <= 0 {
		concurrency = 1
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if !c.enabled() {
			continue
		}
		budget := concurrency - int(c.inflight.Load())
		if budget <= 0 {
			continue
		}
		jobs, err := Lease(c.DB, budget, c.Processor.Timeout+5*time.Minute, c.Now().UTC())
		if err != nil {
			continue
		}
		for _, job := range jobs {
			if c.inflight.Add(1) > int64(concurrency) {
				c.inflight.Add(-1)
				break
			}
			go func(job Job) {
				defer c.inflight.Add(-1)
				c.process(ctx, job)
			}(job)
		}
	}
}

func (c *Coordinator) enabled() bool {
	var row Settings
	if err := c.DB.Where("id = 1").Take(&row).Error; err != nil {
		return false
	}
	return row.Enabled
}

// DeliveryURL returns the processed artifact's signed URL for a delivered
// task, or ok=false when none is ready. The one-hour signature window covers
// playback; refreshes get a fresh URL.
func (c *Coordinator) DeliveryURL(org uint64, taskID string, now time.Time) (string, bool) {
	if c == nil || c.Media == nil {
		return "", false
	}
	job, err := Ready(c.DB, org, taskID, now)
	if err != nil || job == nil {
		return "", false
	}
	url := c.Media.SignedURL(org, job.ProcessedMediaID, now.Add(time.Hour))
	return url, url != ""
}

func (c *Coordinator) process(ctx context.Context, job Job) {
	now := c.Now().UTC()
	done, err := c.attempt(ctx, job)
	if err != nil {
		_ = Fail(c.DB, job, err.Error(), c.Now().UTC(), DefaultBackoff)
		return
	}
	_ = Complete(c.DB, job, done, now.Add(c.RetentionOrDefault()), c.Now().UTC())
}

func (c *Coordinator) RetentionOrDefault() time.Duration {
	if c.Retention > 0 {
		return c.Retention
	}
	return 30 * 24 * time.Hour
}

// attempt: resolve fresh URL → bounded download → probe+encode → store.
// Every stage writes into the scratch dir; failures clean up after themselves.
func (c *Coordinator) attempt(ctx context.Context, job Job) (string, error) {
	if c.Resolver == nil || c.Media == nil {
		return "", fmt.Errorf("watermark pipeline not wired")
	}
	source, err := c.Resolver.ResolveWatermarkSource(ctx, job.OrgID, job.TaskID)
	if err != nil {
		return "", fmt.Errorf("resolve source: %w", err)
	}
	if len(source) > 2048 || !httpsURL(source) {
		return "", fmt.Errorf("source url rejected")
	}
	dir := c.TempDir
	if dir == "" {
		dir = os.TempDir()
	}
	work, err := os.MkdirTemp(dir, "wm-")
	if err != nil {
		return "", fmt.Errorf("scratch: %w", err)
	}
	defer os.RemoveAll(work)
	inPath := filepath.Join(work, "in.mp4")
	if err := c.download(ctx, source, inPath); err != nil {
		return "", err
	}
	outPath := filepath.Join(work, "out.mp4")
	if err := c.Processor.Process(ctx, "", inPath, outPath); err != nil {
		return "", err
	}
	f, err := os.Open(outPath)
	if err != nil {
		return "", fmt.Errorf("open output: %w", err)
	}
	defer f.Close()
	mediaID, err := newMediaID()
	if err != nil {
		return "", err
	}
	if _, err := c.Media.StoreVideo(ctx, mediaID, f); err != nil {
		return "", fmt.Errorf("store: %w", err)
	}
	return mediaID, nil
}

func (c *Coordinator) download(ctx context.Context, source, dest string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return fmt.Errorf("source request: %w", err)
	}
	client := c.Download
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download: status %d", resp.StatusCode)
	}
	f, err := os.Create(dest)
	if err != nil {
		return fmt.Errorf("scratch: %w", err)
	}
	defer f.Close()
	max := c.MaxBytes
	if max <= 0 {
		max = 200_000_000
	}
	size, err := io.Copy(f, io.LimitReader(resp.Body, max+1))
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	if size < 1 || size > max {
		return fmt.Errorf("download: size %d out of range", size)
	}
	return nil
}

func httpsURL(s string) bool {
	return strings.HasPrefix(s, "https://") || strings.HasPrefix(s, "http://")
}

func newMediaID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "media_wm_" + hex.EncodeToString(raw[:]), nil
}
