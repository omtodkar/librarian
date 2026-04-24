// Package defaults blank-imports every built-in FileHandler package so a
// single blank-import of this package at the call site (cmd/, mcpserver/)
// wires up the full handler set.
//
// Adding a new handler package means one new blank-import here, not one new
// blank-import in every place that constructs an indexer.
package defaults

import (
	_ "librarian/internal/indexer/handlers/code"     // register code grammars (Go, ...)
	_ "librarian/internal/indexer/handlers/config"   // register config handlers (YAML/JSON/...)
	_ "librarian/internal/indexer/handlers/markdown" // register markdown handler
	_ "librarian/internal/indexer/handlers/office"   // register DOCX/XLSX/PPTX handlers
)
