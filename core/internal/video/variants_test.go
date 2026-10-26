package video

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/relay"
	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// Financial and provider replies below are synthetic local fixtures. They test
// the real HTTP/DB/worker path and do not assert paid account acceptance.
func variantVideoFixture(t *testing.T) (*fixture, ProviderScope, AssetMediaRegistration) {
	t.Helper()
	f := mysqlFixture(t)
	scope, baseCap := assetTestSetup(t, f)
	var base ModelConfig
	must(t, f.s.DB.Where("public_model = ?", ark.Model20).First(&base).Error)
	must(t, f.s.DB.Model(&relay.Channel{}).Where("id = ?", base.ChannelID).Update("models", strings.Join([]string{ark.Model20, ark.Model20Fast, ark.Model20Mini}, ",")).Error)
	for _, model := range []string{ark.Model20Fast, ark.Model20Mini} {
		cap := baseCap
		cap.PublicModel, cap.Version = model, "variant-fixture-v1"
		cap.UpstreamModel = "doubao-seedance-2-0-fast-260128"
		if model == ark.Model20Mini {
			cap.UpstreamModel = "doubao-seedance-2-0-mini-260615"
		}
		cap.Resolutions, cap.DefaultResolution, cap.DefaultDuration = []string{"480p", "720p"}, "480p", 4
		raw, err := json.Marshal(cap)
		must(t, err)
		cfg := base
		cfg.PublicModel, cfg.Version, cfg.CapabilityJSON = model, "variant-fixture-config", string(raw)
		cfg.BoundsJSON = `{"480p/16:9":{"width":864,"height":496},"720p/16:9":{"width":1280,"height":720}}`
		must(t, f.s.DB.Save(&cfg).Error)
		minimumVersion := model + "-" + base.PricingVersion
		minimum := videobilling.MinimumRuleSet{Model: model, Version: minimumVersion, SourceURL: "https://docs.volcengine.com/fixture-only", EvidenceHash: strings.Repeat("b", 64), ObservedAt: f.now, VerifiedAt: f.now, VerifiedBy: 77}
		cells := []videobilling.MinimumRuleCell{{Resolution: "480p", Ratio: "16:9", OutputDurationMS: 4000, FPS: 24, MinimumTokens: 70308}, {Resolution: "720p", Ratio: "16:9", OutputDurationMS: 4000, FPS: 24, MinimumTokens: 151200}}
		must(t, f.s.DB.Transaction(func(tx *gorm.DB) error { _, e := videobilling.PublishMinimumTx(tx, minimum, cells); return e }))
		for _, resolution := range cap.Resolutions {
			for _, input := range []bool{false, true} {
				cny := int64(37)
				if input {
					cny = 22
				}
				if model == ark.Model20Mini {
					cny = 23
					if input {
						cny = 14
					}
				}
				id, err := randomID("")
				must(t, err)
				price := videobilling.PricingRule{RuleID: id, Version: cfg.PricingVersion, PricingKey: videobilling.PricingKey{ProviderScopeID: scope.ID, Model: model, Mode: "generate", Resolution: resolution, InputVideo: input, ServiceTier: "default"}, RetailUSDPer1M: decimal.NewFromInt(cny).Mul(decimal.RequireFromString("0.14")), SupplierCNYPer1M: decimal.NewFromInt(cny), RiskMultiplier: decimal.NewFromInt(4), FormulaVersion: "fixture-formula", SourceHash: strings.Repeat("a", 64), EffectiveFrom: f.now.Add(-time.Hour), Enabled: true}
				if input {
					price.MinimumRuleVersion = minimumVersion
				}
				must(t, f.s.DB.Create(&price).Error)
			}
		}
	}
	media := assetTestMedia(f)
	media.DurationMS = 2000
	must(t, f.s.RegisterAssetMedia(context.Background(), assetTestActor(), media))
	return f, scope, media
}

func variantRouter(f *fixture) *gin.Engine {
	r := gin.New()
	r.POST("/v1/video/generations", f.s.LegacyCreate(func(c *gin.Context) { c.Status(500) }))
	r.POST("/v1/video/estimates", f.s.Middleware(), f.s.HandleEstimate)
	r.GET("/v1/video/generations/:id", f.s.Middleware(), f.s.HandleGet)
	return r
}

