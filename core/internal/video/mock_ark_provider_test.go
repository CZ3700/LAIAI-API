package video

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// This transport never delegates to another RoundTripper, dials, or resolves
// DNS. Keeping the official URL exercises production request construction
// without making an official request or installing a production endpoint hook.
type mockArkProvider struct {
	mu         sync.Mutex
	generation time.Duration
	queryDelay time.Duration
	tasks      map[string]mockArkTask
	requests   []mockArkRequest
	rejected   int
}

type mockArkTask struct {
	ID          string    `json:"id"`
	AcceptedAt  time.Time `json:"accepted_at"`
	RunningAt   time.Time `json:"running_at"`
	SucceededAt time.Time `json:"succeeded_at"`
}

type mockArkRequest struct {
	Method  string    `json:"method"`
	TaskID  string    `json:"task_id"`
	Started time.Time `json:"started_at"`
	Ended   time.Time `json:"ended_at"`
	Status  string    `json:"returned_status,omitempty"`
}

func newMockArkProvider(generation, queryDelay time.Duration) *mockArkProvider {
	return &mockArkProvider{generation: generation, queryDelay: queryDelay, tasks: map[string]mockArkTask{}}
}

func (m *mockArkProvider) RoundTrip(r *http.Request) (*http.Response, error) {
	const path = "/api/v3/contents/generations/tasks"
	valid := r.URL != nil && r.URL.Scheme == "https" && r.URL.Host == "ark.cn-beijing.volces.com" && r.URL.User == nil && r.URL.RawQuery == "" && r.URL.Fragment == ""
	if !valid || (r.Method != http.MethodPost && r.Method != http.MethodGet) ||
		(r.Method == http.MethodPost && r.URL.Path != path) ||
		(r.Method == http.MethodGet && !strings.HasPrefix(r.URL.Path, path+"/")) {
		m.mu.Lock()
		m.rejected++
		m.mu.Unlock()
		return nil, errors.New("mock Ark rejected an unexpected destination or operation")
	}
	if err := r.Context().Err(); err != nil {
		return nil, err
	}
	started := time.Now()
	if r.Method == http.MethodPost {
		// Read a bounded body, but never retain or print prompts/headers.
		body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024+1))
		if err != nil || len(body) > 64*1024 || !json.Valid(body) {
			return nil, errors.New("mock Ark rejected an invalid request body")
		}
		m.mu.Lock()
		id := fmt.Sprintf("mock_%04d", len(m.tasks)+1)
		task := mockArkTask{ID: id, AcceptedAt: started, RunningAt: started.Add(time.Second), SucceededAt: started.Add(m.generation)}
		m.tasks[id] = task
		m.requests = append(m.requests, mockArkRequest{Method: r.Method, TaskID: id, Started: started, Ended: time.Now()})
		m.mu.Unlock()
		return reply(200, `{"id":"`+id+`"}`), nil
	}
	id := strings.TrimPrefix(r.URL.Path, path+"/")
	m.mu.Lock()
	task, found := m.tasks[id]
	m.mu.Unlock()
	if !found {
		return reply(404, `{"error":{"code":"NotFound"}}`), nil
	}
	if err := mockArkWait(r.Context(), m.queryDelay); err != nil {
		return nil, err
	}
	now := time.Now()
	status := "queued"
	if !now.Before(task.SucceededAt) {
		status = "succeeded"
	} else if !now.Before(task.RunningAt) {
		status = "running"
	}
	out := map[string]any{"id": id, "status": status}
	if status == "succeeded" {
		out["usage"] = map[string]any{"completion_tokens": 100, "total_tokens": 100}
		// This URL is only returned and encrypted. Neither the provider transport
		// nor the baseline downloads media. A valid capsule prevents API reads
		// from triggering delivery refresh queries and skewing worker metrics.
		out["content"] = map[string]any{"video_url": "https://cdn.example.com/mock/" + id + ".mp4"}
	}
	body, _ := json.Marshal(out)
	m.mu.Lock()
	m.requests = append(m.requests, mockArkRequest{Method: r.Method, TaskID: id, Started: started, Ended: now, Status: status})
	m.mu.Unlock()
	return reply(200, string(body)), nil
}

func mockArkWait(ctx context.Context, delay time.Duration) error {
	t := time.NewTimer(delay)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (m *mockArkProvider) snapshot() (map[string]mockArkTask, []mockArkRequest, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	tasks := make(map[string]mockArkTask, len(m.tasks))
	for id, task := range m.tasks {
		tasks[id] = task
	}
	return tasks, append([]mockArkRequest(nil), m.requests...), m.rejected
}

func TestMockArkProviderNeverFallsBackToNetwork(t *testing.T) {
	m := newMockArkProvider(time.Second, 0)
	for _, target := range []string{"https://example.com/api/v3/contents/generations/tasks", "http://ark.cn-beijing.volces.com/api/v3/contents/generations/tasks", "https://ark.cn-beijing.volces.com/api/v3/unexpected"} {
		r, _ := http.NewRequest(http.MethodPost, target, strings.NewReader(`{}`))
		if _, err := m.RoundTrip(r); err == nil {
			t.Fatal("unexpected destination accepted")
		}
	}
	_, requests, rejected := m.snapshot()
	if rejected != 3 || len(requests) != 0 {
		t.Fatal("rejected calls must never become provider operations")
	}
}

func TestMockArkProviderTimeAndCancellation(t *testing.T) {
	m := newMockArkProvider(0, 0)
	create, _ := http.NewRequest(http.MethodPost, "https://ark.cn-beijing.volces.com/api/v3/contents/generations/tasks", strings.NewReader(`{"model":"synthetic"}`))
	response, err := m.RoundTrip(create)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	get, _ := http.NewRequest(http.MethodGet, "https://ark.cn-beijing.volces.com/api/v3/contents/generations/tasks/mock_0001", nil)
	response, err = m.RoundTrip(get)
	if err != nil {
		t.Fatal(err)
	}
	var result struct{ ID, Status string }
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if result.ID != "mock_0001" || result.Status != "succeeded" {
		t.Fatal("mock state must follow its configured transition time")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.RoundTrip(get.WithContext(ctx)); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled mock requests must not wait or produce observations")
	}
	states, requests, _ := m.snapshot()
	if len(states) != 1 || len(requests) != 2 || !states["mock_0001"].SucceededAt.Equal(states["mock_0001"].AcceptedAt) {
		t.Fatal("mock transitions must be scheduled at admission independently of queries")
	}
}
