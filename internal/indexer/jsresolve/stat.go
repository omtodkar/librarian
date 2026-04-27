package jsresolve

import "os"

// StatIsFile is the production statFn — true iff the path exists and is a
// regular file (directories must not satisfy a module-specifier probe).
func StatIsFile(p string) bool {
	info, err := os.Stat(p)
	if err != nil {
		return false
	}
	return !info.IsDir()
}
