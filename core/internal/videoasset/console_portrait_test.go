package videoasset

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestConsolePortraitSeparatesCallbackAndReturn(t *testing.T) {
	s, db, _, provider := setup(t)
	ctx := context.Background()
	if err := s.ConfigureConsole(ConsoleOptions{CallbackURL: s.options.PlatformCallbackURL}); !errors.Is(err, ErrInvalid) {
		t.Fatal("callback receiver accepted as return page", err)
	}
	if err := s.ConfigureConsole(ConsoleOptions{CallbackURL: "https://customer.example/return"}); err != nil {
		t.Fatal(err)
	}
	if !s.ConsoleCapabilities(ctx, 1).Portrait {
		t.Fatal("registered return page should enable portrait")
	}
	c := CreateContext{CallerKind: "session_user", OrgID: 1, UserID: 22, RequestID: "console-portrait", IdempotencyKey: "console-portrait"}
	p, _, err := s.CreateConsolePortrait(ctx, c, PortraitCreate{Name: "Portrait", CallbackURL: "https://untrusted.example/ignored"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ProcessOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(provider.callbackURL, s.options.PlatformCallbackURL+"?state=") || strings.Contains(provider.callbackURL, "customer.example") {
		t.Fatal("provider must receive the state-bound callback receiver")
	}
	router := gin.New()
	router.GET(PortraitCallbackPath, s.HandlePortraitCallback)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, provider.callbackURL+"&resultCode=10000", nil))
	if recorder.Code != http.StatusSeeOther || recorder.Header().Get("Location") != "https://customer.example/return" {
		t.Fatal("callback must redirect to registered console return page", recorder.Code)
	}
	var row Portrait
	if err := db.Where("session_id = ?", p.ID).Take(&row).Error; err != nil || row.CallbackUsedAt == nil || row.SessionStatus == "succeeded" {
		t.Fatal("callback must consume state without trusting browser certification result", err)
	}
}

func TestConsolePortraitRequiresLiveRegisteredReturn(t *testing.T) {
	for _, missing := range []string{"platform_callback", "return_page", "registration", "revoked_registration"} {
		t.Run(missing, func(t *testing.T) {
			s, db, _, _ := setup(t)
			ctx := context.Background()
			if err := s.ConfigureConsole(ConsoleOptions{CallbackURL: "https://customer.example/return"}); err != nil {
				t.Fatal(err)
			}
			switch missing {
			case "platform_callback":
				s.options.PlatformCallbackURL = ""
			case "return_page":
				s.console.CallbackURL = ""
			case "registration":
				s.options.ResolveCallback = func(context.Context, uint64, string) (CallbackTarget, error) { return CallbackTarget{}, ErrNotFound }
			case "revoked_registration":
				s.options.ResolveCallbackByID = func(context.Context, uint64, string) (CallbackTarget, error) { return CallbackTarget{}, ErrNotFound }
			}
			if s.ConsoleCapabilities(ctx, 1).Portrait {
				t.Fatal("unready portrait advertised")
			}
			if _, _, err := s.CreateConsolePortrait(ctx, createContext(missing), PortraitCreate{Name: "Portrait"}); !errors.Is(err, ErrUnsupported) {
				t.Fatal("unready portrait created", err)
			}
			var operations int64
			if err := db.Model(&Operation{}).Count(&operations).Error; err != nil || operations != 0 {
				t.Fatal("failed readiness created provider operation", operations, err)
			}
		})
	}
}
