package video

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/ark"
)

func TestVideoErrorsExposeOnlySafeParameterNames(t *testing.T) {
	for _, param := range []string{"resolution", "secret-input-value", "ratio"} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Set("video_request_id", "request_fixture")
		respondError(c, &ark.Error{Code: "unsupported_capability", Field: param, Message: "secret-provider-message", HTTPStatus: 422})
		var out struct {
			Error     struct{ Code, Message, Param string }
			RequestID string `json:"request_id"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if w.Code != 422 || out.Error.Code != "unsupported_capability" || out.RequestID != "request_fixture" || strings.Contains(w.Body.String(), "secret-") {
			t.Fatal("unsafe error", w.Body.String())
		}
		if param == "resolution" && (out.Error.Param != "resolution" || !strings.Contains(out.Error.Message, "/v1/video/models")) {
			t.Fatal("missing actionable parameter")
		}
	}
}

func TestVideoPolicyErrorIsDistinctFromResourceAuthorization(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code string
	}{{errors.Join(ErrForbidden, ErrExecutionPolicy), "video_execution_not_enabled"}, {ErrForbidden, "forbidden"}} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		respondError(c, tc.err)
		if w.Code != 403 || !strings.Contains(w.Body.String(), `"code":"`+tc.code+`"`) {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if publicErrorCode("insufficient_scope", 403) != "insufficient_scope" || publicErrorCode("secret", 403) != "forbidden" {
		t.Fatal("scope contract")
	}
}
