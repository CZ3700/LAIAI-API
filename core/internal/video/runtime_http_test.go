package video

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/videoruntime"
)

func TestRuntimeHTTPErrorIsStableAndNotRetryable(t *testing.T) {
	for _, err := range []error{videoruntime.ErrUnsupported, invocation.ErrVideoSchemaUnsupported, errors.Join(videoruntime.ErrUnsupported, errors.New("private-provider-text"))} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Set("video_request_id", "request-runtime-test")
		respondError(c, err)
		var body struct {
			RequestID string `json:"request_id"`
			Error     struct {
				Code      string `json:"code"`
				Message   string `json:"message"`
				Retryable bool   `json:"retryable"`
			} `json:"error"`
		}
		if json.Unmarshal(w.Body.Bytes(), &body) != nil || w.Code != 409 || body.RequestID != "request-runtime-test" || body.Error.Code != "runtime_contract_unsupported" || body.Error.Message != "runtime contract unsupported" || body.Error.Retryable {
			t.Fatalf("unsafe runtime error response: %d %s", w.Code, w.Body.String())
		}
	}
}