func variantHTTP(f *fixture, r http.Handler, method, path, key, raw string) *httptest.ResponseRecorder {
	q := httptest.NewRequest(method, path, strings.NewReader(raw))
	q.Header.Set("Authorization", "Bearer "+f.plain)
	if key != "" {
		q.Header.Set("Idempotency-Key", key)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, q)
	return w
}

func variantInput(model, resolution, media string) ark.CreateRequest {
	duration := 4
	in := ark.CreateRequest{Model: model, Resolution: &resolution, Duration: &duration, Content: []ark.ContentItem{{Type: "text", Text: "local variant fixture prompt"}}}
	if media != "" {
		in.Content = append(in.Content, ark.ContentItem{Type: "video_url", VideoURL: &ark.MediaReference{URL: media}, Role: "reference_video"})
	}
	return in
}

func TestMySQLVariantPublicEstimateCreateDispatchSettleAndLogs(t *testing.T) {
	for _, model := range []string{ark.Model20Fast, ark.Model20Mini} {
		for _, resolution := range []string{"480p", "720p"} {
			for _, reference := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/reference=%t", model, resolution, reference), func(t *testing.T) {
					f, _, media := variantVideoFixture(t)
					router := variantRouter(f)
					mediaURL := ""
					if reference {
						mediaURL = media.ClientURL
					}
					in := variantInput(model, resolution, mediaURL)
					raw, err := json.Marshal(in)
					must(t, err)
					estimate := variantHTTP(f, router, "POST", "/v1/video/estimates", "", string(raw))
					if estimate.Code != 200 {
						t.Fatalf("estimate status %d", estimate.Code)
					}
					var quoted struct {
						MaximumReservedTokens int64  `json:"maximum_reserved_tokens"`
						ReservedAmount        string `json:"reserved_amount"`
					}
					must(t, json.Unmarshal(estimate.Body.Bytes(), &quoted))
					create := variantHTTP(f, router, "POST", "/v1/video/generations", "variant-http-create", string(raw))
					if create.Code != 202 {
						t.Fatalf("create status %d", create.Code)
					}
					var created TaskView
					must(t, json.Unmarshal(create.Body.Bytes(), &created))
					rid := create.Header().Get("X-Request-Id")
					var snapshot videobilling.Snapshot
					must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, created.ID).First(&snapshot).Error)
					if snapshot.Model != model || snapshot.EstimatedTokens != quoted.MaximumReservedTokens || snapshot.HoldUSD.StringFixed(8) != quoted.ReservedAmount {
						t.Fatal("HTTP estimate and frozen snapshot differ")
					}
					var metering videobilling.MeteringInput
					must(t, json.Unmarshal([]byte(snapshot.MeteringJSON), &metering))
					if metering.InputVideo != reference || (reference && (metering.InputDurationMS != 2000 || metering.MinimumTokens == 0 || metering.MinimumRuleSetID == 0)) || (!reference && metering.MinimumTokens != 0) {
						t.Fatal("reference evidence not bound to reservation")
					}
					actual := snapshot.EstimatedTokens/4 - 17
					var state State
					must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, created.ID).First(&state).Error)
					upstreamID := "up_" + created.ID
					f.response = func(q *http.Request) (*http.Response, error) {
						if q.Method == "POST" {
							var received ark.CreateRequest
							e := json.NewDecoder(q.Body).Decode(&received)
							if e != nil {
								return nil, e
							}
							if received.Model != state.UpstreamModel || !ark.ValidUpstreamModel(model, received.Model) || received.Resolution == nil || *received.Resolution != resolution {
								return nil, errors.New("wrong variant upstream request")
							}
							if reference && (len(received.Content) != 2 || received.Content[1].VideoURL.URL != media.FetchURL) {
								return nil, errors.New("verified reference binding lost")
							}
							return reply(200, `{"id":"`+upstreamID+`"}`), nil
						}
						return reply(200, fmt.Sprintf(`{"id":%q,"model":%q,"status":"succeeded","usage":{"completion_tokens":%d,"total_tokens":%d}}`, upstreamID, state.UpstreamModel, actual, actual)), nil
					}
					f.dispatch(t, created.ID)
					f.dispatch(t, created.ID)
					must(t, f.s.pollTask(context.Background(), f.key.OrgID, created.ID))
					public := variantHTTP(f, router, "GET", "/v1/video/generations/"+created.ID, "", "")
					if public.Code != 200 {
						t.Fatalf("get status %d", public.Code)
					}
					var final TaskView
					must(t, json.Unmarshal(public.Body.Bytes(), &final))
					wantUSD := snapshot.RetailUSDPer1M.Mul(decimal.NewFromInt(actual)).Shift(-6).Round(8)
					if final.Status != "succeeded" || final.BillingState != "settled" || final.Usage == nil || final.Usage.CompletionTokens == nil || *final.Usage.CompletionTokens != actual || !f.hold(t, created.ID).CapturedAmount.Equal(wantUSD) {
						t.Fatal("actual usage did not settle variant")
					}
					if strings.Contains(public.Body.String(), upstreamID) || strings.Contains(public.Body.String(), "official-test-credential") {
						t.Fatal("internal provider data leaked")
					}
					var receipt QueryReceipt
					must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, created.ID).First(&receipt).Error)
					if receipt.PayloadRef == "" || receipt.AppliedAt == nil {
						t.Fatal("query JSON evidence missing")
					}
					must(t, f.s.applyQueryReceipt(context.Background(), receipt))
					access := invocation.VideoAccess{ActorRole: invocation.VideoRoleRoot, ActorID: "fixture-reviewer", ReasonCode: "request_debugging", OperationID: "variant-review"}
					for _, section := range []string{invocation.VideoClientRequest, invocation.VideoEffectiveRequest, invocation.VideoClientResponse, invocation.VideoUpstreamResponse} {
						attempt := ""
						if section == invocation.VideoUpstreamResponse {
							attempt = receipt.AttemptID
						}
						body, e := f.s.ReadRequestHistory(context.Background(), f.key.OrgID, rid, section, attempt, access)
						must(t, e)
						if !json.Valid(body.Body) || strings.Contains(string(body.Body), "official-test-credential") {
							t.Fatal("JSON evidence unavailable or unsafe", section)
						}
					}
					log, e := f.s.RequestLogDetail(context.Background(), f.key.OrgID, rid, true)
					must(t, e)
					if log.Billing == nil || log.Billing.NetChargedAmount == nil || *log.Billing.NetChargedAmount != wantUSD.StringFixed(8) || log.Billing.BillableTokens == nil || *log.Billing.BillableTokens != actual {
						t.Fatal("request log lost actual billing")
					}
					replay := variantHTTP(f, router, "POST", "/v1/video/generations", "variant-http-create", string(raw))
					var replayed TaskView
					must(t, json.Unmarshal(replay.Body.Bytes(), &replayed))
					if replay.Code != 202 || replayed.ID != created.ID || f.posts.Load() != 1 {
						t.Fatal("idempotent replay resubmitted provider task")
					}
					var entries int64
					must(t, f.s.DB.Model(&videobilling.BillingEntry{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, created.ID).Count(&entries).Error)
					if entries != 1 {
						t.Fatal("replay duplicated settlement")
					}
				})
			}
		}
	}
}

