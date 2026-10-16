package videoasset

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type MediaStoreConfig struct {
	Root, PublicBaseURL, FFProbePath string
	SigningKey                       []byte
	ScanCommand                      []string
}
type MediaStore struct {
	root, base, probe string
	key               []byte
	scan              []string
	client            *http.Client
}

func NewMediaStore(c MediaStoreConfig) (*MediaStore, error) {
	u, e := url.Parse(c.PublicBaseURL)
	if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || len(c.SigningKey) < 32 || len(c.ScanCommand) == 0 {
		return nil, ErrInvalid
	}
	root, e := filepath.Abs(c.Root)
	if e != nil || c.Root == "" {
		return nil, ErrInvalid
	}
	if e = os.MkdirAll(root, 0700); e != nil {
		return nil, ErrStorage
	}
	probe, e := exec.LookPath(c.FFProbePath)
	if e != nil {
		return nil, ErrUnsupported
	}
	scan, e := exec.LookPath(c.ScanCommand[0])
	if e != nil {
		return nil, ErrUnsupported
	}
	sc := append([]string{scan}, c.ScanCommand[1:]...)
	transport := &http.Transport{Proxy: nil, DialContext: publicDial, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 15 * time.Second, DisableKeepAlives: true}
	return &MediaStore{root: root, base: strings.TrimRight(c.PublicBaseURL, "/"), probe: probe, key: append([]byte(nil), c.SigningKey...), scan: sc, client: &http.Client{Transport: transport, Timeout: 3 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func publicIP(ip net.IP) bool {
	a, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	a = a.Unmap()
	if !a.IsGlobalUnicast() || a.IsPrivate() || a.IsLoopback() || a.IsLinkLocalUnicast() || a.IsMulticast() || a.IsUnspecified() {
		return false
	}
	for _, s := range []string{"100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001:db8::/32"} {
		if netip.MustParsePrefix(s).Contains(a) {
			return false
		}
	}
	return true
}
func publicDial(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, e := net.SplitHostPort(address)
	if e != nil || port != "443" {
		return nil, ErrInvalid
	}
	ips, e := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if e != nil || len(ips) == 0 {
		return nil, ErrInvalid
	}
	for _, ip := range ips {
		if !publicIP(ip) {
			return nil, ErrInvalid
		}
	}
	d := net.Dialer{Timeout: 10 * time.Second}
	return d.DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
}
func (m *MediaStore) path(id string) (string, error) {
	if !validID(id) || !strings.HasPrefix(id, "media_") {
		return "", ErrInvalid
	}
	p := filepath.Join(m.root, id)
	if filepath.Dir(p) != m.root {
		return "", ErrInvalid
	}
	return p, nil
}
func (m *MediaStore) url(org uint64, id string, expires time.Time) string {
	expiry := strconv.FormatInt(expires.Unix(), 10)
	subject := fmt.Sprintf("%d/%s/%s", org, id, expiry)
	signature := hex.EncodeToString(mac(m.key, subject))
	return m.base + "/" + id + "?org=" + strconv.FormatUint(org, 10) + "&expires=" + expiry + "&signature=" + signature
}

// ServeHTTP is mounted ONLY at the configured PublicBaseURL path. The random
// object name plus tenant-bound expiring signature is required before opening.
func (m *MediaStore) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" && r.Method != "HEAD" {
		w.WriteHeader(405)
		return
	}
	id := filepath.Base(r.URL.Path)
	org, e := strconv.ParseUint(r.URL.Query().Get("org"), 10, 64)
	expiry, e2 := strconv.ParseInt(r.URL.Query().Get("expires"), 10, 64)
	now := time.Now().Unix()
	provided, e3 := hex.DecodeString(r.URL.Query().Get("signature"))
	subject := fmt.Sprintf("%d/%s/%d", org, id, expiry)
	if e != nil || e2 != nil || e3 != nil || org == 0 || expiry <= now || expiry > now+24*3600 || !hmac.Equal(provided, mac(m.key, subject)) {
		http.NotFound(w, r)
		return
	}
	m.serve(w, r, id)
}
func (m *MediaStore) serve(w http.ResponseWriter, r *http.Request, id string) {
	p, e := m.path(id)
	if e != nil {
		http.NotFound(w, r)
		return
	}
	f, e := os.Open(p)
	if e != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, id, info.ModTime(), f)
}

