package video

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/laiai/laiai-next/core/internal/identity"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const arkRateLimitURL = "https://ark.cn-beijing.volcengineapi.com/?Action=ListModelRateLimit&Version=2024-01-01"
const arkRateLimitHost = "ark.cn-beijing.volcengineapi.com"

var foundationModelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
var officialRequestIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// ProviderModelQuota is keyed by the main-account reference, never by
// provider scope, project, channel, key, or tenant. Nullable 4K fields mean
// the official API did not report a separate 4K quota.
type ProviderModelQuota struct {
	AccountRef                   string     `gorm:"primaryKey;column:account_ref" json:"account_ref"`
	FoundationModel              string     `gorm:"primaryKey;column:foundation_model" json:"foundation_model"`
	OfficialCreateTaskRPM        *int       `gorm:"column:official_create_task_rpm" json:"official_create_task_rpm"`
	OfficialConcurrentRequests   *int       `gorm:"column:official_concurrent_requests" json:"official_concurrent_requests"`
	OfficialCreateTaskRPM4K      *int       `gorm:"column:official_create_task_rpm_4k" json:"official_create_task_rpm_4k"`
	OfficialConcurrentRequests4K *int       `gorm:"column:official_concurrent_requests_4k" json:"official_concurrent_requests_4k"`
	ManualCreateTaskRPM          *int       `gorm:"column:manual_create_task_rpm" json:"manual_create_task_rpm"`
	ManualConcurrentRequests     *int       `gorm:"column:manual_concurrent_requests" json:"manual_concurrent_requests"`
	ManualCreateTaskRPM4K        *int       `gorm:"column:manual_create_task_rpm_4k" json:"manual_create_task_rpm_4k"`
	ManualConcurrentRequests4K   *int       `gorm:"column:manual_concurrent_requests_4k" json:"manual_concurrent_requests_4k"`
	OfficialObservedAt           *time.Time `gorm:"column:official_observed_at" json:"official_observed_at"`
	ManualUpdatedAt              *time.Time `gorm:"column:manual_updated_at" json:"manual_updated_at"`
	ManualUpdatedBy              *uint64    `gorm:"column:manual_updated_by" json:"manual_updated_by"`
	ManualVersion                uint64     `gorm:"column:manual_version" json:"manual_version"`
	OfficialRequestID            string     `gorm:"column:official_request_id" json:"official_request_id"`
}

func (ProviderModelQuota) TableName() string { return "video_provider_model_quotas" }

type ProviderModelQuotaEffective struct {
	CreateTaskRPM        *int `json:"create_task_rpm"`
	ConcurrentRequests   *int `json:"concurrent_requests"`
	CreateTaskRPM4K      *int `json:"create_task_rpm_4k"`
	ConcurrentRequests4K *int `json:"concurrent_requests_4k"`
}

func minOptionalQuota(a, b *int) *int {
	if a == nil {
		return b
	}
	if b == nil || *a <= *b {
		return a
	}
	return b
}

// Effective is the conservative minimum of official and manual ceilings.
// A stale official snapshot remains in force until a newer successful sync;
// sync errors never increase the effective quota.
func (q ProviderModelQuota) Effective() ProviderModelQuotaEffective {
	return ProviderModelQuotaEffective{
		CreateTaskRPM:        minOptionalQuota(q.OfficialCreateTaskRPM, q.ManualCreateTaskRPM),
		ConcurrentRequests:   minOptionalQuota(q.OfficialConcurrentRequests, q.ManualConcurrentRequests),
		CreateTaskRPM4K:      minOptionalQuota(q.OfficialCreateTaskRPM4K, q.ManualCreateTaskRPM4K),
		ConcurrentRequests4K: minOptionalQuota(q.OfficialConcurrentRequests4K, q.ManualConcurrentRequests4K),
	}
}

// A stale snapshot remains a conservative ceiling; it is not represented as
// a fresh observation and must never be silently replaced with an increase.
func (q ProviderModelQuota) OfficialStale(at time.Time) bool {
	return q.OfficialObservedAt == nil || q.OfficialObservedAt.After(at) || at.Sub(*q.OfficialObservedAt) > 30*time.Minute
}