func TestMySQLVariantModelsShareSupplierBudget(t *testing.T) {
	f, scope, _ := variantVideoFixture(t)
	fast, mini := variantInput(ark.Model20Fast, "480p", ""), variantInput(ark.Model20Mini, "480p", "")
	fastQuote, err := f.s.Estimate(f.key.OrgID, fast)
	must(t, err)
	miniQuote, err := f.s.Estimate(f.key.OrgID, mini)
	must(t, err)
	limit := fastQuote.SupplierReservedCNY.Add(miniQuote.SupplierReservedCNY).Sub(decimal.RequireFromString("0.00000001"))
	must(t, f.s.DB.Model(&videobilling.QuotaPolicy{}).Where("scope = ? AND owner_id = ?", videobilling.ScopeProvider, scope.ID).Update("limit_amount", limit).Error)
	_, _, err = f.s.Create(context.Background(), f.key, f.requestID("fast-budget"), "fast-budget-create", fast)
	must(t, err)
	_, _, err = f.s.Create(context.Background(), f.key, f.requestID("mini-budget"), "mini-budget-create", mini)
	if !errors.Is(err, videobilling.ErrQuotaExceeded) {
		t.Fatal("second model bypassed shared provider budget", err)
	}
	var tasks int64
	must(t, f.s.DB.Model(&task.Task{}).Where("org_id = ?", f.key.OrgID).Count(&tasks).Error)
	if tasks != 1 || f.posts.Load() != 0 {
		t.Fatal("budget rejection persisted or sent another task")
	}
}

