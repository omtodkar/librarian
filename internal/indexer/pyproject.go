package indexer

import (
	"fmt"
	"os"
	"path/filepath"

	toml "github.com/pelletier/go-toml/v2"
)

// pyprojectTOML is the narrow subset of pyproject.toml keys consumed by
// src-root auto-detection. go-toml/v2 tolerates missing tables — absent
// keys leave struct fields at their zero value — so every sub-struct is
// safe to dereference unconditionally after Unmarshal.
//
// Supported layouts:
//   - setuptools: [tool.setuptools.packages.find] where = ["src"]
//   - setuptools: [tool.setuptools] package-dir = { "" = "src" }  (pre-find idiom)
//   - Poetry:     [[tool.poetry.packages]] include = "foo", from = "src"
//   - Hatch:      [tool.hatch.build.targets.{wheel,sdist}] packages = ["src/foo"]
//
// Flit, PDM, Rye, maturin: out of scope. Users with those layouts still
// have explicit python.src_roots in config.
type pyprojectTOML struct {
	Tool struct {
		Setuptools struct {
			// PackageDir maps a dotted-name prefix to an on-disk directory.
			// The `""` key — "all top-level packages live under this dir" —
			// is the canonical src-layout idiom and predates packages.find.
			// Other keys (rare) map subpackages; we only read `""`.
			PackageDir map[string]string `toml:"package-dir"`
			Packages   struct {
				Find struct {
					Where []string `toml:"where"`
				} `toml:"find"`
			} `toml:"packages"`
		} `toml:"setuptools"`

		Poetry struct {
			Packages []struct {
				Include string `toml:"include"`
				From    string `toml:"from"`
			} `toml:"packages"`
		} `toml:"poetry"`

		Hatch struct {
			Build struct {
				Targets struct {
					// Wheel and Sdist accept the same `packages` key. Projects
					// that publish source-only (no wheel) configure only sdist;
					// read both so we don't miss either shape.
					Wheel struct {
						Packages []string `toml:"packages"`
					} `toml:"wheel"`
					Sdist struct {
						Packages []string `toml:"packages"`
					} `toml:"sdist"`
				} `toml:"targets"`
			} `toml:"build"`
		} `toml:"hatch"`
	} `toml:"tool"`
}

// detectPythonSrcRootsFromPyproject reads <projectRoot>/pyproject.toml and
// returns src-root directories implied by setuptools / Poetry / Hatch
// configuration, each joined onto projectRoot and filepath.Clean'd to an
// absolute path.
//
// Behaviour:
//   - No pyproject.toml: returns (nil, nil). Silent.
//   - Malformed TOML: returns (nil, err). Caller decides to log + continue.
//   - Parsed but no recognised tool tables: returns (nil, nil). Silent —
//     common when pyproject.toml only declares [project] metadata with no
//     build-backend-specific package layout.
//
// Translation rules:
//   - setuptools.package-dir = {"" = "src"}               → src/
//   - setuptools.packages.find.where = ["src", "libs"]    → src/, libs/
//   - poetry.packages = [{include = "foo", from = "src"}] → src/ (one entry)
//   - poetry.packages = [{include = "foo"}]               → . (dropped;
//     root-layout packages fall back to the __init__.py walk tier)
//   - hatch.build.targets.{wheel,sdist}.packages          → parent of each
//     listed package. Root-layout ("mypkg") entries are dropped.
//
// Detected roots are deduped on their cleaned absolute path.
func detectPythonSrcRootsFromPyproject(projectRoot string) ([]string, error) {
	if projectRoot == "" {
		return nil, nil
	}
	path := filepath.Join(projectRoot, "pyproject.toml")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	var pp pyprojectTOML
	if err := toml.Unmarshal(data, &pp); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}

	seen := map[string]struct{}{}
	var out []string
	add := func(rel string) {
		if rel == "" || rel == "." {
			return
		}
		abs := rel
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(projectRoot, rel)
		}
		abs = filepath.Clean(abs)
		if _, ok := seen[abs]; ok {
			return
		}
		seen[abs] = struct{}{}
		out = append(out, abs)
	}

	// setuptools package-dir = {"" = "src"}: the empty key is the canonical
	// src-layout anchor. Other keys map subpackage dirs and aren't src_roots.
	if root, ok := pp.Tool.Setuptools.PackageDir[""]; ok {
		add(root)
	}
	for _, w := range pp.Tool.Setuptools.Packages.Find.Where {
		add(w)
	}
	for _, p := range pp.Tool.Poetry.Packages {
		add(p.From)
	}
	for _, p := range pp.Tool.Hatch.Build.Targets.Wheel.Packages {
		add(filepath.Dir(filepath.Clean(p)))
	}
	for _, p := range pp.Tool.Hatch.Build.Targets.Sdist.Packages {
		add(filepath.Dir(filepath.Clean(p)))
	}
	return out, nil
}
