package relay

import (
	"errors"
	"testing"
	"time"
)

// 用注入假时钟测试 TTL 缓存：命中只 load 一次、超 TTL 重新 load、invalidate 强制重载、
// 以及 clobber 防御（load 期间 invalidate 则结果不写回）。

func TestTTLCacheHitExpireInvalidate(t *testing.T) {
	now := time.Unix(1000, 0)
	clock := func() time.Time { return now }
	c := newTTLCache[string, int](10*time.Second, clock)

	loads := 0
	load := func() (int, error) { loads++; return loads, nil }

	// 首次 load
	if v, _ := c.getOrLoad("k", load); v != 1 {
		t.Fatalf("first get = %d, want 1", v)
	}
	// TTL 内命中，不重新 load
	now = now.Add(5 * time.Second)
	if v, _ := c.getOrLoad("k", load); v != 1 || loads != 1 {
		t.Fatalf("within-TTL get = %d loads=%d, want 1/1", v, loads)
	}
	// 超 TTL 重新 load
	now = now.Add(6 * time.Second) // 总 11s > 10s
	if v, _ := c.getOrLoad("k", load); v != 2 || loads != 2 {
		t.Fatalf("post-TTL get = %d loads=%d, want 2/2", v, loads)
	}
	// invalidate 强制重载
	c.invalidate("k")
	if v, _ := c.getOrLoad("k", load); v != 3 || loads != 3 {
		t.Fatalf("post-invalidate get = %d loads=%d, want 3/3", v, loads)
	}
	// invalidateAll
	c.invalidateAll()
	if v, _ := c.getOrLoad("k", load); v != 4 {
		t.Fatalf("post-invalidateAll get = %d, want 4", v)
	}
}

// load 失败绝不缓存（错误不钉住一个 TTL）。
func TestTTLCacheDoesNotCacheErrors(t *testing.T) {
	now := time.Unix(1000, 0)
	c := newTTLCache[string, int](10*time.Second, func() time.Time { return now })
	boom := errors.New("boom")
	if _, err := c.getOrLoad("k", func() (int, error) { return 0, boom }); err != boom {
		t.Fatalf("want boom, got %v", err)
	}
	// 下次成功 load 应被缓存（证明上次错误没写回）
	v, err := c.getOrLoad("k", func() (int, error) { return 42, nil })
	if err != nil || v != 42 {
		t.Fatalf("after error, get = %d/%v, want 42/nil", v, err)
	}
}

// clobber 防御：load 期间发生 invalidate → 该次 load 的（已失效的旧）结果不得写回缓存。
func TestTTLCacheClobberDefense(t *testing.T) {
	now := time.Unix(1000, 0)
	c := newTTLCache[string, int](10*time.Second, func() time.Time { return now })

	loads := 0
	// 第一次 load 时在返回前触发一次 invalidate（模拟 load 在锁外、期间有写失效）。
	v, _ := c.getOrLoad("k", func() (int, error) {
		loads++
		c.invalidate("k") // load 进行中失效
		return 100, nil
	})
	if v != 100 {
		t.Fatalf("caller still gets loaded value %d, want 100", v)
	}
	// 因 clobber 防御，100 不应被缓存：下次 get 必须重新 load。
	v2, _ := c.getOrLoad("k", func() (int, error) { loads++; return 200, nil })
	if v2 != 200 || loads != 2 {
		t.Fatalf("clobbered value leaked: get=%d loads=%d, want 200/2", v2, loads)
	}
}

func TestSnapCacheHitExpireInvalidate(t *testing.T) {
	now := time.Unix(0, 0)
	c := newSnapCache[[]int](8*time.Second, func() time.Time { return now })
	loads := 0
	load := func() ([]int, error) { loads++; return []int{loads}, nil }

	if v, _ := c.getOrLoad(load); v[0] != 1 {
		t.Fatalf("first = %v", v)
	}
	now = now.Add(4 * time.Second)
	if v, _ := c.getOrLoad(load); v[0] != 1 || loads != 1 {
		t.Fatalf("within-TTL = %v loads=%d", v, loads)
	}
	now = now.Add(5 * time.Second) // 9s > 8s
	if v, _ := c.getOrLoad(load); v[0] != 2 {
		t.Fatalf("post-TTL = %v", v)
	}
	c.invalidate()
	if v, _ := c.getOrLoad(load); v[0] != 3 {
		t.Fatalf("post-invalidate = %v", v)
	}
}

// pickFromSnapshot 与 PickChannelForAttempt 行为一致：分层、attempt→tier 钳底、exclude。
// 用单渠道层规避 weightedPick 随机性，断言确定性选路。
func TestPickFromSnapshotTierAndExclude(t *testing.T) {
	snap := []Channel{
		{ID: 1, Status: 1, Priority: 10, Weight: 1, Models: "m"},
		{ID: 2, Status: 1, Priority: 5, Weight: 1, Models: "m"},
	}
	// attempt 0 → 最高优先级层（ch1）
	if ch, err := pickFromSnapshot(snap, "m", 0, map[uint64]bool{}, nil); err != nil || ch.ID != 1 {
		t.Fatalf("attempt0 = %v err=%v, want ch1", ch, err)
	}
	// attempt 1 → 第二层（ch2）
	if ch, err := pickFromSnapshot(snap, "m", 1, map[uint64]bool{}, nil); err != nil || ch.ID != 2 {
		t.Fatalf("attempt1 = %v, want ch2", ch)
	}
	// attempt 2 钳到最低层（ch2）
	if ch, err := pickFromSnapshot(snap, "m", 2, map[uint64]bool{}, nil); err != nil || ch.ID != 2 {
		t.Fatalf("attempt2 = %v, want ch2 (clamped)", ch)
	}
	// 排除 ch1 后 attempt0 落到 ch2
	if ch, err := pickFromSnapshot(snap, "m", 0, map[uint64]bool{1: true}, nil); err != nil || ch.ID != 2 {
		t.Fatalf("exclude ch1 = %v, want ch2", ch)
	}
	// 模型不匹配 → ErrNoChannel
	if _, err := pickFromSnapshot(snap, "other", 0, map[uint64]bool{}, nil); !errors.Is(err, ErrNoChannel) {
		t.Fatalf("unknown model err = %v, want ErrNoChannel", err)
	}
}
