package install

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
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
	if err != nil && err != io.EOF {
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

// isTerminal reports whether r is a terminal. Only *os.File can be a terminal;
// anything else (pipes, bytes.Buffer in tests) is treated as non-tty. We detect
// character-device mode from the underlying FileInfo to avoid pulling in
// golang.org/x/term just for this one check.
func isTerminal(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
