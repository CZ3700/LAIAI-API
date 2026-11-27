package ark

import (
	"cmp"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/laiai/laiai-next/core/internal/crypto"
	"github.com/laiai/laiai-next/core/internal/forward"
	"github.com/laiai/laiai-next/core/internal/relay"
	"golang.org/x/sync/errgroup"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	arkNameMaxChars         = 64
	SourceRelayConvert      = "relay_convert"
	SourceUpstreamRelay     = "upstream_relay_convert"
	SourceAPIProxy          = "api_proxy"
	PluginUpstreamAssetName = "upstream_asset_relay"
	PluginAssetManager      = "asset_manager"
	PluginAssetManagerIntl  = "asset_manager_intl"
	ProtocolArkAction       = "ark_action"
	ProtocolPortraitREST    = "portrait_rest"
	ProtocolTencentVOD      = "tencent_vod"
	ProtocolBaiduCloud      = "baidu_cloud"
)

// UpstreamAssetBinding 对应表 upstream_asset_bindings（火山视频转素材ID 上游绑定）。
type UpstreamAssetBinding struct {
	ID              uint64    `gorm:"primaryKey;column:id" json:"id"`
	RID             string    `gorm:"column:rid" json:"rid"`
	Name            string    `gorm:"column:name" json:"name"`
	SourceType      string    `gorm:"column:source_type" json:"source_type"`
	ChannelConfigID uint64    `gorm:"column:channel_config_id" json:"channel_config_id"`
	AssetBasePath   string    `gorm:"column:asset_base_path" json:"asset_base_path"`
	AssetProtocol   string    `gorm:"column:asset_protocol" json:"asset_protocol"`
	VolcAK          string    `gorm:"column:volc_ak" json:"volc_ak"`
	VolcSKEnc       string    `gorm:"column:volc_sk_enc" json:"-"`
	VolcSKMask      string    `gorm:"column:volc_sk_mask" json:"volc_sk_mask"`
	VolcProjectName string    `gorm:"column:volc_project_name" json:"volc_project_name"`
	VolcRegion      string    `gorm:"column:volc_region" json:"volc_region"`
	ForwardRuleID   uint64    `gorm:"column:forward_rule_id" json:"forward_rule_id"`
	GroupID         string    `gorm:"column:group_id" json:"group_id"`
	IsActive        int8      `gorm:"column:is_active" json:"is_active"`
	Remark          string    `gorm:"column:remark" json:"remark"`
	CreatedAt       time.Time `gorm:"column:created_at" json:"created_at"`
	UpdatedAt       time.Time `gorm:"column:updated_at" json:"updated_at"`
}

func (UpstreamAssetBinding) TableName() string { return "upstream_asset_bindings" }

// PluginAssetGroup 对应表 plugin_asset_groups（租户隔离的方舟素材组归属记录）。
type PluginAssetGroup struct {
	ID          uint64    `gorm:"primaryKey;column:id" json:"id"`
	OrgID       uint64    `gorm:"column:org_id" json:"org_id"`
	UserID      uint64    `gorm:"column:user_id" json:"user_id"`
	GroupID     string    `gorm:"column:group_id" json:"group_id"`
	Name        string    `gorm:"column:name" json:"name"`
	Description string    `gorm:"column:description" json:"description"`
	PluginNS    string    `gorm:"column:plugin_ns" json:"plugin_ns"`
	CreatedAt   time.Time `gorm:"column:created_at" json:"created_at"`
	UpdatedAt   time.Time `gorm:"column:updated_at" json:"updated_at"`
}

func (PluginAssetGroup) TableName() string { return "plugin_asset_groups" }

// PluginAsset 对应表 plugin_assets（方舟素材缓存与租户归属记录）。
type PluginAsset struct {
	ID              uint64    `gorm:"primaryKey;column:id" json:"id"`
	OrgID           uint64    `gorm:"column:org_id" json:"org_id"`
	UserID          uint64    `gorm:"column:user_id" json:"user_id"`
	AssetType       string    `gorm:"column:asset_type" json:"asset_type"`
	Source          string    `gorm:"column:source" json:"source"`
	Status          string    `gorm:"column:status" json:"status"`
	FileName        string    `gorm:"column:file_name" json:"file_name"`
	FileURL         string    `gorm:"column:file_url" json:"file_url"`
	AssetID         string    `gorm:"column:asset_id" json:"asset_id"`
	GroupID         string    `gorm:"column:group_id" json:"group_id"`
	Category        string    `gorm:"column:category" json:"category"`
	ContentHash     string    `gorm:"column:content_hash" json:"content_hash"`
	MetaFingerprint string    `gorm:"column:meta_fingerprint" json:"meta_fingerprint"`
	PluginNS        string    `gorm:"column:plugin_ns" json:"plugin_ns"`
	StorageProvider string    `gorm:"column:storage_provider" json:"storage_provider"`
	CreatedAt       time.Time `gorm:"column:created_at" json:"created_at"`
	UpdatedAt       time.Time `gorm:"column:updated_at" json:"updated_at"`
}