type Upload struct {
	ID                            string `gorm:"primaryKey" json:"id"`
	OrgID                         uint64 `json:"-"`
	UserID                        uint64 `json:"-"`
	KeyID                         uint64 `json:"-"`
	MediaType                     string `json:"media_type"`
	Status                        string `json:"status"`
	IdempotencyHash               string `json:"-"`
	BodyHash                      string `json:"-"`
	ObjectRef                     string `json:"-"`
	ObjectSHA256                  string `json:"-"`
	MIME                          string `json:"mime,omitempty"`
	Bytes, DurationMS             int64
	Width, Height, FPSNum, FPSDen int
	ErrorCode                     string    `json:"error_code,omitempty"`
	ExpiresAt                     time.Time `json:"expires_at"`
	CreatedAt, UpdatedAt          time.Time
}

func (Upload) TableName() string { return "video_media_uploads" }
func (s *Service) BeginUpload(ctx context.Context, c CreateContext, mediaType string) (Upload, error) {
	if !validCreate(c) || s.console.Media == nil {
		return Upload{}, ErrUnsupported
	}
	r, e := s.resolve(ctx, c.OrgID, Scope{})
	if e != nil || !mediaAllowed(r.Capabilities, mediaType) {
		return Upload{}, ErrUnsupported
	}
	id, e := newID("media_")
	if e != nil {
		return Upload{}, e
	}
	key := s.digest(s.options.ActiveHMACKey, "upload", c.OrgID, "uploads", c.IdempotencyKey)
	body := s.digest(s.options.ActiveHMACKey, "upload-body", c.OrgID, mediaType, callerLabel(c))
	var out Upload
	e = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := lockTenant(tx, c.OrgID); e != nil {
			return e
		}
		e := tx.Where("org_id = ? AND idempotency_hash = ?", c.OrgID, key).Take(&out).Error
		if e == nil {
			if out.BodyHash != body {
				return ErrConflict
			}
			return nil
		}
		if !errors.Is(e, gorm.ErrRecordNotFound) {
			return ErrStorage
		}
		now := nowMillis(s)
		out = Upload{ID: id, OrgID: c.OrgID, UserID: c.UserID, KeyID: c.KeyID, MediaType: mediaType, Status: "uploading", IdempotencyHash: key, BodyHash: body, ExpiresAt: now.Add(24 * time.Hour), CreatedAt: now, UpdatedAt: now}
		if tx.Create(&out).Error != nil {
			return ErrStorage
		}
		return s.event(tx, c.OrgID, "upload", id, "", "upload_created", "", callerLabel(c))
	})
	return out, e
}
func (s *Service) GetUpload(ctx context.Context, org uint64, id string) (Upload, error) {
	var row Upload
	if s.db.WithContext(ctx).Where("org_id = ? AND id = ?", org, id).Take(&row).Error != nil {
		return row, ErrNotFound
	}
	return row, nil
}
func (s *Service) CompleteUpload(ctx context.Context, c CreateContext, id string, reader io.Reader, rawURL string) (Upload, error) {
	row, e := s.GetUpload(ctx, c.OrgID, id)
	if e != nil {
		return row, e
	}
	if row.UserID != c.UserID || row.KeyID != c.KeyID {
		return Upload{}, ErrAuthorization
	}
	if row.Status == "ready" {
		return row, nil
	}
	if row.Status != "uploading" || !row.ExpiresAt.After(s.now()) || s.console.Media == nil {
		return row, ErrConflict
	}
	claim := s.db.WithContext(ctx).Model(&Upload{}).Where("org_id = ? AND id = ? AND status = ?", c.OrgID, id, "uploading").Updates(map[string]any{"status": "verifying", "updated_at": nowMillis(s)})
	if claim.Error != nil || claim.RowsAffected != 1 {
		return row, ErrConflict
	}
	fail := func(code string) (Upload, error) {
		_ = s.db.Model(&Upload{}).Where("org_id = ? AND id = ?", c.OrgID, id).Updates(map[string]any{"status": "failed", "error_code": code, "updated_at": nowMillis(s)}).Error
		return Upload{}, ErrInvalid
	}
	if rawURL != "" {
		u, e := url.Parse(rawURL)
		if e != nil || !validHTTPS(rawURL, 4096) || u.Port() != "" && u.Port() != "443" {
			return fail("source_not_allowed")
		}
		req, e := http.NewRequestWithContext(ctx, "GET", rawURL, nil)
		if e != nil {
			return fail("source_not_allowed")
		}
		resp, e := s.console.Media.client.Do(req)
		if e != nil {
			return fail("source_unavailable")
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return fail("source_unavailable")
		}
		reader = resp.Body
	}
	if reader == nil {
		return fail("empty_upload")
	}
	meta, e := s.console.Media.write(ctx, id, row.MediaType, reader)
	if e != nil {
		return fail("media_verification_failed")
	}
	now := nowMillis(s)
	row.Status = "ready"
	row.ObjectRef = id
	row.ObjectSHA256 = meta.ObjectSHA256
	row.MIME = meta.MIME
	row.Bytes = meta.ByteSize
	row.DurationMS = meta.DurationMS
	row.Width = meta.Width
	row.Height = meta.Height
	row.FPSNum = meta.FPSNum
	row.FPSDen = meta.FPSDen
	row.UpdatedAt = now
	e = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := lockTenant(tx, c.OrgID); e != nil {
			return e
		}
		if tx.Model(&Upload{}).Where("org_id = ? AND id = ? AND status = ?", c.OrgID, id, "verifying").Select("status", "object_ref", "object_sha256", "mime", "bytes", "duration_ms", "width", "height", "fps_num", "fps_den", "updated_at").Updates(&row).Error != nil {
			return ErrStorage
		}
		return s.event(tx, c.OrgID, "upload", id, "", "media_verified", "", callerLabel(c))
	})
	return row, e
}
func (m *MediaStore) write(ctx context.Context, id, kind string, r io.Reader) (MediaEvidence, error) {
	out := MediaEvidence{}
	p, e := m.path(id)
	if e != nil {
		return out, e
	}
	tmp, e := os.CreateTemp(m.root, "pending-")
	if e != nil {
		return out, ErrStorage
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	defer tmp.Close()
	limit := int64(200_000_000)
	if kind == "image" {
		limit = 29_999_999
	}
	if kind == "audio" {
		limit = 15_000_000
	}
	h := sha256.New()
	size, e := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(r, limit+1))
	if e != nil || size < 1 || size > limit {
		return out, ErrInvalid
	}
	if tmp.Sync() != nil || tmp.Close() != nil {
		return out, ErrStorage
	}
	scanCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	args := append(append([]string(nil), m.scan[1:]...), tmpName)
	cmd := exec.CommandContext(scanCtx, m.scan[0], args...)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if cmd.Run() != nil {
		return out, ErrInvalid
	}
	probeCtx, cancelProbe := context.WithTimeout(ctx, 30*time.Second)
	defer cancelProbe()
	cmd = exec.CommandContext(probeCtx, m.probe, "-v", "error", "-protocol_whitelist", "file", "-show_entries", "format=format_name,duration:stream=codec_type,width,height,r_frame_rate", "-of", "json", tmpName)
	raw, e := cmd.Output()
	if e != nil || len(raw) > 1<<20 {
		return out, ErrInvalid
	}
	var probe struct {
		Streams []struct {
			CodecType     string `json:"codec_type"`
			Width, Height int
			Rate          string `json:"r_frame_rate"`
		}
		Format struct {
			Name     string `json:"format_name"`
			Duration string `json:"duration"`
		}
	}
	if json.Unmarshal(raw, &probe) != nil || len(probe.Streams) == 0 {
		return out, ErrInvalid
	}
	out = MediaEvidence{ImmutableObjectRef: id, ObjectSHA256: hex.EncodeToString(h.Sum(nil)), ByteSize: size, Version: 1, VerifiedAt: time.Now().UTC(), FetchURL: "https://verified.invalid/" + id}
	duration, e := time.ParseDuration(probe.Format.Duration + "s")
	if e == nil {
		out.DurationMS = duration.Milliseconds()
	}
	for _, stream := range probe.Streams {
		if stream.CodecType == "video" {
			out.Width = stream.Width
			out.Height = stream.Height
			fmt.Sscanf(stream.Rate, "%d/%d", &out.FPSNum, &out.FPSDen)
			break
		}
	}
	switch kind {
	case "image":
		f, e := os.Open(tmpName)
		if e != nil {
			return out, ErrStorage
		}
		head := make([]byte, 512)
		n, _ := f.Read(head)
		f.Close()
		out.MIME = http.DetectContentType(head[:n])
	case "video":
		if !strings.Contains(probe.Format.Name, "mp4") && !strings.Contains(probe.Format.Name, "mov") {
			return out, ErrInvalid
		}
		out.MIME = "video/mp4"
	case "audio":
		if strings.Contains(probe.Format.Name, "mp3") {
			out.MIME = "audio/mpeg"
		} else if probe.Format.Name == "wav" {
			out.MIME = "audio/wav"
		}
	default:
		return out, ErrInvalid
	}
	if !validMedia(out, kind, time.Now()) {
		return out, ErrInvalid
	}
	if _, e = os.Stat(p); !os.IsNotExist(e) {
		return out, ErrConflict
	}
	if e = os.Rename(tmpName, p); e != nil {
		return out, ErrStorage
	}
	return out, nil
}
func (s *Service) uploadEvidence(ctx context.Context, org uint64, id, kind string) (MediaEvidence, error) {
	row, e := s.GetUpload(ctx, org, id)
	if e != nil || row.Status != "ready" || row.MediaType != kind || !row.ExpiresAt.After(s.now()) || s.console.Media == nil {
		return MediaEvidence{}, ErrInvalid
	}
	return MediaEvidence{ImmutableObjectRef: row.ObjectRef, ObjectSHA256: row.ObjectSHA256, MIME: row.MIME, ByteSize: row.Bytes, DurationMS: row.DurationMS, Width: row.Width, Height: row.Height, FPSNum: row.FPSNum, FPSDen: row.FPSDen, Version: 1, VerifiedAt: s.now(), FetchURL: s.console.Media.url(org, row.ID, s.now().Add(12*time.Hour))}, nil
}
func (s *Service) CreateAssetFromMedia(ctx context.Context, c CreateContext, group, id, name string) (AssetDTO, bool, error) {
	row, e := s.GetUpload(ctx, c.OrgID, id)
	if e != nil {
		return AssetDTO{}, false, e
	}
	media, e := s.uploadEvidence(ctx, c.OrgID, id, row.MediaType)
	if e != nil {
		return AssetDTO{}, false, e
	}
	return s.CreateAsset(ctx, c, AssetCreate{GroupID: group, URL: media.FetchURL, MediaType: row.MediaType, Name: name})
}
func (s *Service) verifyMedia(ctx context.Context, org uint64, raw, kind string) (MediaEvidence, error) {
	if s.console.Media != nil && strings.HasPrefix(raw, s.console.Media.base+"/") {
		u, e := url.Parse(raw)
		if e != nil {
			return MediaEvidence{}, ErrInvalid
		}
		id := filepath.Base(u.Path)
		evidence, e := s.uploadEvidence(ctx, org, id, kind)
		if e != nil {
			return MediaEvidence{}, e
		}
		return evidence, nil
	}
	if s.options.VerifyMedia == nil {
		return MediaEvidence{}, ErrUnsupported
	}
	return s.options.VerifyMedia(ctx, org, raw, kind)
}
func (s *Service) Preview(ctx context.Context, org uint64, id string, w http.ResponseWriter, r *http.Request) error {
	if s.console.Media == nil {
		return ErrUnsupported
	}
	var a Asset
	if s.db.WithContext(ctx).Where("org_id = ? AND asset_id = ? AND deleted_at IS NULL", org, id).Take(&a).Error != nil {
		return ErrNotFound
	}
	if a.ImmutableObjectRef == "" {
		return ErrUnsupported
	}
	if e := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error { return s.event(tx, org, "asset", id, "", "preview_read", "", "session") }); e != nil {
		return e
	}
	s.console.Media.serve(w, r, a.ImmutableObjectRef)
	return nil
}

