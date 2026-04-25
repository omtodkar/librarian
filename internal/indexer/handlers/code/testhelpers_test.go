package code_test

import (
	"librarian/internal/indexer"
)

// findUnit returns the first Unit with the given Title, or nil. Shared across
// per-language test files so each file doesn't need its own lookup loop.
func findUnit(doc *indexer.ParsedDoc, title string) *indexer.Unit {
	for i := range doc.Units {
		if doc.Units[i].Title == title {
			return &doc.Units[i]
		}
	}
	return nil
}

// importTargets returns the set of Targets for all Kind="import" references.
// Used by grammar tests that want to assert "these imports were recognised"
// without caring about order or metadata.
func importTargets(doc *indexer.ParsedDoc) map[string]bool {
	out := map[string]bool{}
	for _, r := range doc.Refs {
		if r.Kind == "import" {
			out[r.Target] = true
		}
	}
	return out
}

// hasSignal reports whether signals contains an entry matching kind + value.
// Used by grammar tests asserting that annotation/rationale signals surfaced
// on a specific Unit.
func hasSignal(signals []indexer.Signal, kind, value string) bool {
	for _, s := range signals {
		if s.Kind == kind && s.Value == value {
			return true
		}
	}
	return false
}

// inheritsRefsBySource filters a doc's References to Kind="inherits" entries
// whose Source matches the given symbol Path. Shared across per-language
// inheritance tests so each file doesn't re-declare the same 6-line loop.
// Java's multi-class fixtures need a map-keyed-by-(Source,Target) variant —
// those tests define their own local helper.
func inheritsRefsBySource(doc *indexer.ParsedDoc, source string) []indexer.Reference {
	var out []indexer.Reference
	for _, r := range doc.Refs {
		if r.Kind == "inherits" && r.Source == source {
			out = append(out, r)
		}
	}
	return out
}
