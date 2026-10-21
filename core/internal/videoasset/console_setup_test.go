package videoasset

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestConsoleSetupStateDoesNotExposeRuntimeURLs(t *testing.T) {
	var missing *Service
	if state, target := missing.ConsoleSetupState(); state != (ConsoleDeploymentState{}) || target != "" {
		t.Fatal("nil service readiness")
	}
	s := &Service{options: Options{PlatformCallbackURL: "https://gateway.example" + PortraitCallbackPath}}
	if err := s.ConfigureConsole(ConsoleOptions{CallbackURL: "https://console.example/return?secret=internal-only", Media: &MediaStore{}}); err != nil {
		t.Fatal(err)
	}
	state, target := s.ConsoleSetupState()
	if !state.ConsoleConfigured || !state.MediaConfigured || !state.PlatformCallbackConfigured || !state.PortraitReturnConfigured || state.PortraitTargetReady || target == "" {
		t.Fatal("deployment state falsely asserts registry readiness")
	}
	raw, err := json.Marshal(state)
	if err != nil || strings.Contains(string(raw), "https://") || strings.Contains(string(raw), "internal-only") {
		t.Fatal("runtime URL exposed")
	}
	// Defensive even if an older runtime mistakenly installed the callback
	// receiver as the browser's return page before ConfigureConsole rejected it.
	s.console.CallbackURL = s.options.PlatformCallbackURL
	state, _ = s.ConsoleSetupState()
	if state.PortraitReturnConfigured {
		t.Fatal("callback receiver treated as return page")
	}
}
