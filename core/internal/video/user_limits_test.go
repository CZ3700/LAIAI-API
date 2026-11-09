package video

import (
	"errors"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/task"
	"gorm.io/gorm"
)

func userLimitFixture(t *testing.T, limit UserTaskLimit) *Service {
	t.Helper()
	db := catalogLocalDB(t)
	for _, ddl := range []string{
		`CREATE TABLE tasks(id INTEGER PRIMARY KEY,org_id INTEGER,user_id INTEGER,provider TEXT,status TEXT,submitted_at DATETIME)`,
		`CREATE TABLE video_user_task_limits(org_id INTEGER,user_id INTEGER,create_task_rpm INTEGER,max_inflight_tasks INTEGER,daily_task_limit INTEGER,monthly_task_limit INTEGER,timezone TEXT,version INTEGER,updated_by INTEGER,updated_at DATETIME,PRIMARY KEY(org_id,user_id))`,
		`CREATE TABLE video_user_send_gates(org_id INTEGER,user_id INTEGER,next_allowed_at DATETIME,updated_at DATETIME,PRIMARY KEY(org_id,user_id))`,
	} {
		must(t, db.Exec(ddl).Error)
	}
	if limit.Version == 0 {
		limit.Version = 1
	}
	if limit.Timezone == "" {
		limit.Timezone = "Asia/Shanghai"
	}
	limit.OrgID, limit.UserID = 1, 7
	must(t, db.Create(&limit).Error)
	return &Service{DB: db, Now: time.Now}
}

func checkUser(t *testing.T, s *Service, at time.Time, user uint64) error {
	t.Helper()
	return s.DB.Transaction(func(tx *gorm.DB) error { return s.checkUserAdmissionTx(tx, 1, user, at) })
}

func insertUserTask(t *testing.T, s *Service, user uint64, at time.Time, status string) {
	t.Helper()
	must(t, s.DB.Exec("INSERT INTO tasks(org_id,user_id,provider,status,submitted_at) VALUES(?,?,?,?,?)", 1, user, Provider, status, at).Error)
}

func TestUserTaskLimitsCountAcrossKeysAndFailedTasks(t *testing.T) {
	at := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	s := userLimitFixture(t, UserTaskLimit{CreateTaskRPM: 2, MaxInflightTasks: 2, DailyTaskLimit: 3, MonthlyTaskLimit: 4})
	insertUserTask(t, s, 7, at.Add(-20*time.Second), "failed")
	insertUserTask(t, s, 8, at.Add(-10*time.Second), "running")
	if err := checkUser(t, s, at, 7); err != nil {
		t.Fatalf("one failed task should leave one RPM slot: %v", err)
	}
	insertUserTask(t, s, 7, at.Add(-10*time.Second), "accepted")
	if err := checkUser(t, s, at, 7); !errors.Is(err, ErrUserTaskRPM) {
		t.Fatalf("second accepted task of the same user = %v, want RPM", err)
	}
	// The failed task remains a counted creation, while older creations leave
	// the sliding RPM window without erasing the day/month counters.
	if err := checkUser(t, s, at.Add(2*time.Minute), 7); err != nil {
		t.Fatalf("RPM should recover after a minute: %v", err)
	}
	insertUserTask(t, s, 7, at.Add(-2*time.Hour), "failed")
	if err := checkUser(t, s, at.Add(2*time.Minute), 7); !errors.Is(err, ErrUserDailyTasks) {
		t.Fatalf("daily accepted count = %v", err)
	}
}

func TestUserConcurrencyIncludesUnknownAndQueued(t *testing.T) {
	at := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	s := userLimitFixture(t, UserTaskLimit{MaxInflightTasks: 2})
	insertUserTask(t, s, 7, at.Add(-time.Hour), "submission_unknown")
	insertUserTask(t, s, 7, at.Add(-time.Hour), "queued")
	if err := checkUser(t, s, at, 7); !errors.Is(err, ErrUserTaskConcurrency) {
		t.Fatalf("unknown and queued should fill user slots: %v", err)
	}
	must(t, s.DB.Model(&task.Task{}).Where("user_id = ? AND status = ?", 7, "queued").Update("status", "succeeded").Error)
	if err := checkUser(t, s, at, 7); err != nil {
		t.Fatalf("terminal transition should free one slot: %v", err)
	}
}

func TestUserSendGatePacesAndRetries(t *testing.T) {
	at := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	s := userLimitFixture(t, UserTaskLimit{CreateTaskRPM: 2})
	claim := func(now time.Time) (bool, time.Time, error) {
		var allowed bool
		var retry time.Time
		err := s.DB.Transaction(func(tx *gorm.DB) error {
			var e error
			allowed, retry, e = s.claimUserSendTx(tx, 1, 7, now)
			return e
		})
		return allowed, retry, err
	}
	if ok, _, err := claim(at); !ok || err != nil {
		t.Fatalf("first send = %v %v", ok, err)
	}
	if ok, retry, err := claim(at.Add(10 * time.Second)); ok || err != nil || !retry.Equal(at.Add(30*time.Second)) {
		t.Fatalf("too-early send = %v %v %v", ok, retry, err)
	}
	if ok, _, err := claim(at.Add(30 * time.Second)); !ok || err != nil {
		t.Fatalf("send at due = %v %v", ok, err)
	}
}