func (PluginAsset) TableName() string { return "plugin_assets" }

// BindingNS 返回上游素材绑定的缓存命名空间（与 tkeapi `binding_ns` 对齐）。
func BindingNS(bindingID uint64) string {
	return fmt.Sprintf("uar_%d", bindingID)
}

// GenerateRID 生成 5 位十六进制路由短标识（用于 /v1/plugin/:rid/assets）。
func GenerateRID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%05x", time.Now().UnixNano()&0xfffff)
	}
	return hex.EncodeToString(b[:])[:5]
}

// NormalizeArkAssetID 规范化 asset://asset-xxx 前缀。
func NormalizeArkAssetID(id string) string {
	s := strings.TrimSpace(id)
	s = strings.TrimPrefix(s, "asset://")
	return strings.TrimSpace(s)
}

// ClampArkName 将素材组名截断至 64 字符上限。
func ClampArkName(name string) string {
	return clampName(name, false)
}

// ClampCreateAssetName 将素材名截断至 64 字符上限并尽量保留扩展名。
func ClampCreateAssetName(name string) string {
	return clampName(name, true)
}

func clampName(name string, keepExt bool) string {
	runes := []rune(name)
	if len(runes) <= arkNameMaxChars {
		return name
	}
	if keepExt {
		if dot := strings.LastIndex(name, "."); dot > 0 && !strings.Contains(name[dot+1:], "/") {
			ext := []rune(name[dot:])
			if len(ext) <= 16 {
				prefix := []rune(name[:dot])
				if len(prefix) > arkNameMaxChars-len(ext) {
					prefix = prefix[:arkNameMaxChars-len(ext)]
				}
				return string(prefix) + string(ext)
			}
		}
	}
	return string(runes[:arkNameMaxChars])
}

// DeriveCreateAssetName 从 URL 末段推导出规范化文件名（缺省按短哈希兜底）。
func DeriveCreateAssetName(rawURL, assetType string) string {
	bare, _, _ := strings.Cut(rawURL, "?")
	bare, _, _ = strings.Cut(bare, "#")
	parts := strings.Split(strings.Trim(bare, "/"), "/")
	var last string
	if len(parts) > 0 {
		last = parts[len(parts)-1]
	}
	if last == "" || last == bare {
		sum := sha256.Sum256([]byte(rawURL))
		last = fmt.Sprintf("tb_%s_%s", strings.ToLower(cmp.Or(assetType, "image")), hex.EncodeToString(sum[:4]))
	}
	return ClampCreateAssetName(last)
}

// ResolveAssetType 根据显式类型或 URL 后缀推断方舟 AssetType ("Image" / "Video" / "Audio")。
func ResolveAssetType(explicit, rawURL string) string {
	switch strings.ToLower(strings.TrimSpace(explicit)) {
	case "video":
		return "Video"
	case "audio":
		return "Audio"
	case "image":
		return "Image"
	}
	bare := strings.ToLower(rawURL)
	if i := strings.IndexAny(bare, "?#"); i >= 0 {
		bare = bare[:i]
	}
	switch {
	case strings.HasSuffix(bare, ".mp4") || strings.HasSuffix(bare, ".mov") || strings.HasSuffix(bare, ".webm") || strings.HasSuffix(bare, ".mkv"):
		return "Video"
	case strings.HasSuffix(bare, ".mp3") || strings.HasSuffix(bare, ".wav") || strings.HasSuffix(bare, ".ogg") || strings.HasSuffix(bare, ".m4a") || strings.HasSuffix(bare, ".aac"):
		return "Audio"
	default:
		return "Image"
	}
}

func isHTTPMediaURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

func isSafePublicMediaURL(rawURL string) bool {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return false
	}
	host := u.Hostname()
	if strings.EqualFold(host, "localhost") {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() || ip.IsMulticast() {
			return false
		}
	}
	return true
}

func contentRangeTotal(cr string) string {
	idx := strings.LastIndex(cr, "/")
	if idx < 0 {
		return ""
	}
	total := strings.TrimSpace(cr[idx+1:])
	if total == "" || total == "*" {
		return ""
	}
	for i := 0; i < len(total); i++ {
		if total[i] < '0' || total[i] > '9' {
			return ""
		}
	}
	return total
}

