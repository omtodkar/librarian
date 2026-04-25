package indexer

import (
	"bytes"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestProgress_ModeSelection pins the decision table documented on
// newIndexProgressWith: threshold → verbose; above threshold + TTY → bar;
// above threshold + non-TTY → quiet. Explicit override beats auto.
func TestProgress_ModeSelection(t *testing.T) {
	cases := []struct {
		name     string
		total    int
		tty      bool
		override string
		want     progressMode
	}{
		// Auto-select cases (no override)
		{"auto_small_tty", progressThreshold, true, "", progressVerbose},
		{"auto_small_non_tty", 10, false, "", progressVerbose},
		{"auto_at_threshold_tty", progressThreshold, true, "", progressVerbose},
		{"auto_at_threshold_non_tty", progressThreshold, false, "", progressVerbose},
		{"auto_large_tty_bar", progressThreshold + 1, true, "", progressBar},
		{"auto_large_non_tty_quiet", progressThreshold + 1, false, "", progressQuiet},
		{"auto_zero_total", 0, false, "", progressVerbose},

		// Override beats auto-select even when they conflict.
		{"override_verbose_on_large_tty", progressThreshold + 1, true, "verbose", progressVerbose},
		{"override_quiet_on_small_tty", 10, true, "quiet", progressQuiet},
		{"override_bar_on_non_tty", progressThreshold + 1, false, "bar", progressBar},
		{"override_bar_on_small", 10, true, "bar", progressBar},

		// Unknown override falls back to auto.
		{"unknown_override_falls_back", 10, false, "garbage", progressVerbose},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newIndexProgressWith(tc.total, &bytes.Buffer{}, tc.tty, tc.override)
			if p.mode != tc.want {
				t.Errorf("mode = %d; want %d", p.mode, tc.want)
			}
		})
	}
}

// TestProgress_VerboseEmitsPerFileLine pins the verbose format — per-file
// OK / ERROR line. This is the shape pre-PR-3 code relied on, so changes
// here are user-visible regressions.
func TestProgress_VerboseEmitsPerFileLine(t *testing.T) {
	var buf bytes.Buffer
	p := newIndexProgressWith(3, &buf, false, "")
	p.tick("a.go", false)
	p.tick("b.go", true)
	p.tick("c.go", false)
	p.finish()

	out := buf.String()
	wantLines := []string{
		"  [1/3] a.go OK",
		"  [2/3] b.go ERROR",
		"  [3/3] c.go OK",
	}
	for _, line := range wantLines {
		if !strings.Contains(out, line) {
			t.Errorf("verbose output missing %q; got:\n%s", line, out)
		}
	}
}

// TestProgress_BarUsesCarriageReturn pins that bar mode rewrites in place
// (\r-prefixed lines) and finish() emits a trailing newline so subsequent
// stdout writes start cleanly.
func TestProgress_BarUsesCarriageReturn(t *testing.T) {
	var buf bytes.Buffer
	// Bypass mode selection — construct bar mode directly for determinism.
	p := &indexProgress{mode: progressBar, total: 1000, out: &buf}

	p.tick("cmd/main.go", false)
	p.tick("internal/foo.go", false)

	out := buf.String()
	if !strings.Contains(out, "\r") {
		t.Errorf("bar mode must use \\r to rewrite; got %q", out)
	}
	if strings.Count(out, "\r") != 2 {
		t.Errorf("want 2 \\r (one per tick), got %d in %q", strings.Count(out, "\r"), out)
	}
	// finish appends one trailing newline.
	p.finish()
	if !strings.HasSuffix(buf.String(), "\n") {
		t.Errorf("finish should emit trailing newline; got %q", buf.String())
	}
}

// TestProgress_QuietHeartbeatEveryN pins that quiet mode emits at tick 1,
// at every progressHeartbeatEvery boundary, and at the final tick — no more
// than that, so CI logs don't flood.
func TestProgress_QuietHeartbeatEveryN(t *testing.T) {
	total := progressHeartbeatEvery*2 + 1 // 201 to hit tick 1, 100, 200, 201
	var buf bytes.Buffer
	p := &indexProgress{mode: progressQuiet, total: total, out: &buf}
	for i := 0; i < total; i++ {
		p.tick(fmt.Sprintf("f%d.go", i), false)
	}
	p.finish()

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	// Expected heartbeat ticks: 1, 100, 200, 201.
	expected := []string{
		"  1/201",
		"  100/201",
		"  200/201",
		"  201/201",
	}
	if len(lines) != len(expected) {
		t.Fatalf("quiet lines = %d, want %d; output:\n%s", len(lines), len(expected), buf.String())
	}
	for i, want := range expected {
		if lines[i] != want {
			t.Errorf("line %d = %q, want %q", i, lines[i], want)
		}
	}
}