type ProviderModelManualQuota struct {
	AccountRef           string `json:"account_ref"`
	FoundationModel      string `json:"foundation_model"`
	ExpectedVersion      uint64 `json:"expected_version"`
	CreateTaskRPM        *int   `json:"create_task_rpm"`
	ConcurrentRequests   *int   `json:"concurrent_requests"`
	CreateTaskRPM4K      *int   `json:"create_task_rpm_4k"`
	ConcurrentRequests4K *int   `json:"concurrent_requests_4k"`
}

func validProviderQuotaNumber(v *int) bool { return v == nil || *v >= 0 && *v <= 1000000 }

func validProviderQuotaKey(account, model string) bool {
	return assetID(account) && foundationModelPattern.MatchString(model)
}

func (s *Service) ReadProviderModelQuota(ctx context.Context, actor *identity.User, account, model string) (ProviderModelQuota, error) {
	var q ProviderModelQuota
	if err := assetAdmin(actor); err != nil {
		return q, err
	}
	if !validProviderQuotaKey(account, model) {
		return q, ErrInvalid
	}
	err := s.DB.WithContext(ctx).Where("account_ref = ? AND foundation_model = ?", account, model).Take(&q).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return q, ErrNotFound
	}
	return q, err
}

func (s *Service) SetProviderModelManualQuota(ctx context.Context, actor *identity.User, in ProviderModelManualQuota) (ProviderModelQuota, error) {
	var out ProviderModelQuota
	if err := assetAdmin(actor); err != nil {
		return out, err
	}
	if !validProviderQuotaKey(in.AccountRef, in.FoundationModel) || !validProviderQuotaNumber(in.CreateTaskRPM) || !validProviderQuotaNumber(in.ConcurrentRequests) || !validProviderQuotaNumber(in.CreateTaskRPM4K) || !validProviderQuotaNumber(in.ConcurrentRequests4K) || in.ExpectedVersion == ^uint64(0) {
		return out, ErrInvalid
	}
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var provider ProviderScope
		if err := tx.Where("account_ref = ? AND region = ?", in.AccountRef, "cn-beijing").Take(&provider).Error; err != nil {
			return ErrCapability
		}
		q := ProviderModelQuota{AccountRef: in.AccountRef, FoundationModel: in.FoundationModel}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&q).Error; err != nil {
			return err
		}
		if err := locked(tx).Where("account_ref = ? AND foundation_model = ?", in.AccountRef, in.FoundationModel).Take(&q).Error; err != nil {
			return err
		}
		if q.ManualVersion != in.ExpectedVersion {
			return ErrConflict
		}
		now := s.Now().UTC().Truncate(time.Millisecond)
		updates := map[string]any{
			"manual_create_task_rpm": in.CreateTaskRPM, "manual_concurrent_requests": in.ConcurrentRequests,
			"manual_create_task_rpm_4k": in.CreateTaskRPM4K, "manual_concurrent_requests_4k": in.ConcurrentRequests4K,
			"manual_updated_at": now, "manual_updated_by": actor.ID, "manual_version": q.ManualVersion + 1,
		}
		result := tx.Model(&ProviderModelQuota{}).Where("account_ref = ? AND foundation_model = ? AND manual_version = ?", in.AccountRef, in.FoundationModel, q.ManualVersion).Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrConflict
		}
		if err := tx.Where("account_ref = ? AND foundation_model = ?", in.AccountRef, in.FoundationModel).Take(&out).Error; err != nil {
			return err
		}
		return s.assetAuditTx(tx, 0, "provider_model_quota", in.AccountRef+":"+in.FoundationModel, "manual_ceiling_changed", "", strconv.FormatUint(actor.ID, 10))
	})
	return out, err
}

type arkManagementCredential struct {
	Account, Region, Project, AccessKey, SecretKey, SessionToken string
}

