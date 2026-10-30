package video

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/task"
)

var modelGateActiveStatuses = []string{"accepted", "submitting", "submission_unknown", "queued", "running"}

// 模型级并发闸（0136）：配置行的 max_concurrent_tasks 按公共模型计数在途任务。
// 上限设为「基线在途 + 1」以免疫共享测试库里其他用例遗留的任务行：第一笔受理
// 占掉最后一个槽，第二笔被模型闸拒绝；清零后恢复受理（渠道总闸 fixture 为 100，
// 不先触发）。并发闸计数在受理事务内取行锁后的配置权威值。
func TestMySQLModelConcurrencyGate(t *testing.T) {
	f := mysqlFixture(t)
	var base int64
	must(t, f.s.DB.Model(&task.Task{}).
		Where("model = ? AND status IN ?", ark.Model20, modelGateActiveStatuses).
		Count(&base).Error)
	must(t, f.s.DB.Model(&ModelConfig{}).Where("public_model = ?", ark.Model20).
		Update("max_concurrent_tasks", int(base)+1).Error)

	v := f.create(t, "mconc-first")
	if v.ID == "" {
		t.Fatal("first create returned empty id")
	}
	rid, _ := randomID("")
	_, _, err := f.s.Create(context.Background(), f.key, rid, "mconc-second",
		ark.CreateRequest{Model: ark.Model20, Content: []ark.ContentItem{{Type: "text", Text: "second in-flight prompt"}}})
	if !errors.Is(err, ErrModelConcurrencyLimit) {
		t.Fatalf("second create err = %v, want ErrModelConcurrencyLimit", err)
	}
	// 0 = 不启用：同一在途任务不再阻塞受理。
	must(t, f.s.DB.Model(&ModelConfig{}).Where("public_model = ?", ark.Model20).
		Update("max_concurrent_tasks", 0).Error)
	rid, _ = randomID("")
	if _, _, err := f.s.Create(context.Background(), f.key, rid, "mconc-third",
		ark.CreateRequest{Model: ark.Model20, Content: []ark.ContentItem{{Type: "text", Text: "third prompt after limit cleared"}}}); err != nil {
		t.Fatalf("create with disabled model gate = %v, want accepted", err)
	}
}

// modelCreateRPM 热读缓存：配置了 create_rpm 的启用模型返回该值，未配置/停用返回 0。
func TestMySQLModelCreateRPMCache(t *testing.T) {
	f := mysqlFixture(t)
	must(t, f.s.DB.Model(&ModelConfig{}).Where("public_model = ?", ark.Model20).
		Update("create_rpm", 7).Error)
	if got := f.s.modelCreateRPM(ark.Model20); got != 7 {
		t.Fatalf("modelCreateRPM = %d, want 7", got)
	}
	if got := f.s.modelCreateRPM(ark.Model25); got != 0 {
		t.Fatalf("unconfigured model rpm = %d, want 0", got)
	}
	must(t, f.s.DB.Model(&ModelConfig{}).Where("public_model = ?", ark.Model20).
		Update("create_rpm", 0).Error)
	// 5s 缓存内仍是旧值；把缓存时间戳拨过期强制刷新后回落 0。
	f.s.limitMu.Lock()
	f.s.limitCache.loadedAt = f.now.Add(-6 * time.Second)
	f.s.limitMu.Unlock()
	if got := f.s.modelCreateRPM(ark.Model20); got != 0 {
		t.Fatalf("after clear + refresh rpm = %d, want 0", got)
	}
}
