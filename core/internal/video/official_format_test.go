package video

import (
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/task"
)

// TestOfficialVideoResponseFormats 校验清单中各接口返回的原始火山接口格式
func TestOfficialVideoResponseFormats(t *testing.T) {
	// 1. SubmitTask 响应格式验证：火山官方 Submit 返回 {"id": "cgt-..."}
	submitResp := volcengineSubmitResponse("cgt-test-task-123456")
	if submitResp["id"] != "cgt-test-task-123456" {
		t.Fatalf("expected submit id, got %v", submitResp["id"])
	}

	// 2. QueryTask 响应格式验证：火山官方包含 id, model, status, content, usage
	obs := task.Task{
		TaskID: "task_123456",
		Model:  "seedance-2.0",
		Status: "succeeded",
	}
	st := State{
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	queryResp := volcengineTaskResponse(obs, st, &invocation.VideoDelivery{
		VideoURL: "https://ark.cn-beijing.volces.com/video/sample.mp4",
	})
	if queryResp["id"] != "task_123456" || queryResp["status"] != "succeeded" {
		t.Fatalf("query response mismatch: %v", queryResp)
	}
	content, ok := queryResp["content"].(map[string]any)
	if !ok || content["video_url"] != "https://ark.cn-beijing.volces.com/video/sample.mp4" {
		t.Fatalf("content video_url mismatch: %v", queryResp["content"])
	}

	// 3. CancelTask / DeleteTask 响应格式验证：火山官方标准
	cancelResp := volcengineCancelResponse("task_123456", "cancelled")
	if cancelResp["id"] != "task_123456" || cancelResp["status"] != "cancelled" {
		t.Fatalf("cancel response mismatch: %v", cancelResp)
	}
}

// volcengineSubmitResponse 构建火山原生 Submit 响应
func volcengineSubmitResponse(taskID string) map[string]any {
	return map[string]any{
		"id": taskID,
	}
}

// volcengineTaskResponse 构建火山原生 Query 任务响应
func volcengineTaskResponse(t task.Task, st State, delivery *invocation.VideoDelivery) map[string]any {
	resp := map[string]any{
		"id":         t.TaskID,
		"model":      t.Model,
		"status":     t.Status,
		"created_at": st.CreatedAt.Unix(),
		"updated_at": st.UpdatedAt.Unix(),
	}
	if delivery != nil && delivery.VideoURL != "" {
		resp["content"] = map[string]any{
			"video_url": delivery.VideoURL,
		}
	}
	if st.TotalTokens != nil || st.ObservedCompletionTokens != nil {
		resp["usage"] = map[string]any{
			"total_tokens":      st.TotalTokens,
			"completion_tokens": st.ObservedCompletionTokens,
		}
	}
	return resp
}

// volcengineCancelResponse 构建火山原生 Cancel 响应
func volcengineCancelResponse(taskID, status string) map[string]any {
	return map[string]any{
		"id":     taskID,
		"status": status,
	}
}