type officialProviderLimit struct {
	FoundationModel                       string
	CreateTaskRPM, ConcurrentRequests     int
	CreateTaskRPM4K, ConcurrentRequests4K *int
}

func hmacSHA256(key []byte, value string) []byte {
	h := hmac.New(sha256.New, key)
	_, _ = h.Write([]byte(value))
	return h.Sum(nil)
}

// arkRateLimitAuthorization implements the documented HMAC-SHA256 signature
// for the fixed ListModelRateLimit management endpoint. The URL, region,
// service and signed header set are intentionally not caller configurable.
func arkRateLimitAuthorization(cred arkManagementCredential, body []byte, at time.Time) (string, string, string) {
	xDate := at.UTC().Format("20060102T150405Z")
	date := xDate[:8]
	contentHash := sha256.Sum256(body)
	contentSHA := hex.EncodeToString(contentHash[:])
	query := url.Values{"Action": {"ListModelRateLimit"}, "Version": {"2024-01-01"}}.Encode()
	headers := "content-type:application/json\nhost:" + arkRateLimitHost + "\nx-content-sha256:" + contentSHA + "\nx-date:" + xDate + "\n"
	signed := "content-type;host;x-content-sha256;x-date"
	if cred.SessionToken != "" {
		headers += "x-security-token:" + cred.SessionToken + "\n"
		signed += ";x-security-token"
	}
	canonical := "POST\n/\n" + query + "\n" + headers + "\n" + signed + "\n" + contentSHA
	canonicalHash := sha256.Sum256([]byte(canonical))
	scope := date + "/cn-beijing/ark/request"
	toSign := "HMAC-SHA256\n" + xDate + "\n" + scope + "\n" + hex.EncodeToString(canonicalHash[:])
	key := hmacSHA256([]byte(cred.SecretKey), date)
	key = hmacSHA256(key, "cn-beijing")
	key = hmacSHA256(key, "ark")
	key = hmacSHA256(key, "request")
	sig := hex.EncodeToString(hmacSHA256(key, toSign))
	authorization := "HMAC-SHA256 Credential=" + cred.AccessKey + "/" + scope + ", SignedHeaders=" + signed + ", Signature=" + sig
	return authorization, xDate, contentSHA
}

func fetchArkModelRateLimits(ctx context.Context, client *http.Client, cred arkManagementCredential, names []string, at time.Time) ([]officialProviderLimit, string, error) {
	body, err := json.Marshal(struct{ FoundationModelNames []string }{names})
	if err != nil {
		return nil, "", ErrInvalid
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, arkRateLimitURL, bytes.NewReader(body))
	if err != nil {
		return nil, "", ErrUnavailable
	}
	authorization, xDate, contentSHA := arkRateLimitAuthorization(cred, body, at)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Date", xDate)
	request.Header.Set("X-Content-Sha256", contentSHA)
	request.Header.Set("Authorization", authorization)
	if cred.SessionToken != "" {
		request.Header.Set("X-Security-Token", cred.SessionToken)
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, "", ErrUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, "", ErrUnavailable
	}
	var payload struct {
		ResponseMetadata struct {
			RequestID string `json:"RequestId"`
			Error     *struct {
				Code string `json:"Code"`
			} `json:"Error"`
		} `json:"ResponseMetadata"`
		Result struct {
			Items []struct {
				FoundationModelName        string `json:"FoundationModelName"`
				ContentGenerationRateLimit *struct {
					CreateTaskRPM        *int `json:"CreateTaskRpm"`
					ConcurrentRequests   *int `json:"ConcurrentRequests"`
					CreateTaskRPM4K      *int `json:"CreateTaskRpmFor4K"`
					ConcurrentRequests4K *int `json:"ConcurrentRequestsFor4K"`
				} `json:"ContentGenerationRateLimit"`
			} `json:"Items"`
		} `json:"Result"`
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20+1))
	if err != nil || len(raw) > 1<<20 {
		return nil, "", ErrUnavailable
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if decoder.Decode(&payload) != nil || payload.ResponseMetadata.Error != nil || !officialRequestIDPattern.MatchString(payload.ResponseMetadata.RequestID) {
		return nil, "", ErrUnavailable
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return nil, "", ErrUnavailable
	}
	wanted := make(map[string]bool, len(names))
	for _, name := range names {
		wanted[name] = false
	}
	limits := make([]officialProviderLimit, 0, len(names))
	for _, item := range payload.Result.Items {
		if _, ok := wanted[item.FoundationModelName]; !ok || wanted[item.FoundationModelName] || item.ContentGenerationRateLimit == nil {
			return nil, "", ErrUnavailable
		}
		v := item.ContentGenerationRateLimit
		if v.CreateTaskRPM == nil || v.ConcurrentRequests == nil || !validProviderQuotaNumber(v.CreateTaskRPM) || !validProviderQuotaNumber(v.ConcurrentRequests) || !validProviderQuotaNumber(v.CreateTaskRPM4K) || !validProviderQuotaNumber(v.ConcurrentRequests4K) {
			return nil, "", ErrUnavailable
		}
		limits = append(limits, officialProviderLimit{FoundationModel: item.FoundationModelName, CreateTaskRPM: *v.CreateTaskRPM, ConcurrentRequests: *v.ConcurrentRequests, CreateTaskRPM4K: v.CreateTaskRPM4K, ConcurrentRequests4K: v.ConcurrentRequests4K})
		wanted[item.FoundationModelName] = true
	}
	for _, found := range wanted {
		if !found {
			return nil, "", ErrUnavailable
		}
	}
	return limits, payload.ResponseMetadata.RequestID, nil
}

