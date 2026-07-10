package task

// 200-信封提交失败分类（对抗 review 修）：渠道侧故障（欠费/限流/鉴权/内部）走 failover，
// 仅客户端参数/内容错误短路故障转移。

import "testing"

func TestSubmitErrorIsClientFault(t *testing.T) {
	clientFaults := []string{
		"content sensitive",             // hailuo 1026
		"invalid_action bad action",     // suno
		"invalid model_name",            // kling 1200
		"DataInspectionFailed sensitive content", // ali 内容审查
		"invalid parameter: duration",
		"prompt violates content policy",
	}
	for _, m := range clientFaults {
		if !submitErrorIsClientFault(m) {
			t.Errorf("want client-fault (short-circuit): %q", m)
		}
	}
	channelFaults := []string{
		"insufficient balance",       // 欠费
		"invalid api key",            // 渠道凭证（含 invalid 但属渠道侧）
		"rate limit exceeded",        // 限流
		"Throttling.User",            // ali 限流
		"authentication failed",      // 鉴权
		"Authorization is invalid",   // kling 1002 鉴权（含 invalid 但属渠道侧，对抗 review 修）
		"Sign is invalid",            // kling 1004 签名（含 invalid 但属渠道侧）
		"sign check error",           // 签名校验
		"invalid signature",          // 签名
		"internal server error",      // 内部
		"service unavailable, try again later",
		"upstream timeout",
		"账户余额不足",                  // 中文欠费
		"请求过于频繁",                  // 中文限流
		"验签失败",                      // 中文签名
		"",                           // 空 → 默认 failover
		"something totally unknown",  // 未知 → 默认 failover
	}
	for _, m := range channelFaults {
		if submitErrorIsClientFault(m) {
			t.Errorf("want channel-fault (failover): %q", m)
		}
	}
}

func TestSummarizeSubmitErrKeepsInternalErrors(t *testing.T) {
	// 我方内部错误（DB/凭证）原文保留供诊断，不被伪标为 upstream rejected
	if got := summarizeSubmitErr(errDBFake); got != "database is locked" {
		t.Errorf("internal err = %q, want original text", got)
	}
	if got := summarizeSubmitErr(nil); got != "upstream error" {
		t.Errorf("nil = %q", got)
	}
}

var errDBFake = fakeErr("database is locked")

type fakeErr string

func (e fakeErr) Error() string { return string(e) }