// Imported media is verified from a fresh, scoped official GetAsset URL. A
// customer upload cannot be substituted as the evidence for an existing Ark ID.
func (s *Service) VerifyImportedAsset(ctx context.Context, org uint64, id string) error {
	if s.console.Media == nil {
		return ErrUnsupported
	}
	var a Asset
	if s.db.WithContext(ctx).Where("org_id = ? AND asset_id = ? AND deleted_at IS NULL", org, id).Take(&a).Error != nil {
		return ErrNotFound
	}
	if a.ProviderID == nil || a.Status != StatusActive {
		return ErrConflict
	}
	if a.ImmutableObjectRef != "" && a.ReviewState == ReviewClear {
		return nil
	}
	var g Group
	if s.db.WithContext(ctx).Where("org_id = ? AND group_id = ? AND deleted_at IS NULL", org, a.GroupID).Take(&g).Error != nil {
		return ErrNotFound
	}
	r, e := s.resolve(ctx, org, groupScope(g))
	if e != nil {
		return e
	}
	result := s.options.Client.Call(ctx, r.Scope, r.Credentials, "GetAsset", bodyJSON(map[string]string{"Id": *a.ProviderID, "ProjectName": g.Project}), s.now())
	defer clear(result.Body)
	defer clear(result.Data)
	var official upstreamAsset
	if result.Outcome != "accepted" || json.Unmarshal(result.Data, &official) != nil || official.ID != *a.ProviderID || g.ProviderID == nil || official.GroupID != *g.ProviderID || official.ProjectName != g.Project || strings.ToLower(official.AssetType) != a.MediaType || official.Status != "Active" || !validHTTPS(official.URL, 4096) {
		return ErrInvalid
	}
	req, e := http.NewRequestWithContext(ctx, "GET", official.URL, nil)
	if e != nil {
		return ErrInvalid
	}
	response, e := s.console.Media.client.Do(req)
	if e != nil {
		return ErrInvalid
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return ErrInvalid
	}
	object, e := newID("media_")
	if e != nil {
		return e
	}
	media, e := s.console.Media.write(ctx, object, a.MediaType, response.Body)
	if e != nil {
		return e
	}
	now := nowMillis(s)
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := lockTenant(tx, org); e != nil {
			return e
		}
		var current Group
		if tx.Where("org_id = ? AND group_id = ? AND status = ? AND deleted_at IS NULL", org, g.ID, StatusActive).Take(&current).Error != nil {
			return ErrConflict
		}
		res := tx.Model(&Asset{}).Where("org_id = ? AND asset_id = ? AND row_version = ? AND status = ? AND deleted_at IS NULL", org, id, a.RowVersion, StatusActive).Updates(map[string]any{"immutable_object_ref": object, "object_sha256": media.ObjectSHA256, "mime": media.MIME, "byte_size": media.ByteSize, "duration_ms": media.DurationMS, "width": media.Width, "height": media.Height, "fps_num": media.FPSNum, "fps_den": media.FPSDen, "metadata_version": gorm.Expr("metadata_version + 1"), "verified_at": now, "review_state": ReviewClear, "row_version": gorm.Expr("row_version + 1"), "updated_at": now})
		if res.Error != nil {
			return ErrStorage
		}
		if res.RowsAffected != 1 {
			return ErrConflict
		}
		return s.event(tx, org, "asset", id, "", "imported_media_verified", "", "worker")
	})
}
