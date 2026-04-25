package indexer

import (
	"fmt"
	"io"
	"os"
	"sync"

	"golang.org/x/term"
)

// progressThreshold is the file-count cutoff below which the progress helper
// stays in verbose (per-file-line) mode. Small projects get the familiar
// `[N/M] path OK` output; larger projects switch to a progress bar (TTY) or
// a periodic heartbeat (non-TTY).
const progressThreshold = 500

// progressHeartbeatEvery is how often quiet-mode emits a status line. 100 is
// frequent enough that a stuck run becomes obvious within a second or two,
// infrequent enough that CI logs stay readable.
const progressHeartbeatEvery = 100

// progressMode dictates how an indexProgress reports per-file completion.
//
// The iota values are an implementation detail — they are never compared
// numerically and carry no ordering meaning. Listing here goes
// verbose → bar → quiet → silent, which is roughly "loudest to quietest",
// but that ordering is only informative; code that reads a progressMode
// only switches on exact equality.
type progressMode int

const (
	// progressVerbose prints one line per file: `  [N/M] path OK/ERROR`.
	// Default for small projects; matches the pre-bar behaviour of both
	// passes.
	progressVerbose progressMode = iota

	// progressBar rewrites a single line in place via \r, showing
	// `[N/M] pct% — path`. Only usable on a TTY — tested via term.IsTerminal.
	progressBar

	// progressQuiet emits a heartbeat line every progressHeartbeatEvery
	// files. Used when stdout is not a terminal (CI, redirected output)
	// and the file count is large enough that per-file lines would flood
	// logs but silence would look stuck.
	progressQuiet

	// progressSilent emits nothing. Used by `librarian index --json` so
	// no progress chatter pollutes the JSON blob on stdout.
	progressSilent
)

// indexProgress is the goroutine-safe progress reporter used by both the
// docs pass (IndexDirectory) and the graph pass (IndexProjectGraph). The
// graph pass's worker pool calls tick concurrently; the docs pass ticks
// serially. finish flushes a trailing newline in bar mode so the terminal
// cursor doesn't land in the middle of a rewritten line.
//
// out is normally os.Stdout; tests inject a buffer. isTTY is set at
// construction time based on the selected mode — tests don't need to stub
// term.IsTerminal.
type indexProgress struct {
	mu    sync.Mutex
	mode  progressMode
	total int
	done  int
	out   io.Writer
}

// newIndexProgress auto-selects a mode based on total file count and whether
// os.Stdout is a TTY, unless override is non-empty — in which case that mode
// is forced. Valid overrides: "verbose", "bar", "quiet". Empty = auto.
func newIndexProgress(total int, override string) *indexProgress {
	return newIndexProgressWith(total, os.Stdout, isTTY(os.Stdout), override)
}

// newIndexProgressWith is the test-facing constructor — lets unit tests pick
// a mode by directly providing (out, tty, override) without touching real
// stdout. Unknown override values silently fall back to auto-select.
func newIndexProgressWith(total int, out io.Writer, tty bool, override string) *indexProgress {
	var mode progressMode
	switch override {
	case "verbose":
		mode = progressVerbose
	case "bar":
		mode = progressBar
	case "quiet":
		mode = progressQuiet
	case "silent":
		mode = progressSilent
	default:
		// Auto-select: small projects get per-file lines; large projects
		// get a bar on TTY / heartbeat on non-TTY.
		switch {
		case total <= progressThreshold:
			mode = progressVerbose
		case tty:
			mode = progressBar
		default:
			mode = progressQuiet
		}
	}
	return &indexProgress{mode: mode, total: total, out: out}
}

// tick records a single file's completion. path is rendered in verbose and
// bar output so the user can see which file just finished; it's ignored by
// quiet mode. hadError increments an internal error counter and changes the
// verbose-mode tag from `OK` to `ERROR`.
//
// Safe to call concurrently from multiple goroutines.
func (p *indexProgress) tick(path string, hadError bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.done++

	switch p.mode {
	case progressVerbose:
		tag := "OK"
		if hadError {
			tag = "ERROR"
		}
		fmt.Fprintf(p.out, "  [%d/%d] %s %s\n", p.done, p.total, path, tag)

	case progressBar:
		// \r rewrites the same line; trailing space overwrites any longer
		// previous path so the line doesn't smear leftover characters.
		// Truncate long paths to keep the line under ~80 chars in total.
		percent := 0
		if p.total > 0 {
			percent = (100 * p.done) / p.total
		}
		pathDisplay := truncatePathForProgress(path, 60)
		fmt.Fprintf(p.out, "\r  [%d/%d] %d%% — %s   ", p.done, p.total, percent, pathDisplay)

	case progressQuiet:
		if p.done == 1 || p.done%progressHeartbeatEvery == 0 || p.done == p.total {
			fmt.Fprintf(p.out, "  %d/%d\n", p.done, p.total)
		}

	case progressSilent:
		// Intentional no-op — caller wants clean stdout for JSON / machine
		// consumption.
	}
}

// finish is called once after every tick has landed. In bar mode it writes
// the trailing newline so the next output starts cleanly; in verbose and
// quiet modes it's a no-op.
func (p *indexProgress) finish() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.mode == progressBar {
		fmt.Fprintln(p.out)
	}
}

// isTTY reports whether f is an interactive terminal. Factored out so tests
// can construct indexProgress without touching stdout.
func isTTY(f *os.File) bool {
	return term.IsTerminal(int(f.Fd()))
}

// truncatePathForProgress clips path to max visual chars with a leading
// ellipsis when it's too long. We clip from the left (keeping the filename)
// because that's the part the user actually recognises mid-run — the prefix
// directories repeat across many files and are less informative.
//
// Slicing is rune-based, not byte-based — paths containing non-ASCII
// characters (CJK, accented letters, emoji) would otherwise split mid-rune
// and produce malformed UTF-8 in the terminal output.
func truncatePathForProgress(path string, max int) string {
	runes := []rune(path)
	if len(runes) <= max {
		return path
	}
	if max <= 1 {
		return string(runes[len(runes)-max:])
	}
	return "…" + string(runes[len(runes)-(max-1):])
}
