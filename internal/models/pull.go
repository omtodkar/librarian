package models

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/term"
)

// PullOptions tunes a Pull.
type PullOptions struct {
	// Force re-downloads every artifact even when already present.
	Force bool
	// Progress receives human-readable progress lines. nil → os.Stderr.
	// Set to io.Discard to silence (used by --json output).
	Progress io.Writer
	// Timeout bounds the whole pull. 0 → no deadline.
	Timeout time.Duration
}

// PullResult summarizes a completed Pull.
type PullResult struct {
	ModelID    string   `json:"model_id"`
	ModelDir   string   `json:"model_dir"`
	ORTLibPath string   `json:"ort_lib_path"`
	Files      []string `json:"files"`
	Skipped    bool     `json:"skipped"`
}

// Pull downloads a model's artifacts and its platform ONNX Runtime library
// into the shared cache, verifying every file's sha256. It is idempotent:
// already-present, verified artifacts are skipped unless opts.Force is set.
func Pull(ctx context.Context, modelID string, opts PullOptions) (PullResult, error) {
	m, ok := Lookup(modelID)
	if !ok {
		return PullResult{}, fmt.Errorf("unknown model %q; known models: %s", modelID, knownIDs())
	}

	out := opts.Progress
	if out == nil {
		out = os.Stderr
	}
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}

	modelDir, err := ModelDir(modelID)
	if err != nil {
		return PullResult{}, err
	}
	ortLib, err := ORTLibPath(modelID) // also validates platform support
	if err != nil {
		return PullResult{}, err
	}
	ortSpec := m.ORT.Libs[platformKey()]
	ortDestDir := filepath.Dir(ortLib)

	res := PullResult{ModelID: modelID, ModelDir: modelDir, ORTLibPath: ortLib}

	if !opts.Force && IsPulled(modelID) {
		res.Skipped = true
		for _, f := range m.Files {
			res.Files = append(res.Files, filepath.Join(modelDir, f.Name))
		}
		fmt.Fprintf(out, "%s already present in %s\n", modelID, modelDir)
		return res, nil
	}

	// Model artifacts.
	for _, spec := range m.Files {
		dest := filepath.Join(modelDir, spec.Name)
		if !opts.Force && fileExists(dest) {
			fmt.Fprintf(out, "  ✓ %s (cached)\n", spec.Name)
			res.Files = append(res.Files, dest)
			continue
		}
		if err := fetchOne(ctx, spec, modelDir, out); err != nil {
			return PullResult{}, err
		}
		res.Files = append(res.Files, dest)
	}
	if err := writeMarker(modelDir); err != nil {
		return PullResult{}, err
	}

	// ONNX Runtime shared library.
	if opts.Force || !fileExists(ortLib) {
		if err := fetchOne(ctx, ortSpec, ortDestDir, out); err != nil {
			return PullResult{}, err
		}
	} else {
		fmt.Fprintf(out, "  ✓ %s (cached)\n", ortSpec.Name)
	}
	if err := writeMarker(ortDestDir); err != nil {
		return PullResult{}, err
	}

	fmt.Fprintf(out, "Pulled %s → %s\n", modelID, modelDir)
	return res, nil
}

// fetchOne downloads a single spec with a progress bar labeled by name.
func fetchOne(ctx context.Context, spec FileSpec, destDir string, out io.Writer) error {
	fmt.Fprintf(out, "  ↓ %s\n", spec.Name)
	prog := newProgressPrinter(out, spec.Name)
	err := fetchFile(ctx, spec, destDir, prog.update)
	prog.done()
	if err != nil {
		return fmt.Errorf("fetching %s: %w", spec.Name, err)
	}
	return nil
}

func writeMarker(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, pulledMarker), []byte("ok\n"), 0o644)
}

func knownIDs() string {
	ids := ""
	for i, m := range List() {
		if i > 0 {
			ids += ", "
		}
		ids += m.ID
	}
	return ids
}

// progressPrinter renders byte progress. On a TTY it rewrites a single line
// with \r; otherwise it emits a periodic plain line (so logs stay readable).
type progressPrinter struct {
	out     io.Writer
	label   string
	isTTY   bool
	lastPct int
}

func newProgressPrinter(out io.Writer, label string) *progressPrinter {
	isTTY := false
	if f, ok := out.(*os.File); ok {
		isTTY = term.IsTerminal(int(f.Fd()))
	}
	return &progressPrinter{out: out, label: label, isTTY: isTTY, lastPct: -1}
}

func (p *progressPrinter) update(downloaded, total int64) {
	if total <= 0 {
		if p.isTTY {
			fmt.Fprintf(p.out, "\r    %s %.1f MB", p.label, float64(downloaded)/1e6)
		}
		return
	}
	pct := int(downloaded * 100 / total)
	if p.isTTY {
		fmt.Fprintf(p.out, "\r    %s %3d%% (%.1f/%.1f MB)", p.label, pct, float64(downloaded)/1e6, float64(total)/1e6)
		return
	}
	// Non-TTY: log every 25% so CI/file logs aren't spammed.
	if pct/25 != p.lastPct/25 {
		fmt.Fprintf(p.out, "    %s %d%%\n", p.label, pct)
		p.lastPct = pct
	}
}

func (p *progressPrinter) done() {
	if p.isTTY {
		fmt.Fprintln(p.out)
	}
}
