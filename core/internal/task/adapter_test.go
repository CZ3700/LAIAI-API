package task

import (
	"errors"
	"testing"
)

// TestAdaptorForRegistry：任务型适配器分发——注册表命中、家族回退、未命中三类。
// 锁住"新增厂商=注册表登记一行"的契约，避免回退逻辑被误改。
func TestAdaptorForRegistry(t *testing.T) {
	// 直接登记的 volcengine 家族两个 provider code 都应解析到 volcengineTask。
	for _, code := range []string{"volcengine", "modelark"} {
		ad, err := adaptorFor(code)
		if err != nil {
			t.Fatalf("adaptorFor(%q) unexpected error: %v", code, err)
		}
		if _, ok := ad.(volcengineTask); !ok {
			t.Fatalf("adaptorFor(%q) = %T, want volcengineTask", code, ad)
		}
	}

	// 纯 chat provider（openai 家族）不是任务型——必须返回 ErrNoTaskAdaptor，不可误命中。
	for _, code := range []string{"openai", "deepseek", "anthropic", "gemini"} {
		if _, err := adaptorFor(code); !errors.Is(err, ErrNoTaskAdaptor) {
			t.Fatalf("adaptorFor(%q) err = %v, want ErrNoTaskAdaptor", code, err)
		}
	}

	// 完全未知的 provider code 同样返回 ErrNoTaskAdaptor（不 panic、不空指针）。
	if _, err := adaptorFor("does-not-exist"); !errors.Is(err, ErrNoTaskAdaptor) {
		t.Fatalf("adaptorFor(unknown) err = %v, want ErrNoTaskAdaptor", err)
	}
}