// TestProgress_ConcurrentTickCountsCorrectly pins thread-safety: N
// goroutines call tick in parallel; the final done count must equal N and
// no data race detected by the race detector (run with -race).
func TestProgress_ConcurrentTickCountsCorrectly(t *testing.T) {
	const workers = 16
	const perWorker = 100
	total := workers * perWorker

	p := &indexProgress{mode: progressVerbose, total: total, out: &bytes.Buffer{}}

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				p.tick("file.go", false)
			}
		}()
	}
	wg.Wait()

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.done != total {
		t.Errorf("done = %d, want %d (lost ticks under concurrency)", p.done, total)
	}
}

// TestProgress_FinishIsNoOpInVerboseAndQuiet pins that finish does nothing
// in modes where no in-place rewriting is happening. We ensure it doesn't
// emit a stray blank line that might confuse downstream output parsing.
func TestProgress_FinishIsNoOpInVerboseAndQuiet(t *testing.T) {
	for _, mode := range []progressMode{progressVerbose, progressQuiet} {
		var buf bytes.Buffer
		p := &indexProgress{mode: mode, total: 1, out: &buf}
		p.tick("x.go", false)
		lenBefore := buf.Len()
		p.finish()
		if buf.Len() != lenBefore {
			t.Errorf("mode %d: finish() added %d bytes; want 0", mode, buf.Len()-lenBefore)
		}
	}
}

// TestAdaptiveWorkersForAvg pins the fast/medium/slow bucketing. These
// thresholds are the contract between per-file wall time and worker count;
// changes here alter real user behavior on large repos.
func TestAdaptiveWorkersForAvg(t *testing.T) {
	// Lower bound: cap at min(GOMAXPROCS, graphWorkerCap). On a multi-core
	// machine GOMAXPROCS usually >= 2, so the fast bucket's 2 survives the
	// cap. On single-core CI we'd clamp to 1 — still correct.
	cases := []struct {
		name     string
		avg      time.Duration
		wantMin  int // capWorkers means exact value depends on GOMAXPROCS
		wantMax  int
	}{
		{"fast_under_2ms", 500 * time.Microsecond, 1, 2},
		{"fast_at_boundary", 1999 * time.Microsecond, 1, 2},
		{"medium_2ms", 2 * time.Millisecond, 1, 4},
		{"medium_5ms", 5 * time.Millisecond, 1, 4},
		{"medium_just_under_10ms", 9999 * time.Microsecond, 1, 4},
		{"slow_10ms", 10 * time.Millisecond, 1, graphWorkerCap},
		{"slow_100ms", 100 * time.Millisecond, 1, graphWorkerCap},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := adaptiveWorkersForAvg(tc.avg)
			if got < tc.wantMin || got > tc.wantMax {
				t.Errorf("adaptiveWorkersForAvg(%v) = %d; want in [%d, %d]",
					tc.avg, got, tc.wantMin, tc.wantMax)
			}
		})
	}
}

// TestCapWorkers pins that requests above the cap clamp down, below 1
// clamp up, and values within the allowed range pass through.
func TestCapWorkers(t *testing.T) {
	cases := []struct{ in, wantMin, wantMax int }{
		{-5, 1, 1},
		{0, 1, 1},
		{1, 1, 1},
		{3, 1, 3},
		{graphWorkerCap * 2, 1, graphWorkerCap},
	}
	for _, tc := range cases {
		got := capWorkers(tc.in)
		if got < tc.wantMin || got > tc.wantMax {
			t.Errorf("capWorkers(%d) = %d; want in [%d, %d]",
				tc.in, got, tc.wantMin, tc.wantMax)
		}
	}
}

// TestTruncatePathForProgress pins that long paths are clipped from the
// left with an ellipsis so the filename (what the user is watching) stays
// visible.
func TestTruncatePathForProgress(t *testing.T) {
	cases := []struct {
		in   string
		max  int
		want string
	}{
		{"a.go", 10, "a.go"},
		{"very/long/path/to/a/file.go", 15, "…h/to/a/file.go"}, // "…" + 14 trailing = 15 visual chars
		{"exactmatch", 10, "exactmatch"},
		// With rune-based impl, max=2 means "…" + 1 trailing rune.
		{"tiny", 2, "…y"},
		// Multi-byte rune path — each 文 is 3 bytes but 1 visual char.
		// Byte-count slicing would split mid-rune and corrupt UTF-8;
		// with rune-based slicing max=6 means "…" + 5 trailing runes.
		{"路径/子目录/文件.go", 6, "…文件.go"},
	}
	for _, tc := range cases {
		got := truncatePathForProgress(tc.in, tc.max)
		if got != tc.want {
			t.Errorf("truncatePathForProgress(%q, %d) = %q; want %q", tc.in, tc.max, got, tc.want)
		}
	}
}
