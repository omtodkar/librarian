// Package tree_sitter_swift ships the tree-sitter-swift grammar as an
// in-tree vendor.
//
// Grammar: alex-pinkus/tree-sitter-swift (upstream)
// Artifact source: frozen parser.c from the smacker/go-tree-sitter
//                  v0.0.0-20240827094217 swift/ subpackage (which
//                  itself is a CI snapshot of alex-pinkus)
// ABI:     14
//
// In-tree vendor because the upstream alex-pinkus repo doesn't check
// parser.c in — it regenerates from grammar.json in CI — and its
// go.mod declares a module path (`github.com/tree-sitter/tree-sitter-swift`,
// 404) that doesn't match where the repo lives, breaking `go get` even
// if the artifact were checked in. The smacker bundle shipped a frozen
// ABI-14 parser.c we mirror here; smacker itself is not a runtime
// dependency of librarian (runtime is `github.com/tree-sitter/go-tree-sitter`).
//
// ABI 14 is compatible with `github.com/tree-sitter/go-tree-sitter`
// (ABI 13-15). Upgrading this vendor to a newer parser.c requires
// obtaining a fresh build artifact from alex-pinkus CI or running
// `tree-sitter generate` locally against its grammar.json.
package tree_sitter_swift

// #cgo CFLAGS: -std=c11 -fPIC
// #include "tree_sitter/parser.h"
// TSLanguage *tree_sitter_swift();
import "C"

import "unsafe"

// Language returns the tree-sitter Language pointer. Wrap with
// sitter.NewLanguage(...) at the call site.
func Language() unsafe.Pointer {
	return unsafe.Pointer(C.tree_sitter_swift())
}
