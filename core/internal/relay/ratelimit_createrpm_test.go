package relay

import (
	"errors"
	"testing"
)

// AllowModelCreateRPM（task.Gate，视频受理侧平台级窗口）：
// 桶 `vm:<model>` 计数恰好在 limit 处截断；0=不限；与租户级 `m:` 桶互不干扰。
func TestAllowModelCreateRPMCountsToLimit(t *testing.T) {
	fc := &fakeCounter{m: map[string]int64{}}
	r := rateTestRelay(fc, 3)
	allowed := 0
	for i := 0; i < 6; i++ {
		if r.AllowModelCreateRPM("seedance-2.5", 3) {
			allowed++
		}
	}
	if allowed != 3 {
		t.Fatalf("allowed = %d, want 3 (create RPM via windowed counter)", allowed)
	}
}

func TestAllowModelCreateRPMZeroIsUnlimited(t *testing.T) {
	fc := &fakeCounter{m: map[string]int64{}}
	r := rateTestRelay(fc, 1)
	for i := 0; i < 50; i++ {
		if !r.AllowModelCreateRPM("seedance-2.0", 0) {
			t.Fatalf("request %d blocked; limit 0 must be unlimited", i)
		}
	}
	if len(fc.m) != 0 {
		t.Fatalf("unlimited path created window state: %v", fc.m)
	}
}

func TestAllowModelCreateRPMIsolation(t *testing.T) {
	fc := &fakeCounter{m: map[string]int64{}}
	r := rateTestRelay(fc, 1)
	if !r.AllowModelCreateRPM("seedance-2.5", 1) {
		t.Fatal("first 2.5 create must pass")
	}
	if r.AllowModelCreateRPM("seedance-2.5", 1) {
		t.Fatal("second 2.5 create must hit the model window")
	}
	if !r.AllowModelCreateRPM("seedance-2.0", 1) {
		t.Fatal("other model must have its own bucket")
	}
	if ok, _ := r.allowModelRate(7, "seedance-2.5"); !ok {
		t.Fatal("tenant-level (org,model) bucket must be independent of the platform create window")
	}
}

// fail-open 纪律与 allowModelRate 一致：窗口计数器故障时不拒绝受理。
func TestAllowModelCreateRPMFailsOpenOnCounterError(t *testing.T) {
	fc := &fakeCounter{m: map[string]int64{}, err: errors.New("redis unreachable")}
	r := rateTestRelay(fc, 1)
	for i := 0; i < 10; i++ {
		if !r.AllowModelCreateRPM("seedance-2.5", 1) {
			t.Fatalf("request %d blocked; must fail open on counter error", i)
		}
	}
}
