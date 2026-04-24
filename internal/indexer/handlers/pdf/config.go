package pdf

// Config controls PDF conversion. Zero values yield DefaultConfig behaviour.
type Config struct {
	// MaxPages caps how many pages of a single PDF are extracted.
	// 0 means unlimited. Large books produce proportional chunks and
	// can dominate the index otherwise.
	MaxPages int
}

// DefaultConfig returns the baseline Config used by init-time registration.
// cmd/root.go overwrites this via indexer.RegisterDefault once user config
// has been loaded. The zero value is the default — MaxPages=0 means
// unlimited — so this is just Config{} with a documented name.
func DefaultConfig() Config { return Config{} }