func validateModelNames(names []string) ([]string, error) {
	if len(names) == 0 || len(names) > 32 {
		return nil, ErrInvalid
	}
	seen := map[string]bool{}
	for _, name := range names {
		if !foundationModelPattern.MatchString(name) || seen[name] {
			return nil, ErrInvalid
		}
		seen[name] = true
	}
	ordered := append([]string(nil), names...)
	sort.Strings(ordered)
	return ordered, nil
}

// SyncProviderModelRateLimits uses encrypted management AK/SK, not an Ark
// Bearer API key. It validates the complete response before changing any row.
// The returned rows expose no credential or provider response body.
func (s *Service) SyncProviderModelRateLimits(ctx context.Context, actor *identity.User, scopeID uint64, names []string) ([]ProviderModelQuota, error) {
	if err := assetAdmin(actor); err != nil {
		return nil, err
	}
	return s.syncProviderModelRateLimits(ctx, scopeID, names)
}

func (s *Service) syncProviderModelRateLimits(ctx context.Context, scopeID uint64, names []string) ([]ProviderModelQuota, error) {
	ordered, err := validateModelNames(names)
	if err != nil || scopeID == 0 {
		return nil, ErrInvalid
	}
	var provider ProviderScope
	var cfg AssetScopeConfig
	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("id = ?", scopeID).Take(&provider).Error; err != nil || !validAssetProvider(provider) {
			return ErrCapability
		}
		if err := tx.Where("provider_scope_id = ?", scopeID).Order("version DESC").Take(&cfg).Error; err != nil || !cfg.Enabled || cfg.CredentialCipher == "" || cfg.AccountRef != provider.AccountRef || cfg.Region != provider.Region || cfg.ProjectName != provider.ProjectName {
			return ErrCapability
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	var cred arkManagementCredential
	if err := s.assetOpen("management", 0, strconv.FormatUint(scopeID, 10), cfg.Version, cfg.CredentialCipher, &cred); err != nil || cred.Account != provider.AccountRef || cred.Region != "cn-beijing" || cred.Project != provider.ProjectName || cred.AccessKey == "" || cred.SecretKey == "" || strings.ContainsAny(cred.AccessKey+cred.SecretKey+cred.SessionToken, "\r\n\x00") {
		return nil, ErrUnavailable
	}
	client := &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	observed := s.Now().UTC().Truncate(time.Millisecond)
	limits, requestID, err := fetchArkModelRateLimits(ctx, client, cred, ordered, observed)
	if err != nil {
		return nil, err
	}
	var saved []ProviderModelQuota
	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, item := range limits {
			q := ProviderModelQuota{AccountRef: provider.AccountRef, FoundationModel: item.FoundationModel}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&q).Error; err != nil {
				return err
			}
			if err := locked(tx).Where("account_ref = ? AND foundation_model = ?", q.AccountRef, q.FoundationModel).Take(&q).Error; err != nil {
				return err
			}
			if q.OfficialObservedAt == nil || !q.OfficialObservedAt.After(observed) {
				result := tx.Model(&ProviderModelQuota{}).Where("account_ref = ? AND foundation_model = ?", q.AccountRef, q.FoundationModel).Updates(map[string]any{
					"official_create_task_rpm": item.CreateTaskRPM, "official_concurrent_requests": item.ConcurrentRequests,
					"official_create_task_rpm_4k": item.CreateTaskRPM4K, "official_concurrent_requests_4k": item.ConcurrentRequests4K,
					"official_observed_at": observed, "official_request_id": requestID,
				})
				if result.Error != nil || result.RowsAffected != 1 {
					return ErrUnavailable
				}
			}
			if err := tx.Where("account_ref = ? AND foundation_model = ?", q.AccountRef, q.FoundationModel).Take(&q).Error; err != nil {
				return err
			}
			saved = append(saved, q)
		}
		return nil
	})
	return saved, err
}

