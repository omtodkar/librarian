// Package tree_sitter_tsx ships the tree-sitter-typescript grammar
// (TSX variant, parses .tsx files) as an in-tree vendor.
//
// Source:  github.com/tree-sitter/tree-sitter-typescript
// Commit:  75b3874edb (2025-01-30, master)
// ABI:     14
//
// In-tree vendor because the upstream checks parser.c into the repo
// under `tsx/src/` but ships no Go binding — there's nothing to
// `go get`. Shares the external scanner
// (`../common/scanner.h`) with the sibling TypeScript variant.
//
// ABI 14 is compatible with `github.com/tree-sitter/go-tree-sitter`
// (ABI 13-15).
package tree_sitter_tsx

// #cgo CFLAGS: -std=c11 -fPIC
// #include "tree_sitter/parser.h"
// TSLanguage *tree_sitter_tsx();
import "C"

import "unsafe"

// Language returns the tree-sitter Language pointer. Wrap with
// sitter.NewLanguage(...) at the call site.
func Language() unsafe.Pointer {
	return unsafe.Pointer(C.tree_sitter_tsx())
}
