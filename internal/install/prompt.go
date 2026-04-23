package install

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// selectPlatforms is the interactive checklist shown when `librarian install` is
// run with no --all / --platforms flag. It prints one line per platform with
// [x] pre-checked for detected platforms, reads a single line from stdin like
// "1,3", and returns the selected set.
//
// Non-TTY stdin is a hard error — scripted callers must pass --all or
// --platforms explicitly so intent stays unambiguous.
func selectPlatforms(in io.Reader, out io.Writer, platforms []*Platform, root string) ([]*Platform, error) {
	if !isTerminal(in) {
		return nil, fmt.Errorf("non-interactive stdin: pass --all or --platforms=<list> to scripted installs")
	}

	defaults := map[int]bool{}
	for i, p := range platforms {
		if p.Detected != nil && p.Detected(root) {
			defaults[i] = true
		}
	}

	fmt.Fprintln(out, "Install Librarian integrations for:")
	for i, p := range platforms {
		mark := " "
		if defaults[i] {
			mark = "x"
		}
		suffix := ""
		if defaults[i] {
			suffix = "  (detected)"
		}
		fmt.Fprintf(out, "  [%s] %d) %s%s\n", mark, i+1, p.Name, suffix)
	}
	fmt.Fprintln(out)
	fmt.Fprint(out, "Comma-separated numbers (blank = install detected, 'a' = all, 'n' = none): ")

	reader := bufio.NewReader(in)
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		// Ctrl-C and similar user interrupts arrive as syscall.EINTR or
		// ErrUnexpectedEOF; surface a clean message instead of a raw errno.
		if errors.Is(err, io.ErrUnexpectedEOF) || strings.Contains(err.Error(), "interrupted") {
			return nil, fmt.Errorf("install cancelled")
		}
		return nil, fmt.Errorf("reading selection: %w", err)
	}
	line = strings.TrimSpace(line)

	picked := map[int]bool{}
	switch strings.ToLower(line) {
	case "":
		picked = defaults
	case "a", "all":
		for i := range platforms {
			picked[i] = true
		}
	case "n", "none":
		// leave empty
	default:
		for _, tok := range strings.Split(line, ",") {
			tok = strings.TrimSpace(tok)
			if tok == "" {
				continue
			}
			var n int
			if _, err := fmt.Sscanf(tok, "%d", &n); err != nil || n < 1 || n > len(platforms) {
				return nil, fmt.Errorf("invalid selection %q", tok)
			}
			picked[n-1] = true
		}
	}

	out2 := make([]*Platform, 0, len(picked))
	for i, p := range platforms {
		if picked[i] {
			out2 = append(out2, p)
		}
	}
	return out2, nil
}

// isTerminal reports whether r is an interactive terminal. Uses golang.org/x/term
// (tcgetattr / GetConsoleMode under the hood) so /dev/null and similar character
// devices correctly fail the check — a prior FileInfo-based heuristic let
// `librarian install < /dev/null` silently install for detected platforms.
func isTerminal(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}
