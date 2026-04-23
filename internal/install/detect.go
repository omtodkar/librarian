package install

import "os"

// fileExists returns true if path exists (file or directory). Errors other than
// IsNotExist are treated as "doesn't exist" — detection is best-effort and
// should never fail the installer.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
