package videoasset

import "net/url"

// ConsoleDeploymentState reports process dependencies only. It does not probe
// the provider or assert that the configured account has any official rights.
type ConsoleDeploymentState struct {
	URLRegistrationConfigured  bool `json:"url_registration_configured"`
	ConsoleConfigured          bool `json:"console_configured"`
	MediaConfigured            bool `json:"media_configured"`
	PlatformCallbackConfigured bool `json:"platform_callback_configured"`
	PortraitReturnConfigured   bool `json:"portrait_return_configured"`
	PortraitTargetReady        bool `json:"portrait_target_ready"`
	// Local wiring prerequisite used by video.AssetSetup before it checks the
	// tenant's live registry row. It is not a claim of credential verification.
	CallbackResolversConfigured bool `json:"-"`
}

// The second return value is an internal registry lookup input, never a response
// field. No credential, H5 session, signed URL or encrypted record is read here.
func (s *Service) ConsoleSetupState() (ConsoleDeploymentState, string) {
	if s == nil {
		return ConsoleDeploymentState{}, ""
	}
	out := ConsoleDeploymentState{
		URLRegistrationConfigured: s.console.enabled,
		ConsoleConfigured:         s.console.enabled, MediaConfigured: s.console.Media != nil,
		PlatformCallbackConfigured:  validPlatformCallback(s.options.PlatformCallbackURL),
		CallbackResolversConfigured: s.options.ResolveCallback != nil && s.options.ResolveCallbackByID != nil,
	}
	u, err := url.Parse(s.console.CallbackURL)
	out.PortraitReturnConfigured = err == nil && validHTTPS(s.console.CallbackURL, 2048) && u.Path != PortraitCallbackPath
	return out, s.console.CallbackURL
}