// FetchMetaFingerprint 通过 Range=bytes=0-0 获取对象元数据指纹（SHA256(bareURL|len|ETag|Last-Modified)）。
// 仅对公网安全 URL（或显式 allowPrivate 场景）发探测，失败时返回空串降级到 L2 URL 匹配。
var publicMediaProbeClient = relay.NewGuardedMediaClient(10*time.Second, false)

func FetchMetaFingerprint(ctx context.Context, httpClient *http.Client, rawURL string, allowPrivate bool) string {
	if !allowPrivate && !isSafePublicMediaURL(rawURL) {
		return ""
	}
	if httpClient == nil {
		httpClient = publicMediaProbeClient
		if allowPrivate {
			httpClient = relay.NewGuardedMediaClient(10*time.Second, true)
		}
	}
	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, rawURL, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Range", "bytes=0-0")
	resp, err := httpClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ""
	}

	etag := resp.Header.Get("ETag")
	lastMod := resp.Header.Get("Last-Modified")
	fullLen := contentRangeTotal(resp.Header.Get("Content-Range"))
	if fullLen == "" && resp.StatusCode == http.StatusOK {
		fullLen = resp.Header.Get("Content-Length")
	}
	if fullLen == "" && etag == "" && lastMod == "" {
		return ""
	}

	bare := rawURL
	if i := strings.IndexAny(bare, "?#"); i >= 0 {
		bare = bare[:i]
	}
	h := sha256.New()
	_, _ = h.Write([]byte(bare))
	_, _ = h.Write([]byte("|"))
	_, _ = h.Write([]byte(fullLen))
	_, _ = h.Write([]byte("|"))
	_, _ = h.Write([]byte(etag))
	_, _ = h.Write([]byte("|"))
	_, _ = h.Write([]byte(lastMod))
	return hex.EncodeToString(h.Sum(nil))
}

// LookupCachedConvertedAsset 按 tkeapi L1/L2 规则查找缓存的方舟素材 ID：
// 1. 若 metaFP 非空且命中则返回；若 metaFP 非空但未命中，**跳过** URL 匹配（防止同 URL 内容被覆盖误复用）；
// 2. 仅当 metaFP 为空时降级按 file_url 精确匹配。
func LookupCachedConvertedAsset(db *gorm.DB, orgID uint64, rawURL, pluginNS, source, metaFP string) string {
	var row PluginAsset
	db = db.Where("org_id = ?", orgID)
	if metaFP != "" {
		if err := db.Select("asset_id").
			Where("meta_fingerprint = ? AND source = ? AND plugin_ns = ? AND asset_id <> ''", metaFP, source, pluginNS).
			Order("id DESC").
			First(&row).Error; err == nil {
			return row.AssetID
		}
		return ""
	}
	if err := db.Select("asset_id").
		Where("file_url = ? AND source = ? AND plugin_ns = ? AND asset_id <> ''", rawURL, source, pluginNS).
		Order("id DESC").
		First(&row).Error; err == nil {
		return row.AssetID
	}
	return ""
}

// MediaConvertTarget 描述请求体中的一个待转换媒体引用。
type MediaConvertTarget struct {
	Kind      string // "content" | "fileinfo"
	Index     int
	URLKey    string // "image_url" | "video_url" | "audio_url"
	AssetType string // "Image" | "Video" | "Audio"
	URL       string
}

// CollectMediaConvertTargets 扫描 content[] 与 FileInfos[] 收集需要转为 asset:// 的媒体 URL。
func CollectMediaConvertTargets(body map[string]any) []MediaConvertTarget {
	if len(body) == 0 {
		return nil
	}
	var out []MediaConvertTarget
	if rawContent, ok := body["content"].([]any); ok {
		for idx, item := range rawContent {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			typ, _ := m["type"].(string)
			var urlKey, assetType string
			switch typ {
			case "image_url":
				urlKey, assetType = "image_url", "Image"
			case "video_url":
				urlKey, assetType = "video_url", "Video"
			case "audio_url":
				urlKey, assetType = "audio_url", "Audio"
			default:
				continue
			}
			sub, _ := m[urlKey].(map[string]any)
			if sub == nil {
				continue
			}
			rawURL, _ := sub["url"].(string)
			rawURL = strings.TrimSpace(rawURL)
			if rawURL == "" || strings.HasPrefix(rawURL, "asset://") {
				continue
			}
			out = append(out, MediaConvertTarget{
				Kind:      "content",
				Index:     idx,
				URLKey:    urlKey,
				AssetType: assetType,
				URL:       rawURL,
			})
		}
	}
	if rawFIs, ok := body["FileInfos"].([]any); ok {
		for idx, item := range rawFIs {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			rawURL, _ := m["Url"].(string)
			if strings.TrimSpace(rawURL) == "" {
				rawURL, _ = m["Base64"].(string)
			}
			rawURL = strings.TrimSpace(rawURL)
			if rawURL == "" || strings.HasPrefix(rawURL, "asset://") {
				continue
			}
			cat, _ := m["Category"].(string)
			out = append(out, MediaConvertTarget{
				Kind:      "fileinfo",
				Index:     idx,
				AssetType: ResolveAssetType(cat, rawURL),
				URL:       rawURL,
			})
		}
	}
	return out
}

