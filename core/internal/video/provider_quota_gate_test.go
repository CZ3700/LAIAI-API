package video

import (
	"errors"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/ark"
	"gorm.io/gorm"
)

func TestFoundationModelForReviewedPublicModels(t *testing.T) {
	for _, test := range []struct{ public, upstream, foundation string }{
		{ark.Model20, "doubao-seedance-2-0-260128", "doubao-seedance-2-0"},
		{ark.Model25, "doubao-seedance-2-5-260628", "doubao-seedance-2-5"},
		{ark.Model20Fast, "doubao-seedance-2-0-fast-260128", "doubao-seedance-2-0-fast"},
		{ark.Model20Mini, "doubao-seedance-2-0-mini-260615", "doubao-seedance-2-0-mini"},
	} {
		got, err := foundationModelForTask(test.public, test.upstream)
		if err != nil || got != test.foundation {
			t.Fatalf("%s/%s => %q, %v", test.public, test.upstream, got, err)
		}
	}
	for _, test := range []struct{ public, upstream string }{
		{"other", "doubao-seedance-2-0-260128"},
		{ark.Model20, "doubao-seedance-2-0-fast-260128"},
		{ark.Model20Fast, "doubao-seedance-2-0-fast-260129"},
		{ark.Model25, "doubao-seedance-2-5-other"},
	} {
		if got, err := foundationModelForTask(test.public, test.upstream); err == nil {
			t.Fatalf("accepted unreviewed deployment %s/%s => %s", test.public, test.upstream, got)
		}
	}
}

func TestProviderModelConcurrencyIsMainAccountWideAnd4KSubpool(t *testing.T) {
	db := catalogLocalDB(t)
	must(t, db.AutoMigrate(&ProviderModelQuota{}))
	must(t, db.Exec(`CREATE TABLE tasks(org_id INTEGER,task_id TEXT,provider TEXT,status TEXT)`).Error)
	must(t, db.Exec(`CREATE TABLE video_task_parameters(org_id INTEGER,task_id TEXT,account_ref TEXT,public_model TEXT,parameters_json TEXT)`).Error)
	baseConcurrency, fourKConcurrency := 2, 1
	q := ProviderModelQuota{AccountRef: "mainaccount", FoundationModel: "doubao-seedance-2-0", OfficialConcurrentRequests: &baseConcurrency, OfficialCreateTaskRPM: &baseConcurrency, OfficialConcurrentRequests4K: &fourKConcurrency}
	must(t, db.Create(&q).Error)
	for _, row := range []struct {
		org            int
		id, resolution string
	}{{1, "first", "4k"}, {2, "second", "720p"}} {
		must(t, db.Exec("INSERT INTO tasks(org_id,task_id,provider,status) VALUES(?,?,?,?)", row.org, row.id, Provider, "running").Error)
		must(t, db.Exec("INSERT INTO video_task_parameters(org_id,task_id,account_ref,public_model,parameters_json) VALUES(?,?,?,?,?)", row.org, row.id, "mainaccount", ark.Model20, `{"Resolution":"`+row.resolution+`"}`).Error)
	}
	s := &Service{DB: db}
	check := func(is4K, includeAccepted bool) error {
		return db.Transaction(func(tx *gorm.DB) error {
			return s.checkProviderModelConcurrencyTx(tx, "mainaccount", ark.Model20, "doubao-seedance-2-0-260128", is4K, includeAccepted)
		})
	}
	if err := check(false, true); !errors.Is(err, ErrProviderModelConcurrencyLimit) {
		t.Fatalf("account-wide concurrency = %v", err)
	}
	must(t, db.Exec("UPDATE tasks SET status = 'accepted' WHERE task_id = ?", "second").Error)
	if err := check(false, false); err != nil {
		t.Fatalf("accepted backlog must not block outbound dispatch: %v", err)
	}
	must(t, db.Model(&ProviderModelQuota{}).Where("account_ref = ?", "mainaccount").Update("official_concurrent_requests", 3).Error)
	if err := check(false, true); err != nil {
		t.Fatalf("general pool should have one slot: %v", err)
	}
	if err := check(true, true); !errors.Is(err, ErrProviderModelConcurrencyLimit) {
		t.Fatalf("4K subpool = %v", err)
	}
	must(t, db.Model(&ProviderModelQuota{}).Where("account_ref = ?", "mainaccount").Update("official_create_task_rpm", 0).Error)
	if err := check(false, true); !errors.Is(err, ErrModelRateLimit) {
		t.Fatalf("zero official RPM must reject new admission before hold: %v", err)
	}
	if err := check(false, false); err != nil {
		t.Fatalf("old accepted work must remain retryable: %v", err)
	}
	must(t, db.Model(&ProviderModelQuota{}).Where("account_ref = ?", "mainaccount").Updates(map[string]any{"official_create_task_rpm": 2, "official_create_task_rpm_4k": 0}).Error)
	if err := check(true, true); !errors.Is(err, ErrModelRateLimit) {
		t.Fatalf("zero 4K RPM must reject new 4K admission: %v", err)
	}
}

