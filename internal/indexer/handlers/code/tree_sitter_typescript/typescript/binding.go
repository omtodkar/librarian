// Package tree_sitter_typescript ships the tree-sitter-typescript grammar
// (TypeScript variant, parses .ts files) as an in-tree vendor.
//
// Source:  github.com/tree-sitter/tree-sitter-typescript
// Commit:  75b3874edb (2025-01-30, master)
// ABI:     14
//
// In-tree vendor because the upstream checks parser.c into the repo
// under `typescript/src/` but ships no Go binding — there's nothing
// to `go get`. Same pattern applies to the TSX variant in the sibling
// `tsx/` package; both share a common external scanner
// (`../common/scanner.h`).
//
// ABI 14 is compatible with `github.com/tree-sitter/go-tree-sitter`
// (ABI 13-15).
package tree_sitter_typescript

// #cgo CFLAGS: -std=c11 -fPIC
// #include "tree_sitter/parser.h"
// TSLanguage *tree_sitter_typescript();
import "C"

import "unsafe"

// Language returns the tree-sitter Language pointer. Wrap with
// sitter.NewLanguage(...) at the call site.
func Language() unsafe.Pointer {
	return unsafe.Pointer(C.tree_sitter_typescript())
}