func applyConvertedAssetToTarget(body map[string]any, tgt MediaConvertTarget, assetID string) {
	assetRef := "asset://" + NormalizeArkAssetID(assetID)
	switch tgt.Kind {
	case "content":
		if arr, ok := body["content"].([]any); ok && tgt.Index < len(arr) {
			if m, ok := arr[tgt.Index].(map[string]any); ok {
				if sub, ok := m[tgt.URLKey].(map[string]any); ok {
					sub["url"] = assetRef
				}
			}
		}
	case "fileinfo":
		if arr, ok := body["FileInfos"].([]any); ok && tgt.Index < len(arr) {
			if m, ok := arr[tgt.Index].(map[string]any); ok {
				m["Url"] = assetRef
				m["Type"] = "Url"
				delete(m, "Base64")
			}
		}
	}
}

// ConvertRequestOptions 控制媒体转 asset:// 的凭证、协议与测试开关。
type ConvertRequestOptions struct {
	OrgID                  uint64
	UserID                 uint64
	AssetConvert           bool
	AssetConvertNS         string
	AssetModeration        bool
	UpstreamAssetConvert   bool
	UpstreamAssetBindingID int64
	AllowPrivateFetch      bool
	PollInterval           time.Duration // 0 则使用标准轮询延迟
	OpenAIOverride         string        // 仅测试可覆盖 asset_manager 凭证 Endpoint
}

// ConvertBodyAssets 扫描请求体或参数中的 content[] / FileInfos[] 并原地替换为 asset://<asset_id>。
func ConvertBodyAssets(
	ctx context.Context,
	db *gorm.DB,
	httpClient *http.Client,
	keyring *crypto.Keyring,
	opts ConvertRequestOptions,
	body map[string]any,
) ([]string, error) {
	targets := CollectMediaConvertTargets(body)
	if len(targets) == 0 {
		return nil, nil
	}

	if opts.UpstreamAssetConvert && opts.UpstreamAssetBindingID > 0 {
		return convertViaUpstreamBinding(ctx, db, httpClient, keyring, opts, targets, body)
	}
	if opts.AssetConvert {
		return convertViaPluginConfig(ctx, db, httpClient, keyring, opts, targets, body)
	}
	return nil, nil
}

func loadExtSettingMap(db *gorm.DB, prefix string) map[string]string {
	var rows []struct {
		Name  string `gorm:"column:name"`
		Value string `gorm:"column:value"`
	}
	_ = db.Table("ext_settings").Where("name LIKE ?", prefix+"%").Find(&rows).Error
	out := make(map[string]string, len(rows))
	for _, r := range rows {
		out[strings.TrimPrefix(r.Name, prefix)] = r.Value
	}
	return out
}

func saveExtSetting(db *gorm.DB, name, value string) error {
	return db.Table("ext_settings").Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "name"}},
		DoUpdates: clause.AssignmentColumns([]string{"value"}),
	}).Create(map[string]any{"name": name, "value": value}).Error
}

type conversionGroup struct {
	mu sync.Mutex
	id string
}

// resolve returns the current group, creating a replacement if it is missing or stale.
func (g *conversionGroup) resolve(ctx context.Context, stale string, create func(context.Context) (string, error)) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.id != "" && g.id != stale {
		return g.id, nil
	}
	groupID, err := create(ctx)
	if err != nil {
		return "", err
	}
	if groupID == "" {
		return "", errors.New("asset group response missing ID")
	}
	g.id = groupID
	return groupID, nil
}

func decryptIfSealed(keyring *crypto.Keyring, raw string) (string, error) {
	if strings.HasPrefix(raw, "enc:v1:") {
		if keyring == nil {
			return "", errors.New("ark: keyring not configured for sealed secret")
		}
		return keyring.Decrypt(raw)
	}
	return raw, nil
}

