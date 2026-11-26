package forward

import (
	"cmp"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// OfficialTaskView 描述平台异步任务在构造厂商原生响应时的归一化视图（已剥离内部渠道/密钥敏感信息）。
type OfficialTaskView struct {
	TaskID           string
	Model            string
	Status           string // queued | running | succeeded | failed | cancelled
	Progress         int
	ResultURL        string
	LastFrameURL     string
	CompletionTokens int64
	CostUSD          decimal.Decimal
	BillingDetail    string
	ForwardRuleEID   string
	BillingRulePID   string
	FailReason       string
	SubmittedAt      time.Time
	StartedAt        *time.Time
	FinishedAt       *time.Time
	Resolution       string
	Ratio            string
	Duration         int64
	Draft            bool
	GenerateAudio    bool
	ServiceTier      string
	ViduType         string
}

// InferViduType 从请求路径推断 Vidu 官方任务类型。
func InferViduType(path string) string {
	clean := strings.ToLower(strings.TrimSpace(path))
	switch {
	case strings.Contains(clean, "text2video"):
		return "text2video"
	case strings.Contains(clean, "start-end2video"):
		return "start-end2video"
	case strings.Contains(clean, "reference2video"):
		return "reference2video"
	default:
		return "img2video"
	}
}

// WrapVolcengineCreate 构造火山方舟 / Seedance 创建任务响应（兼容已有 cost_usd / forward_rule_eid 断言）。
func WrapVolcengineCreate(v OfficialTaskView) map[string]any {
	created := v.SubmittedAt.Unix()
	if created <= 0 {
		created = time.Now().Unix()
	}
	return map[string]any{
		"id":               v.TaskID,
		"model":            v.Model,
		"status":           cmp.Or(v.Status, "queued"),
		"created_at":       created,
		"forward_rule_eid": v.ForwardRuleEID,
		"billing_rule_pid": v.BillingRulePID,
	}
}

// WrapVolcengineQuery 构造火山方舟 / Seedance 查询任务响应。
func WrapVolcengineQuery(v OfficialTaskView) map[string]any {
	created := v.SubmittedAt.Unix()
	if created <= 0 {
		created = time.Now().Unix()
	}
	updated := created
	if v.FinishedAt != nil {
		updated = v.FinishedAt.Unix()
	} else if v.StartedAt != nil {
		updated = v.StartedAt.Unix()
	}
	out := map[string]any{
		"id":                      v.TaskID,
		"model":                   v.Model,
		"status":                  cmp.Or(v.Status, "running"),
		"created_at":              created,
		"updated_at":              updated,
		"service_tier":            cmp.Or(v.ServiceTier, "default"),
		"execution_expires_after": int64(3600),
		"generate_audio":          v.GenerateAudio,
		"draft":                   v.Draft,
		"output_format":           "mp4",
		"cost_usd":                v.CostUSD,
		"billing_detail":          v.BillingDetail,
		"forward_rule_eid":        v.ForwardRuleEID,
		"billing_rule_pid":        v.BillingRulePID,
	}
	if v.Resolution != "" {
		out["resolution"] = v.Resolution
	}
	if v.Ratio != "" {
		out["ratio"] = v.Ratio
	}
	if v.Duration > 0 {
		out["duration"] = v.Duration
	}
	if v.Status == "succeeded" && v.ResultURL != "" {
		content := map[string]any{"video_url": v.ResultURL}
		if v.LastFrameURL != "" {
			content["last_frame_url"] = v.LastFrameURL
		}
		out["content"] = content
	}
	if v.CompletionTokens > 0 {
		out["usage"] = map[string]any{
			"completion_tokens": v.CompletionTokens,
			"total_tokens":      v.CompletionTokens,
		}
	}
	if v.FailReason != "" {
		out["error"] = map[string]any{
			"code":    "TaskFailed",
			"message": v.FailReason,
		}
	}
	return out
}

// WrapViduCreate 构造 Vidu 官方 /ent/v2/* 创建任务响应。
func WrapViduCreate(v OfficialTaskView, reqBody map[string]any) map[string]any {
	createdAt := v.SubmittedAt.UTC().Format(time.RFC3339Nano)
	if v.SubmittedAt.IsZero() {
		createdAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	out := map[string]any{
		"task_id":    v.TaskID,
		"type":       cmp.Or(v.ViduType, "img2video"),
		"state":      "created",
		"created_at": createdAt,
	}
	if v.Model != "" {
		out["model"] = v.Model
	}
	for _, key := range []string{"style", "prompt", "images", "duration", "seed", "aspect_ratio", "resolution", "movement_amplitude"} {
		if val, ok := reqBody[key]; ok {
			out[key] = val
		}
	}
	return out
}

// WrapViduQuery 构造 Vidu 官方 GET /ent/v2/tasks/:id/creations 查询响应。
func WrapViduQuery(v OfficialTaskView) map[string]any {
	vType := cmp.Or(v.ViduType, "img2video")
	switch v.Status {
	case "succeeded":
		creations := []map[string]any{}
		if v.ResultURL != "" {
			item := map[string]any{
				"id":  v.TaskID + "_0",
				"url": v.ResultURL,
			}
			if v.LastFrameURL != "" {
				item["cover_url"] = v.LastFrameURL
			}
			creations = append(creations, item)
		}
		out := map[string]any{
			"id":        v.TaskID,
			"type":      vType,
			"state":     "success",
			"err_code":  "",
			"err_msg":   "",
			"creations": creations,
		}
		if v.Model != "" {
			out["model"] = v.Model
		}
		if v.CompletionTokens > 0 {
			out["credits"] = max(int64(1), v.CompletionTokens/1_000_000)
		}
		return out
	case "failed", "cancelled":
		out := map[string]any{
			"id":        v.TaskID,
			"type":      vType,
			"state":     "failed",
			"err_code":  "1000",
			"err_msg":   cmp.Or(v.FailReason, "generation failed"),
			"creations": []any{},
		}
		if v.Model != "" {
			out["model"] = v.Model
		}
		return out
	default:
		state := "processing"
		if v.Status == "queued" {
			state = "queueing"
		}
		out := map[string]any{
			"id":        v.TaskID,
			"type":      vType,
			"state":     state,
			"err_code":  "",
			"err_msg":   "",
			"creations": []any{},
		}
		if v.Model != "" {
			out["model"] = v.Model
		}
		return out
	}
}

// WrapMiniMaxV2Create 构造 MiniMax V2 POST /v2/video_generation 创建响应。
func WrapMiniMaxV2Create(v OfficialTaskView) map[string]any {
	return map[string]any{
		"task_id": v.TaskID,
		"base_resp": map[string]any{
			"status_code": 0,
			"status_msg":  "success",
		},
	}
}

// WrapMiniMaxV2Query 构造 MiniMax V2 GET /v2/query/video_generation/:id 查询响应。
func WrapMiniMaxV2Query(v OfficialTaskView) map[string]any {
	status := "running"
	switch v.Status {
	case "queued":
		status = "queued"
	case "succeeded":
		status = "succeeded"
	case "failed":
		status = "failed"
	case "cancelled":
		status = "cancelled"
	}
	taskObj := map[string]any{
		"id":        v.TaskID,
		"model":     v.Model,
		"status":    status,
		"task_type": "generation",
		"modality":  "video",
	}
	if v.Resolution != "" {
		taskObj["resolution"] = v.Resolution
	}
	if v.Duration > 0 {
		taskObj["duration"] = v.Duration
	}
	if v.Ratio != "" {
		taskObj["ratio"] = v.Ratio
	}
	switch status {
	case "succeeded":
		if v.ResultURL != "" {
			c := map[string]any{"url": v.ResultURL}
			if v.LastFrameURL != "" {
				c["last_frame_url"] = v.LastFrameURL
			}
			taskObj["content"] = c
		}
		dur := v.Duration
		taskObj["usage"] = map[string]any{
			"prompt_tokens":     int64(0),
			"completion_tokens": v.CompletionTokens,
			"total_tokens":      v.CompletionTokens,
			"input_seconds":     int64(0),
			"output_seconds":    dur,
			"total_seconds":     dur,
		}
	case "failed", "cancelled":
		taskObj["error"] = map[string]any{
			"code":    "1026",
			"message": cmp.Or(v.FailReason, "generation failed"),
		}
		taskObj["usage"] = map[string]any{}
	}
	return map[string]any{
		"task": taskObj,
		"base_resp": map[string]any{
			"status_code": 0,
			"status_msg":  "success",
		},
	}
}

// WrapKlingV3Create 构造可灵 3.0 / Omni 原生创建响应。
func WrapKlingV3Create(v OfficialTaskView) map[string]any {
	createMS := v.SubmittedAt.UnixMilli()
	if createMS <= 0 {
		createMS = time.Now().UnixMilli()
	}
	return map[string]any{
		"code":       0,
		"message":    "SUCCEED",
		"request_id": v.TaskID,
		"task_id":    v.TaskID,
		"data": map[string]any{
			"id":          v.TaskID,
			"task_id":     v.TaskID,
			"status":      "submitted",
			"create_time": createMS,
			"update_time": createMS,
		},
	}
}

// WrapKlingV3Query 构造可灵 3.0 GET /tasks?task_ids=... 查询响应。
func WrapKlingV3Query(views []OfficialTaskView) map[string]any {
	items := make([]map[string]any, 0, len(views))
	reqID := ""
	for _, v := range views {
		if reqID == "" {
			reqID = v.TaskID
		}
		status := "processing"
		switch v.Status {
		case "queued":
			status = "submitted"
		case "succeeded":
			status = "succeeded"
		case "failed", "cancelled":
			status = "failed"
		}
		createMS := v.SubmittedAt.UnixMilli()
		if createMS <= 0 {
			createMS = time.Now().UnixMilli()
		}
		updateMS := createMS
		if v.FinishedAt != nil {
			updateMS = v.FinishedAt.UnixMilli()
		}
		durStr := "5"
		if v.Duration > 0 {
			durStr = strconvI64(v.Duration)
		}
		item := map[string]any{
			"id":          v.TaskID,
			"task_id":     v.TaskID,
			"status":      status,
			"message":     v.FailReason,
			"create_time": createMS,
			"update_time": updateMS,
		}
		if status == "succeeded" && v.ResultURL != "" {
			item["outputs"] = []map[string]any{
				{
					"type":     "video",
					"id":       v.TaskID + "_0",
					"url":      v.ResultURL,
					"duration": durStr,
				},
			}
		}
		items = append(items, item)
	}
	return map[string]any{
		"code":       0,
		"message":    "SUCCEED",
		"request_id": reqID,
		"data":       items,
		"tasks":      items,
	}
}

// WrapDashScopeCreate 构造阿里百炼原生视频提交响应。
func WrapDashScopeCreate(v OfficialTaskView) map[string]any {
	return map[string]any{
		"request_id": v.TaskID,
		"output": map[string]any{
			"task_id":     v.TaskID,
			"task_status": "PENDING",
		},
	}
}

// WrapDashScopeQuery 构造阿里百炼原生 GET /api/v1/tasks/:id 查询响应。
func WrapDashScopeQuery(v OfficialTaskView) map[string]any {
	taskStatus := "RUNNING"
	switch v.Status {
	case "queued":
		taskStatus = "PENDING"
	case "succeeded":
		taskStatus = "SUCCEEDED"
	case "failed":
		taskStatus = "FAILED"
	case "cancelled":
		taskStatus = "CANCELED"
	}
	output := map[string]any{
		"task_id":     v.TaskID,
		"task_status": taskStatus,
	}
	if taskStatus == "SUCCEEDED" && v.ResultURL != "" {
		output["video_url"] = v.ResultURL
	}
	if (taskStatus == "FAILED" || taskStatus == "CANCELED") && v.FailReason != "" {
		output["code"] = "TaskFailed"
		output["message"] = v.FailReason
	}
	resp := map[string]any{
		"request_id": v.TaskID,
		"output":     output,
	}
	if taskStatus == "SUCCEEDED" && (v.Duration > 0 || v.CompletionTokens > 0) {
		resp["usage"] = map[string]any{
			"video_duration":    max(int64(5), v.Duration),
			"completion_tokens": v.CompletionTokens,
		}
	}
	return resp
}

func strconvI64(n int64) string {
	const digits = "0123456789"
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = digits[n%10]
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
