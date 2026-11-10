package video

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type quotaRoundTrip func(*http.Request) (*http.Response, error)

func (f quotaRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func quotaResponse(body string) *http.Response {
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

func TestArkModelRateLimitSignedManagementRequestAndCompleteParse(t *testing.T) {
	at := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	cred := arkManagementCredential{AccessKey: "AKLfixture", SecretKey: "fixture-secret", Account: "account"}
	client := &http.Client{Transport: quotaRoundTrip(func(r *http.Request) (*http.Response, error) {
		if r.Method != "POST" || r.URL.String() != arkRateLimitURL || r.Header.Get("X-Date") != "20261109T120000Z" || !strings.Contains(r.Header.Get("Authorization"), "Credential=AKLfixture/20261109/cn-beijing/ark/request") || strings.Contains(r.Header.Get("Authorization"), "Bearer") {
			t.Fatalf("unexpected management request: %s %s %q", r.Method, r.URL, r.Header.Get("Authorization"))
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		hash := sha256.Sum256(body)
		if r.Header.Get("X-Content-Sha256") != hex.EncodeToString(hash[:]) || string(body) != `{"FoundationModelNames":["doubao-seedance-2-0"]}` {
			t.Fatalf("signed body or hash mismatch")
		}
		return quotaResponse(`{"ResponseMetadata":{"RequestId":"official-request"},"Result":{"Items":[{"FoundationModelName":"doubao-seedance-2-0","ContentGenerationRateLimit":{"CreateTaskRpm":60,"ConcurrentRequests":8,"CreateTaskRpmFor4K":20,"ConcurrentRequestsFor4K":2}}]}}`), nil
	})}
	got, requestID, err := fetchArkModelRateLimits(context.Background(), client, cred, []string{"doubao-seedance-2-0"}, at)
	if err != nil || requestID != "official-request" || len(got) != 1 || got[0].CreateTaskRPM != 60 || got[0].ConcurrentRequests != 8 || got[0].CreateTaskRPM4K == nil || *got[0].CreateTaskRPM4K != 20 {
		t.Fatalf("official limits = %#v, %q, %v", got, requestID, err)
	}
}

func TestArkModelRateLimitRejectsIncompleteOrDuplicateResponse(t *testing.T) {
	for _, body := range []string{
		`{"Result":{"Items":[]}}`,
		`{"Result":{"Items":[{"FoundationModelName":"doubao-seedance-2-0","ContentGenerationRateLimit":{"CreateTaskRpm":60}}]}}`,
		`{"Result":{"Items":[{"FoundationModelName":"doubao-seedance-2-0","ContentGenerationRateLimit":{"CreateTaskRpm":60,"ConcurrentRequests":8}},{"FoundationModelName":"doubao-seedance-2-0","ContentGenerationRateLimit":{"CreateTaskRpm":60,"ConcurrentRequests":8}}]}}`,
		`{"ResponseMetadata":{"Error":{"Code":"AccessDenied"}},"Result":{"Items":[{"FoundationModelName":"doubao-seedance-2-0","ContentGenerationRateLimit":{"CreateTaskRpm":60,"ConcurrentRequests":8}}]}}`,
	} {
		client := &http.Client{Transport: quotaRoundTrip(func(*http.Request) (*http.Response, error) { return quotaResponse(body), nil })}
		if _, _, err := fetchArkModelRateLimits(context.Background(), client, arkManagementCredential{AccessKey: "a", SecretKey: "b"}, []string{"doubao-seedance-2-0"}, time.Now()); err == nil {
			t.Fatalf("accepted incomplete response %q", body)
		}
	}
}

func TestProviderModelManualCeilingIsAccountSharedAndVersioned(t *testing.T) {
	db := catalogLocalDB(t)
	providerQuotaLocalSchema(t, db)
	must(t, db.Exec(assetProjectSchema).Error)
	for _, scope := range []ProviderScope{
		{ID: 1, ScopeKey: "one", AccountRef: "mainaccount", Region: "cn-beijing", ProjectName: "default", Enabled: true},
		{ID: 2, ScopeKey: "two", AccountRef: "mainaccount", Region: "cn-beijing", ProjectName: "other", Enabled: true},
	} {
		must(t, db.Create(&scope).Error)
	}
	at := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	s := &Service{DB: db, Now: func() time.Time { return at }}
	manualRPM, manualConcurrency := 40, 6
	in := ProviderModelManualQuota{AccountRef: "mainaccount", FoundationModel: "doubao-seedance-2-0", CreateTaskRPM: &manualRPM, ConcurrentRequests: &manualConcurrency}
	q, err := s.SetProviderModelManualQuota(context.Background(), assetTestActor(), in)
	if err != nil || q.ManualVersion != 1 || q.Effective().CreateTaskRPM == nil || *q.Effective().CreateTaskRPM != 40 {
		t.Fatalf("manual ceiling = %+v, %v", q, err)
	}
	if _, err := s.SetProviderModelManualQuota(context.Background(), assetTestActor(), in); err != ErrConflict {
		t.Fatalf("stale manual version = %v", err)
	}
	var count int64
	must(t, db.Model(&ProviderModelQuota{}).Where("account_ref = ?", "mainaccount").Count(&count).Error)
	if count != 1 {
		t.Fatalf("same account across scopes yielded %d quota rows", count)
	}
	officialRPM, officialConcurrency := 60, 4
	q.OfficialCreateTaskRPM, q.OfficialConcurrentRequests = &officialRPM, &officialConcurrency
	if got := q.Effective(); got.CreateTaskRPM == nil || *got.CreateTaskRPM != 40 || got.ConcurrentRequests == nil || *got.ConcurrentRequests != 4 {
		t.Fatalf("effective quota = %+v", got)
	}
}
