package office

// Config controls office-document conversion. Each Handler instance owns
// its Config by value (no package-level mutable state), so configuring the
// office handlers means re-constructing them: `office.NewXlsx(cfg)` after
// loading `.librarian/config.yaml` in cmd/root.go.
type Config struct {
	XLSXMaxRows         int  // per sheet
	XLSXMaxCols         int  // per row
	IncludeSpeakerNotes bool // emit PPTX notes-slide text as `### Notes`
}

// DefaultConfig returns the settings used by init-time handler
// registration and by any caller (tests, ad-hoc usage) that hasn't
// loaded a workspace config.
func DefaultConfig() Config {
	return Config{
		XLSXMaxRows:         100,
		XLSXMaxCols:         50,
		IncludeSpeakerNotes: true,
	}
}
