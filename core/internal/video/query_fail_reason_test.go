package video

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestClipFailReasonBoundsColumn（2026-11-18 事故回归）：tasks.fail_reason 是
// VARCHAR(1024)（0156）。A08 回填拼 code + ": " + message，若不做长度判断，
// 上游带 request id 的长报文会让整条 apply 事务 1406 回滚、收据无限重试。
// 断言：任意长度/任意字符集的报文经 clip 后都能放入列（字节 <= 1024 且
// UTF-8 合法），而正常的真实事故报文完整保留。
func TestClipFailReasonBoundsColumn(t *testing.T) {
	const code = "InvalidParameter.TaskTypeMismatch"
	// 11-18 事故真实报文（约 330 字节）：必须原样保留（不丢 request id）。
	real := "The parameter inner_generation_options.pe_classification specified in the request is not valid: " +
		"You requested the reference generation task type, but Seedance classified your task as " +
		"first-frame-to-video based on your prompt and input. Request id: " +
		"02179497095037000000000000000000000ffffac15cee6f3a133"
	if got := clipFailReason(code, real); got != code+": "+real {
		t.Fatal("real failure message must be preserved intact")
	}
	for _, message := range []string{
		"",
		"short",
		strings.Repeat("a", 1024),
		strings.Repeat("a", 2048),
		strings.Repeat("字", 1000),        // 3000 字节中文
		strings.Repeat("🙂", 3000),        // 12000 字节 emoji
		strings.Repeat("a", 1021) + "汉字", // 截断点落在多字节字符中间
	} {
		got := clipFailReason(code, message)
		if len(got) > maxFailReasonBytes {
			t.Fatalf("clip exceeded column width: %d bytes", len(got))
		}
		if !utf8.ValidString(got) {
			t.Fatalf("clip produced invalid UTF-8: %q", got)
		}
		if !strings.HasPrefix(got, code+": ") {
			t.Fatalf("clip lost the code prefix: %q", got[:20])
		}
	}
}