func TestMySQLDispatchBudgetBlockStopsAcceptedButNeverReinterpretsSent(t *testing.T) {
	f, scope, _ := variantVideoFixture(t)
	must(t, f.s.DB.Model(&videobilling.PricingRule{}).Where("provider_scope_id = ?", scope.ID).Update("risk_multiplier", 1).Error)
	fastQuote, err := f.s.Estimate(f.key.OrgID, variantInput(ark.Model20Fast, "480p", ""))
	must(t, err)
	miniQuote, err := f.s.Estimate(f.key.OrgID, variantInput(ark.Model20Mini, "480p", ""))
	must(t, err)
	// Both tasks fit exactly. One extra billed token then exceeds the shared
	// total including the second task's still-owned reservation.
	limit := fastQuote.SupplierReservedCNY.Add(miniQuote.SupplierReservedCNY)
	must(t, f.s.DB.Model(&videobilling.QuotaPolicy{}).Where("scope = ? AND owner_id = ?", videobilling.ScopeProvider, scope.ID).Update("limit_amount", limit).Error)
	fast, _, err := f.s.Create(context.Background(), f.key, f.requestID("overrun"), "overrun-variant-first", variantInput(ark.Model20Fast, "480p", ""))
	must(t, err)
	mini, _, err := f.s.Create(context.Background(), f.key, f.requestID("queued"), "overrun-variant-second", variantInput(ark.Model20Mini, "480p", ""))
	must(t, err)
	f.dispatch(t, fast.ID)
	var snap videobilling.Snapshot
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, fast.ID).First(&snap).Error)
	actual := snap.EstimatedTokens + 1
	f.response = func(*http.Request) (*http.Response, error) {
		return reply(200, fmt.Sprintf(`{"id":%q,"status":"succeeded","usage":{"completion_tokens":%d}}`, f.upstream(t, fast.ID), actual)), nil
	}
	must(t, f.s.pollTask(context.Background(), f.key.OrgID, fast.ID))
	var bucket videobilling.QuotaBucket
	must(t, f.s.DB.Table("video_quota_buckets b").Select("b.*").Joins("JOIN video_quota_policies p ON p.id=b.policy_id").Where("p.scope = ? AND p.owner_id = ?", videobilling.ScopeProvider, scope.ID).First(&bucket).Error)
	if !bucket.Blocked {
		t.Fatal("actual plus outstanding reservation exceeded the budget without blocking")
	}
	_, _, err = f.s.Create(context.Background(), f.key, f.requestID("blocked-admission"), "overrun-variant-denied", variantInput(ark.Model20Mini, "480p", ""))
	if !errors.Is(err, videobilling.ErrQuotaExceeded) {
		t.Fatal("exhausted supplier budget admitted a new task", err)
	}
	f.dispatch(t, mini.ID)
	f.dispatch(t, mini.ID)
	view, err := f.s.Get(f.key.OrgID, mini.ID)
	must(t, err)
	var op Operation
	must(t, f.s.DB.Where("org_id = ? AND resource_id = ? AND kind = ?", f.key.OrgID, mini.ID, "create_task").First(&op).Error)
	if f.posts.Load() != 1 || view.Status != "failed" || view.BillingState != "released" || op.ErrorCode != "quota_blocked" || f.hold(t, mini.ID).Status != ledger.HoldReleased {
		t.Fatal("blocked accepted task reached provider or remained frozen")
	}
	must(t, f.s.DB.First(&bucket, bucket.ID).Error)
	if !bucket.Blocked || !bucket.ReservedAmount.IsZero() || !bucket.ConsumedAmount.Equal(snap.SupplierCNYPer1M.Mul(decimal.NewFromInt(actual)).Shift(-6).Round(8)) {
		t.Fatal("release erased blocked evidence or supplier consumption")
	}
	// A provider-accepted task is never reinterpreted as not sent by this gate.
	f.dispatch(t, fast.ID)
	if f.posts.Load() != 1 || f.hold(t, fast.ID).Status != ledger.HoldCaptured {
		t.Fatal("sent task was retried or refunded")
	}
}

