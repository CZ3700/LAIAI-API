package leader

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeStore struct {
	held bool
	err  error
}

func (f *fakeStore) acquireOrRenew(ctx context.Context, key, token string, ttl time.Duration) (bool, error) {
	return f.held, f.err
}

// store=nil（单实例/未配 Redis）→ 恒 leader，无需续租。
func TestNilStoreAlwaysLeader(t *testing.T) {
	l := New(nil, "k", "t", time.Second, nil)
	if !l.IsLeader() {
		t.Fatal("nil store must always be leader")
	}
	l.Run(context.Background()) // 应立即返回，不阻塞
}

// 抢占成功 → leader；丢失 → 非 leader。
func TestAcquireAndLose(t *testing.T) {
	fs := &fakeStore{held: true}
	l := New(fs, "k", "t", time.Second, nil)
	if l.IsLeader() {
		t.Fatal("before refresh should not yet be leader")
	}
	l.refresh(context.Background())
	if !l.IsLeader() {
		t.Fatal("after successful acquire should be leader")
	}
	fs.held = false // 另一实例抢走
	l.refresh(context.Background())
	if l.IsLeader() {
		t.Fatal("after losing lease should not be leader")
	}
}

// Redis 出错 → fail-open（视为 leader，宁可多跑不可不跑——DB 幂等兜正确性）。
func TestRefreshErrorFailsOpen(t *testing.T) {
	fs := &fakeStore{held: false, err: errors.New("redis down")}
	l := New(fs, "k", "t", time.Second, nil)
	l.refresh(context.Background())
	if !l.IsLeader() {
		t.Fatal("on redis error must fail open (run as leader)")
	}
}
