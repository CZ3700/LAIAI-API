package video

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/task"
	"gorm.io/gorm"
)

func insertQuotaLockTestTask(tx *gorm.DB, org, user, key, channel, scope uint64, account string, submitted time.Time, status string) (string, error) {
	id, err := randomID("task_")
	if err != nil {
		return "", err
	}
	err = tx.Exec(`INSERT INTO tasks(task_id,org_id,user_id,api_key_id,model,channel_id,provider,status,hold_id,price_out_usd_per_1m,submitted_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, id, org, user, key, ark.Model20, channel, Provider, status, 1, 0, submitted).Error
	if err != nil {
		return "", err
	}
	err = tx.Exec(`INSERT INTO video_task_parameters(task_id,org_id,schema_version,provider_scope_id,provider_scope_version,account_ref,region,project_name,public_model,upstream_model,capability_version,parameters_json,has_input_video,input_duration_ms,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, org, 1, scope, 1, account, "cn-beijing", "default", ark.Model20, "doubao-seedance-2-0-260128", "test-cap-v1", `{"Resolution":"720p"}`, false, 0, submitted).Error
	return id, err
}

// The fixture applies every tracked migration in its own MySQL schema. These
// two interleavings exercise actual InnoDB locks, not a SQLite approximation.
func TestMySQLQuotaLockOrderAndCurrentCounts(t *testing.T) {
	h := queryRecoveryNew(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	org := h.users[0].key.OrgID
	key := h.users[0].key.ID
	user := uint64(7001)
	account := "test-query-recovery"
	var cfg ModelConfig
	if err := h.db.Where("public_model = ?", ark.Model20).Take(&cfg).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	limit := UserTaskLimit{OrgID: org, UserID: user, CreateTaskRPM: 1, MaxInflightTasks: 3, Timezone: "UTC", Version: 1, UpdatedBy: 1, UpdatedAt: now}
	if err := h.db.Create(&limit).Error; err != nil {
		t.Fatal(err)
	}
	rpm, concurrent := 60, 2
	quota := ProviderModelQuota{AccountRef: account, FoundationModel: "doubao-seedance-2-0", OfficialCreateTaskRPM: &rpm, OfficialConcurrentRequests: &concurrent, OfficialObservedAt: &now}
	if err := h.db.Create(&quota).Error; err != nil {
		t.Fatal(err)
	}
	var existing string
	if err := h.db.Transaction(func(tx *gorm.DB) error {
		var e error
		existing, e = insertQuotaLockTestTask(tx, org, user, key, cfg.ChannelID, h.scope, account, now.Add(-2*time.Minute), "accepted")
		return e
	}); err != nil {
		t.Fatal(err)
	}
	s := &Service{DB: h.db, Now: time.Now}

	// Admission owns user -> provider quota, then takes a current locking
	// count of the existing task. Worker prelocks in that same order before
	// touching the task row. The old task-first worker order deadlocked here.
	admissionLocked := make(chan struct{})
	continueAdmission := make(chan struct{})
	admissionDone := make(chan error, 1)
	go func() {
		admissionDone <- h.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if _, _, err := readUserTaskLimitTx(tx, org, user); err != nil {
				return err
			}
			close(admissionLocked)
			<-continueAdmission
			if err := s.checkUserAdmissionTx(tx, org, user, now); err != nil {
				return err
			}
			return s.checkProviderModelConcurrencyTx(tx, account, ark.Model20, "doubao-seedance-2-0-260128", false, true)
		})
	}()
	select {
	case <-admissionLocked:
	case <-ctx.Done():
		t.Fatal("admission did not acquire user lock")
	}
	workerDone := make(chan error, 1)
	workerStarted := make(chan struct{})
	go func() {
		workerDone <- h.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			close(workerStarted)
			identity, err := s.prelockCreateQuotaIdentityTx(tx, org, existing)
			if err != nil {
				return err
			}
			var row task.Task
			if err := locked(tx).Where("org_id = ? AND task_id = ?", org, existing).Take(&row).Error; err != nil {
				return err
			}
			if identity.UserID == nil || *identity.UserID != user || row.TaskID != existing {
				return ErrUnavailable
			}
			return nil
		})
	}()
	<-workerStarted
	time.Sleep(50 * time.Millisecond)
	close(continueAdmission)
	for _, done := range []<-chan error{admissionDone, workerDone} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("user -> provider -> task interleaving: %v", err)
			}
		case <-ctx.Done():
			t.Fatal("quota/task interleaving timed out")
		}
	}

	// A different admission starts with an old RR snapshot while the first
	// transaction holds the quota row. COUNT FOR UPDATE must see its newly
	// committed task after the wait, so the account pool cannot oversell.
	providerLocked := make(chan struct{})
	continueProvider := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- h.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if _, _, _, err := readProviderModelQuotaTx(tx, account, ark.Model20, "doubao-seedance-2-0-260128"); err != nil {
				return err
			}
			close(providerLocked)
			<-continueProvider
			_, err := insertQuotaLockTestTask(tx, org, user+1, key, cfg.ChannelID, h.scope, account, now, "accepted")
			return err
		})
	}()
	select {
	case <-providerLocked:
	case <-ctx.Done():
		t.Fatal("first account admission did not lock quota")
	}
	secondDone := make(chan error, 1)
	snapshotReady := make(chan struct{})
	go func() {
		secondDone <- h.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var ignored int64
			if err := tx.Model(&task.Task{}).Where("org_id = ?", org).Count(&ignored).Error; err != nil { // establish an old RR read view
				return err
			}
			close(snapshotReady)
			return s.checkProviderModelConcurrencyTx(tx, account, ark.Model20, "doubao-seedance-2-0-260128", false, true)
		})
	}()
	select {
	case <-snapshotReady:
	case <-ctx.Done():
		t.Fatal("second admission did not establish its old snapshot")
	}
	close(continueProvider)
	select {
	case err := <-firstDone:
		if err != nil {
			t.Fatalf("first account admission: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("first account admission timed out")
	}
	select {
	case err := <-secondDone:
		if !errors.Is(err, ErrProviderModelConcurrencyLimit) {
			t.Fatalf("old RR snapshot oversold account quota: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("second account admission timed out")
	}

	// Two keys for the same user share one rolling-minute admission count.
	// The second transaction waits on the user row until the first task is
	// committed, then sees it through a current read and must reject.
	otherUser := user + 2
	limit.UserID = otherUser
	if err := h.db.Create(&limit).Error; err != nil {
		t.Fatal(err)
	}
	firstUserLocked := make(chan struct{})
	continueFirstUser := make(chan struct{})
	firstUserDone := make(chan error, 1)
	go func() {
		firstUserDone <- h.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := s.checkUserAdmissionTx(tx, org, otherUser, now); err != nil {
				return err
			}
			close(firstUserLocked)
			<-continueFirstUser
			_, err := insertQuotaLockTestTask(tx, org, otherUser, key, cfg.ChannelID, h.scope, account, now, "accepted")
			return err
		})
	}()
	select {
	case <-firstUserLocked:
	case <-ctx.Done():
		t.Fatal("first user admission did not acquire limit row")
	}
	secondUserDone := make(chan error, 1)
	secondUserStarted := make(chan struct{})
	go func() {
		secondUserDone <- h.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			close(secondUserStarted)
			return s.checkUserAdmissionTx(tx, org, otherUser, now)
		})
	}()
	<-secondUserStarted
	time.Sleep(50 * time.Millisecond)
	close(continueFirstUser)
	select {
	case err := <-firstUserDone:
		if err != nil {
			t.Fatalf("first user admission: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("first user admission timed out")
	}
	select {
	case err := <-secondUserDone:
		if !errors.Is(err, ErrUserTaskRPM) {
			t.Fatalf("user RPM oversold: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("second user admission timed out")
	}
}