func convertViaPluginConfig(
	ctx context.Context,
	db *gorm.DB,
	httpClient *http.Client,
	keyring *crypto.Keyring,
	opts ConvertRequestOptions,
	targets []MediaConvertTarget,
	body map[string]any,
) ([]string, error) {
	ns := cmp.Or(strings.TrimSpace(opts.AssetConvertNS), PluginAssetManager)
	cfgMap := loadExtSettingMap(db, "plugin_cfg:"+ns+":")
	ak := strings.TrimSpace(cfgMap["volc_access_key"])
	skRaw := strings.TrimSpace(cfgMap["volc_secret_key"])
	if ak == "" || skRaw == "" {
		return []string{fmt.Sprintf("素材转换跳过: 未配置 %s 审核凭证", ns)}, nil
	}
	sk, err := decryptIfSealed(keyring, skRaw)
	if err != nil {
		return nil, fmt.Errorf("解密素材管理密钥失败: %w", err)
	}
	clientCfg := ClientConfig{
		AccessKey:       ak,
		SecretKey:       sk,
		Region:          cmp.Or(strings.TrimSpace(cfgMap["volc_region"]), DefaultVolcRegion),
		ProjectName:     cmp.Or(strings.TrimSpace(cfgMap["volc_project_name"]), "default"),
		GroupID:         strings.TrimSpace(cfgMap["volc_group_id"]),
		BaseURLOverride: cmp.Or(strings.TrimSpace(opts.OpenAIOverride), strings.TrimSpace(cfgMap["volc_endpoint"])),
		ThrottleScope:   ns,
	}
	client := NewClient(clientCfg, httpClient)
	if opts.PollInterval > 0 {
		client.rateRetryScale = opts.PollInterval
	}

	group := conversionGroup{id: clientCfg.GroupID}
	createGroup := func(ctx context.Context) (string, error) {
		gid, err := client.CreateAssetGroup(ctx, "laiai_auto_generated_group", "laiai conversion assets")
		if err != nil {
			return "", err
		}
		if gid == "" {
			return "", errors.New("asset group response missing ID")
		}
		return gid, saveExtSetting(db.WithContext(ctx), "plugin_cfg:"+ns+":volc_group_id", gid)
	}

	return convertTargetsConcurrent(ctx, db, httpClient, opts, targets, body, ns, SourceRelayConvert, func(gctx context.Context, tgt MediaConvertTarget) (string, error) {
		groupID, err := group.resolve(gctx, "", createGroup)
		if err != nil {
			return "", err
		}
		aid, cerr := client.CreateAsset(gctx, groupID, tgt.URL, tgt.AssetType, "", opts.AssetModeration)
		if cerr != nil && IsGroupNotFoundErr(cerr.Error()) {
			newGID, err := group.resolve(gctx, groupID, createGroup)
			if err != nil {
				return "", err
			}
			aid, cerr = client.CreateAsset(gctx, newGID, tgt.URL, tgt.AssetType, "", opts.AssetModeration)
		}
		if cerr != nil {
			return "", cerr
		}
		if perr := pollAssetReady(gctx, client, aid, tgt.URL, opts.PollInterval); perr != nil {
			return "", perr
		}
		return aid, nil
	})
}

func convertViaUpstreamBinding(
	ctx context.Context,
	db *gorm.DB,
	httpClient *http.Client,
	keyring *crypto.Keyring,
	opts ConvertRequestOptions,
	targets []MediaConvertTarget,
	body map[string]any,
) ([]string, error) {
	var binding UpstreamAssetBinding
	if err := db.First(&binding, opts.UpstreamAssetBindingID).Error; err != nil {
		return nil, fmt.Errorf("上游素材绑定 #%d 不存在: %w", opts.UpstreamAssetBindingID, err)
	}
	if binding.IsActive != 1 {
		return []string{fmt.Sprintf("上游素材转换跳过: 绑定#%d 已停用", binding.ID)}, nil
	}

	ns := BindingNS(binding.ID)
	createFn, err := buildBindingCreateAssetFn(ctx, db, httpClient, keyring, &binding, opts.PollInterval)
	if err != nil {
		return nil, err
	}
	return convertTargetsConcurrent(ctx, db, httpClient, opts, targets, body, ns, SourceUpstreamRelay, createFn)
}