func TestProviderModelSendGateSharesAccountAndChecks4KSeparately(t *testing.T) {
	db := catalogLocalDB(t)
	must(t, db.AutoMigrate(&ProviderModelQuota{}, &ProviderModelSendGate{}))
	baseRPM, baseConcurrency := 40, 8
	fourKRPM, fourKConcurrency := 10, 2
	q := ProviderModelQuota{
		AccountRef: "mainaccount", FoundationModel: "doubao-seedance-2-0",
		OfficialCreateTaskRPM: &baseRPM, OfficialConcurrentRequests: &baseConcurrency,
		OfficialCreateTaskRPM4K: &fourKRPM, OfficialConcurrentRequests4K: &fourKConcurrency,
	}
	must(t, db.Create(&q).Error)
	s := &Service{DB: db}
	at := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	claim := func(is4K bool, at time.Time) (bool, time.Time, error) {
		var ok bool
		var retry time.Time
		err := db.Transaction(func(tx *gorm.DB) error {
			var e error
			ok, retry, e = s.claimProviderModelSendTx(tx, "mainaccount", ark.Model20, "doubao-seedance-2-0-260128", is4K, at)
			return e
		})
		return ok, retry, err
	}
	if ok, _, err := claim(true, at); !ok || err != nil {
		t.Fatalf("first 4K send = %v %v", ok, err)
	}
	if ok, retry, err := claim(false, at.Add(time.Second)); ok || err != nil || !retry.Equal(at.Add(1500*time.Millisecond)) {
		t.Fatalf("same-account general gate = %v %v %v", ok, retry, err)
	}
	if ok, retry, err := claim(true, at.Add(2*time.Second)); ok || err != nil || !retry.Equal(at.Add(6*time.Second)) {
		t.Fatalf("separate 4K gate = %v %v %v", ok, retry, err)
	}
	if ok, _, err := claim(false, at.Add(2*time.Second)); !ok || err != nil {
		t.Fatalf("general task should not spend 4K slot: %v %v", ok, err)
	}
}

func TestProviderModelSendGateKeepsLegacyFallbackWithoutSnapshot(t *testing.T) {
	db := catalogLocalDB(t)
	must(t, db.AutoMigrate(&ProviderModelQuota{}, &ProviderModelSendGate{}))
	s := &Service{DB: db}
	err := db.Transaction(func(tx *gorm.DB) error {
		allowed, retry, e := s.claimProviderModelSendTx(tx, "mainaccount", ark.Model25, "doubao-seedance-2-5-260628", false, time.Now())
		if !allowed || !retry.IsZero() || e != nil {
			t.Fatalf("missing snapshot fallback = %v %v %v", allowed, retry, e)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
