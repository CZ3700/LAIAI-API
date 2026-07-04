package relay

import (
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const pickerSchema = `
CREATE TABLE channels (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL, provider TEXT NOT NULL, base_url TEXT NOT NULL,
    credential_enc TEXT NOT NULL DEFAULT '', models TEXT NOT NULL,
    weight INTEGER NOT NULL DEFAULT 1, priority INTEGER NOT NULL DEFAULT 0,
    max_concurrent_tasks INTEGER NOT NULL DEFAULT 3, strict_response INTEGER NOT NULL DEFAULT 1, model_mapping TEXT NOT NULL DEFAULT '',
    proxy TEXT NOT NULL DEFAULT '',
    param_override TEXT NOT NULL DEFAULT '', header_override TEXT NOT NULL DEFAULT '',
    status INTEGER NOT NULL DEFAULT 1
);
`

func pickerDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	// 单连接：:memory: 下每条连接是独立库——noteFailure→disableChannel 的异步 goroutine
	// 必须与前台读写共享同一库，否则按连接命中不同空库，导致 autodisable 测试时序性 flaky。
	if sqlDB, e := db.DB(); e == nil {
		sqlDB.SetMaxOpenConns(1)
	}
	if err := db.Exec(pickerSchema).Error; err != nil {
		t.Fatal(err)
	}
	return db
}

func mkChannel(t *testing.T, db *gorm.DB, name string, priority, weight int, status int8, models string) uint64 {
	t.Helper()
	ch := &Channel{Name: name, Provider: "openai", BaseURL: "http://x", Models: models,
		Weight: weight, Priority: priority, Status: status}
	if err := db.Create(ch).Error; err != nil {
		t.Fatal(err)
	}
	return ch.ID
}

func TestPickPriorityTiers(t *testing.T) {
	db := pickerDB(t)
	high := mkChannel(t, db, "high", 10, 1, 1, "m1")
	low := mkChannel(t, db, "low", 0, 1, 1, "m1")

	// attempt 0 → 高优先级层
	for i := 0; i < 10; i++ {
		ch, err := PickChannelForAttempt(db, "m1", 0, nil, nil)
		if err != nil || ch.ID != high {
			t.Fatalf("attempt 0 picked %v (err %v), want high tier", ch, err)
		}
	}
	// attempt 1 → 次层
	ch, err := PickChannelForAttempt(db, "m1", 1, nil, nil)
	if err != nil || ch.ID != low {
		t.Fatalf("attempt 1 = %v, want low tier", ch)
	}
	// attempt 99 → 钳到最低层（保证重试永远有渠道）
	ch, err = PickChannelForAttempt(db, "m1", 99, nil, nil)
	if err != nil || ch.ID != low {
		t.Fatalf("attempt 99 = %v, want clamped to last tier", ch)
	}
}

func TestPickExclusionAndModelFilter(t *testing.T) {
	db := pickerDB(t)
	a := mkChannel(t, db, "a", 0, 1, 1, "m1,m2")
	b := mkChannel(t, db, "b", 0, 1, 1, "m1")
	mkChannel(t, db, "other-model", 0, 1, 1, "m3")
	mkChannel(t, db, "disabled", 0, 1, 0, "m1")
	mkChannel(t, db, "auto-disabled", 0, 1, 2, "m1")

	// 排除 a 后只剩 b
	for i := 0; i < 10; i++ {
		ch, err := PickChannelForAttempt(db, "m1", 0, map[uint64]bool{a: true}, nil)
		if err != nil || ch.ID != b {
			t.Fatalf("with exclusion picked %v, want b", ch)
		}
	}
	// 全部排除 → ErrNoChannel
	if _, err := PickChannelForAttempt(db, "m1", 0, map[uint64]bool{a: true, b: true}, nil); err != ErrNoChannel {
		t.Fatalf("want ErrNoChannel, got %v", err)
	}
	// 无人服务的模型
	if _, err := PickChannelForAttempt(db, "m999", 0, nil, nil); err != ErrNoChannel {
		t.Fatalf("unknown model: want ErrNoChannel, got %v", err)
	}
}

func TestWeightedPickZeroWeightSemantics(t *testing.T) {
	// 零权重 = 均分而非"别用我"（知识库 §3）
	a := &Channel{ID: 1, Weight: 0}
	b := &Channel{ID: 2, Weight: 0}
	seen := map[uint64]int{}
	for i := 0; i < 200; i++ {
		seen[weightedPick([]*Channel{a, b}, nil).ID]++
	}
	if seen[1] == 0 || seen[2] == 0 {
		t.Fatalf("zero-weight channels not equally selectable: %v", seen)
	}
	// 混合：weight=1 与 weight=0 共存时，0 权重渠道仍可被选（平滑因子）
	c := &Channel{ID: 3, Weight: 1}
	d := &Channel{ID: 4, Weight: 0}
	seen = map[uint64]int{}
	for i := 0; i < 500; i++ {
		seen[weightedPick([]*Channel{c, d}, nil).ID]++
	}
	if seen[4] == 0 {
		t.Fatalf("zero-weight starved in mixed tier: %v", seen)
	}
}