func buildBindingCreateAssetFn(
	ctx context.Context,
	db *gorm.DB,
	httpClient *http.Client,
	keyring *crypto.Keyring,
	binding *UpstreamAssetBinding,
	pollInterval time.Duration,
) (func(context.Context, MediaConvertTarget) (string, error), error) {
	if binding.SourceType == "volc_account" {
		sk, err := decryptIfSealed(keyring, binding.VolcSKEnc)
		if err != nil {
			return nil, err
		}
		client := NewClient(ClientConfig{
			AccessKey:     binding.VolcAK,
			SecretKey:     sk,
			Region:        cmp.Or(binding.VolcRegion, DefaultVolcRegion),
			ProjectName:   cmp.Or(binding.VolcProjectName, "default"),
			GroupID:       binding.GroupID,
			ThrottleScope: BindingNS(binding.ID),
		}, httpClient)
		if strings.TrimSpace(binding.AssetBasePath) != "" && strings.HasPrefix(binding.AssetBasePath, "http") {
			client.cfg.BaseURLOverride = strings.TrimSpace(binding.AssetBasePath)
		}
		gid := strings.TrimSpace(binding.GroupID)
		if gid == "" {
			newGID, gerr := client.CreateAssetGroup(ctx, fmt.Sprintf("laiai_uar_%d", binding.ID), "上游视频转素材自动建组")
			if gerr != nil {
				return nil, gerr
			}
			gid = newGID
			_ = db.Model(&UpstreamAssetBinding{}).Where("id = ?", binding.ID).Update("group_id", gid).Error
		}
		return func(gctx context.Context, tgt MediaConvertTarget) (string, error) {
			aid, err := client.CreateAsset(gctx, gid, tgt.URL, tgt.AssetType, "", false)
			if err != nil {
				return "", err
			}
			if perr := pollAssetReady(gctx, client, aid, tgt.URL, pollInterval); perr != nil {
				return "", perr
			}
			return aid, nil
		}, nil
	}

	// 默认 source_type = "channel": 关联 channel_configs 上游预设经 Bearer 调用 CreateAsset/GetAsset。
	var cfgRow struct {
		BaseURL   string `gorm:"column:base_url"`
		APIKeyEnc string `gorm:"column:api_key_enc"`
		Status    int8   `gorm:"column:status"`
	}
	if err := db.Table("channel_configs").Select("base_url, api_key_enc, status").Where("id = ?", binding.ChannelConfigID).First(&cfgRow).Error; err != nil {
		return nil, fmt.Errorf("上游渠道配置 #%d 不存在: %w", binding.ChannelConfigID, err)
	}
	if cfgRow.Status != 1 {
		return nil, fmt.Errorf("上游渠道配置 #%d 已停用", binding.ChannelConfigID)
	}
	bearerKey, err := decryptIfSealed(keyring, cfgRow.APIKeyEnc)
	if err != nil {
		return nil, err
	}
	proxyBase := strings.TrimRight(strings.TrimSpace(cfgRow.BaseURL), "/")
	if rel := strings.TrimSpace(binding.AssetBasePath); rel != "" {
		if strings.HasPrefix(rel, "http://") || strings.HasPrefix(rel, "https://") {
			proxyBase = strings.TrimRight(rel, "/")
		} else {
			u, uerr := forward.JoinUpstreamURL(proxyBase, rel)
			if uerr != nil {
				return nil, uerr
			}
			proxyBase = strings.TrimRight(u.String(), "/")
		}
	}

	group := conversionGroup{id: strings.TrimSpace(binding.GroupID)}
	bindingID := binding.ID
	createGroup := func(ctx context.Context) (string, error) {
		res, err := callUpstreamAssetAction(ctx, httpClient, proxyBase, bearerKey, "CreateAssetGroup", map[string]any{
			"Name":      fmt.Sprintf("laiai_uar_%d", bindingID),
			"GroupType": "AIGC",
		})
		if err != nil {
			return "", err
		}
		gid := extractResultID(res)
		if gid == "" {
			return "", errors.New("asset group response missing ID")
		}
		return gid, db.WithContext(ctx).Model(&UpstreamAssetBinding{}).Where("id = ?", bindingID).Update("group_id", gid).Error
	}

	return func(gctx context.Context, tgt MediaConvertTarget) (string, error) {
		groupID, err := group.resolve(gctx, "", createGroup)
		if err != nil {
			return "", err
		}
		createReq := map[string]any{
			"URL":       tgt.URL,
			"AssetType": tgt.AssetType,
			"Name":      DeriveCreateAssetName(tgt.URL, tgt.AssetType),
			"GroupId":   groupID,
		}
		res, cerr := callUpstreamAssetAction(gctx, httpClient, proxyBase, bearerKey, "CreateAsset", createReq)
		if cerr != nil && IsGroupNotFoundErr(cerr.Error()) {
			newGID, err := group.resolve(gctx, groupID, createGroup)
			if err != nil {
				return "", err
			}
			createReq["GroupId"] = newGID
			res, cerr = callUpstreamAssetAction(gctx, httpClient, proxyBase, bearerKey, "CreateAsset", createReq)
		}
		if cerr != nil {
			return "", cerr
		}
		aid := extractResultID(res)
		if aid == "" {
			return "", errors.New("上游素材接口响应缺少 Id")
		}
		for round := 0; round < 30; round++ {
			if err := sleepPollRound(gctx, round, pollInterval); err != nil {
				return "", err
			}
			getRes, gerr := callUpstreamAssetAction(gctx, httpClient, proxyBase, bearerKey, "GetAsset", map[string]any{"Id": aid})
			if gerr != nil {
				return "", gerr
			}
			st := extractResultField(getRes, "Status")
			if strings.EqualFold(st, "Active") {
				return aid, nil
			}
			if strings.EqualFold(st, "Failed") {
				return "", fmt.Errorf("素材审核/处理失败: %s", cmp.Or(extractResultField(getRes, "FailReason"), st))
			}
		}
		return "", errors.New("素材状态查询超时")
	}, nil
}

