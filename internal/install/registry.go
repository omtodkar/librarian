package install

// allPlatforms returns the canonical ordered list of supported assistant
// platforms. Order is preserved in the interactive prompt, flag parsing, and
// summary output. Adding a new platform = adding a *Platform entry here.
func allPlatforms() []*Platform {
	return []*Platform{
		claudePlatform(),
		codexPlatform(),
		cursorPlatform(),
		geminiPlatform(),
	}
}