func TestMySQLRiskOneVarianceKeepsSharedBudgetAvailable(t *testing.T) {
	f, scope, _ := variantVideoFixture(t)
	must(t, f.s.DB.Model(&videobilling.PricingRule{}).Where("provider_scope_id = ?", scope.ID).Update("risk_multiplier", 1).Error)
	fast, _, err := f.s.Create(context.Background(), f.key, f.requestID("variance"), "variance-first", variantInput(ark.Model20Fast, "480p", ""))
	must(t, err)
	mini, _, err := f.s.Create(context.Background(), f.key, f.requestID("variance-waiting"), "variance-waiting", variantInput(ark.Model20Mini, "480p", ""))
	must(t, err)
	f.dispatch(t, fast.ID)
	upstream := f.upstream(t, fast.ID)
	var snap videobilling.Snapshot
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, fast.ID).First(&snap).Error)
	actual := snap.EstimatedTokens + 1
	f.response = func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodPost {
			return reply(200, fmt.Sprintf(`{"id":%q}`, "up_"+mini.ID)), nil
		}
		return reply(200, fmt.Sprintf(`{"id":%q,"status":"succeeded","usage":{"completion_tokens":%d}}`, upstream, actual)), nil
	}
	must(t, f.s.pollTask(context.Background(), f.key.OrgID, fast.ID))
	var bucket videobilling.QuotaBucket
	must(t, f.s.DB.Table("video_quota_buckets b").Select("b.*").Joins("JOIN video_quota_policies p ON p.id=b.policy_id").Where("p.scope = ? AND p.owner_id = ?", videobilling.ScopeProvider, scope.ID).First(&bucket).Error)
	var entry videobilling.BillingEntry
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, fast.ID).First(&entry).Error)
	if bucket.Blocked || !entry.AbsorbedUSD.IsPositive() || !entry.CapturedUSD.Equal(snap.HoldUSD) || !bucket.ConsumedAmount.Equal(entry.SupplierCNY) {
		t.Fatal("variance blocked available budget or lost capped billing", bucket, entry)
	}
	makeFixtureTaskRateDue(t, f, mini.ID, "create")
	f.dispatch(t, mini.ID)
	view, err := f.s.Get(f.key.OrgID, mini.ID)
	must(t, err)
	if f.posts.Load() != 2 || view.Status != "queued" || f.hold(t, mini.ID).Status != ledger.HoldActive {
		t.Fatal("small variance stopped previously accepted work")
	}
	_, _, err = f.s.Create(context.Background(), f.key, f.requestID("variance-new"), "variance-new", variantInput(ark.Model20Mini, "480p", ""))
	must(t, err)
}