func convertTargetsConcurrent(
	ctx context.Context,
	db *gorm.DB,
	httpClient *http.Client,
	opts ConvertRequestOptions,
	targets []MediaConvertTarget,
	body map[string]any,
	pluginNS string,
	source string,
	createFn func(context.Context, MediaConvertTarget) (string, error),
) ([]string, error) {
	type resolvedItem struct {
		tgt     MediaConvertTarget
		assetID string
		cached  bool
	}
	results := make([]resolvedItem, len(targets))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(8)
	for i, tgt := range targets {
		g.Go(func() error {
			if !isHTTPMediaURL(tgt.URL) {
				return fmt.Errorf("暂不支持非 HTTP(S) 媒体地址转换: %s", tgt.AssetType)
			}
			fp := FetchMetaFingerprint(gctx, httpClient, tgt.URL, opts.AllowPrivateFetch)
			if cachedAID := LookupCachedConvertedAsset(db.WithContext(gctx), opts.OrgID, tgt.URL, pluginNS, source, fp); cachedAID != "" {
				results[i] = resolvedItem{tgt: tgt, assetID: cachedAID, cached: true}
				return nil
			}
			aid, err := createFn(gctx, tgt)
			if err != nil {
				return err
			}
			if err := db.WithContext(gctx).Create(&PluginAsset{
				OrgID:           opts.OrgID,
				UserID:          opts.UserID,
				AssetType:       strings.ToLower(tgt.AssetType),
				Source:          source,
				Status:          "approved",
				FileName:        DeriveCreateAssetName(tgt.URL, tgt.AssetType),
				FileURL:         tgt.URL,
				AssetID:         aid,
				Category:        "转换素材",
				MetaFingerprint: fp,
				PluginNS:        pluginNS,
				StorageProvider: "tos",
			}).Error; err != nil {
				return fmt.Errorf("save converted asset: %w", err)
			}
			results[i] = resolvedItem{tgt: tgt, assetID: aid, cached: false}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	logs := make([]string, 0, len(results))
	for _, r := range results {
		applyConvertedAssetToTarget(body, r.tgt, r.assetID)
		tag := ""
		if r.cached {
			tag = " [命中缓存]"
		}
		logs = append(logs, fmt.Sprintf("[%s] %s ✓ asset://%s%s", r.tgt.AssetType, r.tgt.URL, NormalizeArkAssetID(r.assetID), tag))
	}
	return logs, nil
}

func pollAssetReady(ctx context.Context, client *Client, assetID, mediaURL string, overrideInterval time.Duration) error {
	for round := 0; round < 30; round++ {
		if err := sleepPollRound(ctx, round, overrideInterval); err != nil {
			return err
		}
		st, err := client.GetAsset(ctx, assetID)
		if err != nil {
			return fmt.Errorf("素材状态查询失败(%s): %w", mediaURL, err)
		}
		switch strings.ToLower(strings.TrimSpace(st.Status)) {
		case "active":
			return nil
		case "failed":
			reason := cmp.Or(st.FailReason, st.FailCode, "审核未通过")
			if st.Error != nil {
				reason = cmp.Or(st.Error.Message, st.Error.Code, reason)
			}
			return fmt.Errorf("素材处理失败(%s): %s", mediaURL, reason)
		}
	}
	return fmt.Errorf("素材处理超时(%s)", mediaURL)
}

func sleepPollRound(ctx context.Context, round int, override time.Duration) error {
	d := override
	if d <= 0 {
		maxMS := int64(3000)
		if round > 0 {
			maxMS = 5000
		}
		d = time.Duration(cryptoRandRange(2000, maxMS)) * time.Millisecond
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// CleanupExpiredAssets 按保留小时数分批清理过期转换/代理素材，云端引用归零后串行调用 DeleteAsset。
func CleanupExpiredAssets(
	ctx context.Context,
	db *gorm.DB,
	keyring *crypto.Keyring,
	retentionHours int,
	httpClient *http.Client,
) (int, error) {
	if retentionHours <= 0 {
		retentionHours = 168 // 默认 7 天（匹配 Volc Asset Retention）
	}
	cutoff := time.Now().UTC().Add(-time.Duration(retentionHours) * time.Hour)
	var expired []PluginAsset
	if err := db.WithContext(ctx).
		Where("source IN ? AND created_at < ?", []string{SourceRelayConvert, SourceAPIProxy, SourceUpstreamRelay}, cutoff).
		Order("id ASC").
		Limit(200).
		Find(&expired).Error; err != nil {
		return 0, err
	}
	if len(expired) == 0 {
		return 0, nil
	}

	deleted := 0
	for _, row := range expired {
		var otherRefs int64
		_ = db.WithContext(ctx).Model(&PluginAsset{}).
			Where("asset_id = ? AND plugin_ns = ? AND id <> ?", row.AssetID, row.PluginNS, row.ID).
			Count(&otherRefs).Error
		if otherRefs == 0 && row.AssetID != "" && (row.PluginNS == PluginAssetManager || row.PluginNS == PluginAssetManagerIntl) {
			cfgMap := loadExtSettingMap(db, "plugin_cfg:"+row.PluginNS+":")
			if ak, skRaw := strings.TrimSpace(cfgMap["volc_access_key"]), strings.TrimSpace(cfgMap["volc_secret_key"]); ak != "" && skRaw != "" {
				if sk, derr := decryptIfSealed(keyring, skRaw); derr == nil {
					cl := NewClient(ClientConfig{
						AccessKey:   ak,
						SecretKey:   sk,
						Region:      cmp.Or(strings.TrimSpace(cfgMap["volc_region"]), DefaultVolcRegion),
						ProjectName: cmp.Or(strings.TrimSpace(cfgMap["volc_project_name"]), "default"),
					}, httpClient)
					_ = RunDeleteAssetPaced(ctx, DefaultDeleteAssetGap, func(dctx context.Context) {
						_ = cl.DeleteAsset(dctx, row.AssetID)
					})
				}
			}
		}
		if err := db.WithContext(ctx).Delete(&PluginAsset{}, row.ID).Error; err == nil {
			deleted++
		}
	}
	return deleted, nil
}

// EnsureOwnedOrClaimAssetOrGroup 实现多租户资产与素材组归属隔离校验（非 admin 不可越权操作他人素材）。
func EnsureOwnedOrClaimAssetOrGroup(db *gorm.DB, kind, id string, orgID, userID uint64, pluginNS string) error {
	cleanID := strings.TrimSpace(id)
	if cleanID == "" {
		return fmt.Errorf("缺少必需参数 %s ID", kind)
	}
	if kind == "asset" {
		var ownedCount int64
		_ = db.Model(&PluginAsset{}).Where("asset_id = ? AND org_id = ? AND plugin_ns = ?", cleanID, orgID, pluginNS).Count(&ownedCount).Error
		if ownedCount > 0 {
			return nil
		}
		var anyCount int64
		_ = db.Model(&PluginAsset{}).Where("asset_id = ? AND plugin_ns = ?", cleanID, pluginNS).Count(&anyCount).Error
		if anyCount > 0 {
			return errors.New("无权操作此素材，仅可操作本组织创建的素材")
		}
		_ = db.Create(&PluginAsset{
			OrgID:           orgID,
			UserID:          userID,
			AssetType:       "unknown",
			Source:          SourceAPIProxy,
			Status:          "approved",
			FileName:        "unknown",
			FileURL:         "",
			AssetID:         cleanID,
			Category:        "API素材",
			PluginNS:        pluginNS,
			StorageProvider: "tos",
		}).Error
		return nil
	}

	var ownedCount int64
	_ = db.Model(&PluginAssetGroup{}).Where("group_id = ? AND org_id = ? AND plugin_ns = ?", cleanID, orgID, pluginNS).Count(&ownedCount).Error
	if ownedCount > 0 {
		return nil
	}
	var anyCount int64
	_ = db.Model(&PluginAssetGroup{}).Where("group_id = ? AND plugin_ns = ?", cleanID, pluginNS).Count(&anyCount).Error
	if anyCount > 0 {
		return errors.New("无权操作此素材组，仅可操作本组织创建的素材组")
	}
	_ = db.Create(&PluginAssetGroup{
		OrgID:    orgID,
		UserID:   userID,
		GroupID:  cleanID,
		Name:     "历史数据",
		PluginNS: pluginNS,
	}).Error
	return nil
}

// ParseUintOrZero safely parses optional numeric IDs.
func ParseUintOrZero(raw string) uint64 {
	v, _ := strconv.ParseUint(strings.TrimSpace(raw), 10, 64)
	return v
}
