package relay

// SupportsCatalogMode describes transport support for an explicitly configured
// documentation category. It does not assert model capabilities or alter routing.
func SupportsCatalogMode(ch Channel, mode RelayMode) bool {
	profile, known := ProfileFor(ch.Provider)
	if known && profile.TaskOnly {
		return false
	}
	if known && profile.Adapter == "replicate" {
		return mode == ModeImagesGenerations
	}
	if mode == ModeChat {
		return true
	}
	_, e := modeUpstreamSpec(&ch, mode, "")
	return e == nil
}
