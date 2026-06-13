package relay

import (
	"log/slog"
	"testing"
	"time"

	"gorm.io/gorm"
)

// disableChannel 是异步的，轮询等待状态翻转（最多 ~1s）
func waitDisabled(t *testing.T, db *gorm.DB, id uint64) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if chStatus(t, db, id) == 2 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("channel not disabled within timeout")
}

// 给 picker DB 补一张 ext_settings，供 autoDisablePolicy 读取
func withSettings(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.Exec(`CREATE TABLE ext_settings (name TEXT PRIMARY KEY, value TEXT NOT NULL)`).Error; err != nil {
		t.Fatal(err)
	}
}

func chStatus(t *testing.T, db *gorm.DB, id uint64) int8 {
	t.Helper()
	var ch Channel
	if err := db.First(&ch, id).Error; err != nil {
		t.Fatal(err)
	}
	return ch.Status
}

func newRelay(db *gorm.DB) *Relay {
	return &Relay{DB: db, Log: slog.Default(), failCounts: map[uint64]int{}}
}

// 阈值 = 2：第一次失败不禁用，第二次才禁用；成功清零后重新计数。
func TestNoteFailureThreshold(t *testing.T) {
	db := pickerDB(t)
	withSettings(t, db)
	db.Exec(`INSERT INTO ext_settings (name, value) VALUES ('channel_disable_threshold', '2')`)
	id := mkChannel(t, db, "c", 0, 1, 1, "m1")
	r := newRelay(db)

	r.noteFailure(id, "boom")
	if chStatus(t, db, id) != 1 {
		t.Fatal("disabled after 1 failure, want still active (threshold=2)")
	}
	r.noteFailure(id, "boom")
	waitDisabled(t, db, id) // 第二次失败 → 异步禁用
}

// 总开关关闭：连续失败也绝不自动禁用（交人工）。
func TestNoteFailureSwitchOff(t *testing.T) {
	db := pickerDB(t)
	withSettings(t, db)
	db.Exec(`INSERT INTO ext_settings (name, value) VALUES ('channel_auto_disable', 'false')`)
	id := mkChannel(t, db, "c", 0, 1, 1, "m1")
	r := newRelay(db)

	for i := 0; i < 5; i++ {
		r.noteFailure(id, "boom")
	}
	if chStatus(t, db, id) != 1 {
		t.Fatal("channel disabled despite auto-disable switch off")
	}
}

// 成功清零：失败 1 次后成功，再失败 1 次也不应到阈值（阈值 2）。
func TestResetFailuresOnSuccess(t *testing.T) {
	db := pickerDB(t)
	withSettings(t, db)
	db.Exec(`INSERT INTO ext_settings (name, value) VALUES ('channel_disable_threshold', '2')`)
	id := mkChannel(t, db, "c", 0, 1, 1, "m1")
	r := newRelay(db)

	r.noteFailure(id, "boom")
	r.resetFailures(id) // 一次成功
	r.noteFailure(id, "boom")
	if chStatus(t, db, id) != 1 {
		t.Fatal("disabled though success reset the counter between failures")
	}
}