// RefreshKnownProviderModelRateLimits is called by the trusted background job,
// never directly by an HTTP route. It only refreshes models initialized by an admin and
// selects one enabled management scope per main account. A failed batch
// leaves that batch's prior snapshots intact and does not stop other accounts.
func (s *Service) RefreshKnownProviderModelRateLimits(ctx context.Context) map[string]error {
	failures := map[string]error{}
	var rows []ProviderModelQuota
	if err := s.DB.WithContext(ctx).Select("account_ref, foundation_model").Order("account_ref, foundation_model").Find(&rows).Error; err != nil {
		failures["database"] = err
		return failures
	}
	byAccount := map[string][]string{}
	for _, row := range rows {
		byAccount[row.AccountRef] = append(byAccount[row.AccountRef], row.FoundationModel)
	}
	accounts := make([]string, 0, len(byAccount))
	for account := range byAccount {
		accounts = append(accounts, account)
	}
	sort.Strings(accounts)
	for _, account := range accounts {
		var scopes []ProviderScope
		if err := s.DB.WithContext(ctx).Where("account_ref = ? AND region = ? AND enabled = ?", account, "cn-beijing", true).Order("id").Find(&scopes).Error; err != nil {
			failures[account] = err
			continue
		}
		if len(scopes) == 0 {
			failures[account] = ErrCapability
			continue
		}
		var selected uint64
		for _, scope := range scopes {
			var cfg AssetScopeConfig
			if err := s.DB.WithContext(ctx).Where("provider_scope_id = ?", scope.ID).Order("version DESC").Take(&cfg).Error; err == nil && cfg.Enabled && cfg.CredentialCipher != "" && cfg.AccountRef == account && cfg.Region == "cn-beijing" && cfg.ProjectName == scope.ProjectName {
				selected = scope.ID
				break
			}
		}
		if selected == 0 {
			failures[account] = ErrCapability
			continue
		}
		// A failed selected scope may indicate revoked credentials. Do not
		// silently switch identity: an operator must select the replacement.
		for start := 0; start < len(byAccount[account]); start += 32 {
			end := start + 32
			if end > len(byAccount[account]) {
				end = len(byAccount[account])
			}
			if _, err := s.syncProviderModelRateLimits(ctx, selected, byAccount[account][start:end]); err != nil {
				failures[account] = err
				break
			}
		}
	}
	return failures
}

// Keep signing and sync errors devoid of AK/SK and provider response bodies.
func (q ProviderModelQuota) String() string {
	return fmt.Sprintf("provider quota %s/%s", q.AccountRef, q.FoundationModel)
}
