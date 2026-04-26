// Package tree_sitter_dart ships the tree-sitter-dart grammar as an
// in-tree vendor.
//
// Source:  github.com/UserNobody14/tree-sitter-dart
// Commit:  0fc19c3a57 (2026-03-14, master)
// ABI:     15
//
// In-tree vendor because the upstream Go module declares its path as
// `github.com/tree-sitter/tree-sitter-dart` (a repo that does not exist)
// but lives at `github.com/UserNobody14/...` — `go get` refuses the
// path mismatch. Vendoring sidesteps the broken module declaration.
//
// Covers Dart 3 declarations — sealed/base/final/interface class
// modifiers, records, patterns, enhanced enums, extension types,
// Dart 3.10 dot shorthand on enums and constructors. Known
// expression-level parse quirks (upstream issues #72 loop-labels-
// after-imports, #54 abstract-final) don't affect declaration-level
// symbol extraction, which is all librarian consumes.
//
// ABI 15 requires `github.com/tree-sitter/go-tree-sitter` runtime;
// the older `smacker/go-tree-sitter` (ABI 13-14 only) cannot load
// this grammar.
package tree_sitter_dart

// #cgo CFLAGS: -std=c11 -fPIC
// #include "tree_sitter/parser.h"
// TSLanguage *tree_sitter_dart();
import "C"

import "unsafe"

// Language returns the tree-sitter Language pointer. Wrap with
// sitter.NewLanguage(...) at the call site.
func Language() unsafe.Pointer {
	return unsafe.Pointer(C.tree_sitter_dart())
}
