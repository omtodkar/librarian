// Package tree_sitter_sql ships the tree-sitter-sql grammar as an in-tree vendor.
//
// Source:  github.com/DerekStride/tree-sitter-sql
// Version: v0.3.11 (tag), commit 7b51ecda191d36b92f5a90a8d1bc3faef1c7b8b8
// ABI:     15
//
// In-tree vendor because the release tarball includes src/parser.c but the
// Go module zip does not — `go get github.com/DerekStride/tree-sitter-sql`
// would compile without the parser and fail at runtime. Vendoring the
// generated C sources sidesteps the broken module distribution.
//
// ABI 15 requires github.com/tree-sitter/go-tree-sitter (v0.25.0); the
// older smacker/go-tree-sitter (ABI 13–14 only) cannot load this grammar.
package tree_sitter_sql

// #cgo CFLAGS: -std=c11 -fPIC
// #include "tree_sitter/parser.h"
// TSLanguage *tree_sitter_sql();
import "C"

import "unsafe"

// Language returns the tree-sitter Language pointer. Wrap with
// sitter.NewLanguage(...) at the call site.
func Language() unsafe.Pointer {
	return unsafe.Pointer(C.tree_sitter_sql())
}