func TestMySQLConcurrentRiskOneSettlementPreservesSharedActuals(t *testing.T) {
	f, scope, _ := variantVideoFixture(t)
	must(t, f.s.DB.Model(&videobilling.PricingRule{}).Where("provider_scope_id = ?", scope.ID).Update("risk_multiplier", 1).Error)
	const taskCount = 4
	tasks := make([]TaskView, taskCount)
	var snap videobilling.Snapshot
	for i := range tasks {
		var key *identity.APIKey
		must(t, f.s.DB.Transaction(func(tx *gorm.DB) error {
			var err error
			_, key, err = identity.IssueKey(tx, identity.IssueKeyInput{OrgID: f.key.OrgID, Name: fmt.Sprintf("settlement-%d", i), Scopes: "video:*"})
			return err
		}))
		var err error
		tasks[i], _, err = f.s.Create(context.Background(), key, f.requestID(fmt.Sprintf("concurrent-settlement-%d", i)), fmt.Sprintf("settlement-%d", i), variantInput(ark.Model20Mini, "480p", ""))
		must(t, err)
	}
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, tasks[0].ID).First(&snap).Error)
	actual := snap.EstimatedTokens + 1
	var wg sync.WaitGroup
	errs := make(chan error, taskCount*2)
	for i := 0; i < taskCount*2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := tasks[i%taskCount].ID
			errs <- retryMySQLDeadlockedTransaction(f.s.DB, func(tx *gorm.DB) error {
				_, err := videobilling.SettleUsageTx(tx, videobilling.SettlementInput{OrgID: f.key.OrgID, TaskID: id, Now: f.now, Evidence: videobilling.UsageEvidence{EvidenceID: "ev_" + id, Source: "task_get", SourceRecordID: id, SourceVersion: "concurrent-v1", CompletionTokens: &actual, ObservedAt: f.now}})
				return err
			})
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		must(t, err)
	}
	var bucket videobilling.QuotaBucket
	must(t, f.s.DB.Table("video_quota_buckets b").Select("b.*").Joins("JOIN video_quota_policies p ON p.id=b.policy_id").Where("p.scope = ? AND p.owner_id = ?", videobilling.ScopeProvider, scope.ID).First(&bucket).Error)
	want := snap.SupplierCNYPer1M.Mul(decimal.NewFromInt(actual)).Shift(-6).Round(8).Mul(decimal.NewFromInt(taskCount))
	var entries, evidence int64
	must(t, f.s.DB.Model(&videobilling.BillingEntry{}).Where("org_id = ?", f.key.OrgID).Count(&entries).Error)
	must(t, f.s.DB.Model(&videobilling.UsageEvidence{}).Where("org_id = ?", f.key.OrgID).Count(&evidence).Error)
	if bucket.Blocked || !bucket.ReservedAmount.IsZero() || !bucket.ConsumedAmount.Equal(want) || entries != taskCount || evidence != taskCount || f.posts.Load() != 0 {
		t.Fatal("parallel keys lost or duplicated shared supplier actuals", bucket, entries, evidence)
	}
	if drift, err := ledger.CheckDrift(f.s.DB); err != nil || len(drift) != 0 {
		t.Fatal("concurrent settlement introduced ledger drift", drift, err)
	}
}

func TestMySQLDispatchBudgetUnknownAndMissingEvidenceFailClosed(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		t.Run(fmt.Sprintf("uncertain=%t", uncertain), func(t *testing.T) {
			f, scope, _ := variantVideoFixture(t)
			v, _, err := f.s.Create(context.Background(), f.key, f.requestID("dispatch-evidence"), "dispatch-evidence-create", variantInput(ark.Model20Mini, "480p", ""))
			must(t, err)
			if uncertain {
				f.response = func(*http.Request) (*http.Response, error) { return nil, io.ErrUnexpectedEOF }
				f.dispatch(t, v.ID)
				must(t, f.s.DB.Model(&videobilling.QuotaBucket{}).Where("policy_id IN (?)", f.s.DB.Model(&videobilling.QuotaPolicy{}).Select("id").Where("scope = ? AND owner_id = ?", videobilling.ScopeProvider, scope.ID)).Update("blocked", true).Error)
				f.dispatch(t, v.ID)
				view, e := f.s.Get(f.key.OrgID, v.ID)
				must(t, e)
				if view.Status != "submission_unknown" || f.posts.Load() != 1 || f.hold(t, v.ID).Status != ledger.HoldActive {
					t.Fatal("budget block reinterpreted unknown submit")
				}
			} else {
				must(t, f.s.DB.Where("org_id = ? AND task_id = ? AND supplier = ?", f.key.OrgID, v.ID, true).Delete(&videobilling.QuotaReservation{}).Error)
				var op Operation
				must(t, f.s.DB.Where("org_id = ? AND resource_id = ? AND kind = ?", f.key.OrgID, v.ID, "create_task").First(&op).Error)
				if e := f.s.processOperation(context.Background(), op); !errors.Is(e, videobilling.ErrInvariant) {
					t.Fatal("missing budget evidence not refused", e)
				}
				view, e := f.s.Get(f.key.OrgID, v.ID)
				must(t, e)
				if view.Status != "accepted" || f.posts.Load() != 0 || f.hold(t, v.ID).Status != ledger.HoldActive {
					t.Fatal("missing budget evidence sent or incorrectly released task")
				}
			}
		})
	}
}
