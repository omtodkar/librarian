package code_test

import (
	"strings"
	"testing"

	"librarian/internal/indexer"
	"librarian/internal/indexer/handlers/code"
)

const rustSample = `
// Package-level comment.
use std::io::{Read, Write};
use std::fmt as fmt_lib;
use std::collections::*;

/// Service provides HTTP handling.
pub struct Service {
    port: u32,
}

/// Status of a request.
pub enum Status {
    Ok,
    Err(String),
}

/// Doer can do things.
pub trait Doer: Read {
    fn do_it(&self) -> bool;
}

/// Inherent impl — methods should surface as method Units.
impl Service {
    /// Create a new Service.
    pub fn new(port: u32) -> Self {
        Service { port }
    }

    pub fn validate(&self) -> bool {
        true
    }
}

/// Trait impl — do_it inside impl should be a method.
impl Doer for Service {
    fn do_it(&self) -> bool {
        false
    }
}

/// Top-level function.
pub fn top_level(x: u32) -> u32 { x }

/// Type alias.
pub type MyAlias = u32;

/// Module with nested items.
mod my_mod {
    pub fn nested_fn() {}
}

#[cfg(test)]
mod tests {
    #[test]
    fn it_works() {}
}
`

func TestRustGrammar_Invariants(t *testing.T) {
	h := code.New(code.NewRustGrammar())
	code.AssertGrammarInvariants(t, h, "src/service.rs", []byte(rustSample))
}

func TestRustGrammar_SymbolKinds(t *testing.T) {
	h := code.New(code.NewRustGrammar())
	doc, err := h.Parse("src/service.rs", []byte(rustSample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if doc.Title != "service" {
		t.Errorf("Title = %q, want %q", doc.Title, "service")
	}
	if doc.Format != "rust" {
		t.Errorf("Format = %q, want %q", doc.Format, "rust")
	}

	wantKinds := map[string]string{
		"Service":   "class",
		"Status":    "enum",
		"Doer":      "interface",
		"do_it":     "method", // in trait body
		"new":       "method", // in impl block
		"validate":  "method", // in impl block
		"top_level": "function",
		"MyAlias":   "type",
		"nested_fn": "function",
		"it_works":  "function",
	}
	gotKinds := map[string]string{}
	for _, u := range doc.Units {
		gotKinds[u.Title] = u.Kind
	}
	for name, wantKind := range wantKinds {
		got, ok := gotKinds[name]
		if !ok {
			t.Errorf("missing Unit for %q (all: %v)", name, rustUnitTitles(doc))
			continue
		}
		if got != wantKind {
			t.Errorf("Unit %q Kind = %q, want %q", name, got, wantKind)
		}
	}
}

func TestRustGrammar_MethodPaths(t *testing.T) {
	h := code.New(code.NewRustGrammar())
	doc, err := h.Parse("src/service.rs", []byte(rustSample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	wantPaths := map[string]string{
		"Service":   "service.Service",
		"new":       "service.Service.new",
		"validate":  "service.Service.validate",
		"top_level": "service.top_level",
		"MyAlias":   "service.MyAlias",
		"nested_fn": "service.my_mod.nested_fn",
	}
	for _, u := range doc.Units {
		want, ok := wantPaths[u.Title]
		if !ok {
			continue
		}
		if u.Path != want {
			t.Errorf("Unit %q Path = %q, want %q", u.Title, u.Path, want)
		}
	}
}

func TestRustGrammar_Imports(t *testing.T) {
	h := code.New(code.NewRustGrammar())
	doc, err := h.Parse("src/service.rs", []byte(rustSample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	got := importTargets(doc)
	for _, want := range []string{
		"std::io::Read",
		"std::io::Write",
		"std::fmt",
		"std::collections::*",
	} {
		if !got[want] {
			t.Errorf("missing import %q (got: %v)", want, got)
		}
	}
	for _, r := range doc.Refs {
		if r.Target == "std::fmt" {
			if r.Metadata == nil || r.Metadata["alias"] != "fmt_lib" {
				t.Errorf("use std::fmt as fmt_lib: alias metadata = %v, want fmt_lib", r.Metadata)
			}
		}
	}
}

func TestRustGrammar_NestedGroupedImports(t *testing.T) {
	const src = `
use std::{io::{Read, Write as W}, fmt::Display};
use crate::module::Thing;
use super::util;
use self::local;
`
	h := code.New(code.NewRustGrammar())
	doc, err := h.Parse("src/lib.rs", []byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got := importTargets(doc)
	for _, want := range []string{
		"std::io::Read",
		"std::io::Write",
		"std::fmt::Display",
		"crate::module::Thing",
		"super::util",
		"self::local",
	} {
		if !got[want] {
			t.Errorf("missing import %q (got: %v)", want, got)
		}
	}
	for _, r := range doc.Refs {
		if r.Target == "std::io::Write" {
			if r.Metadata == nil || r.Metadata["alias"] != "W" {
				t.Errorf("Write as W: alias metadata = %v, want W", r.Metadata)
			}
		}
	}
}

func TestRustGrammar_TraitSuperBounds(t *testing.T) {
	h := code.New(code.NewRustGrammar())
	doc, err := h.Parse("src/service.rs", []byte(rustSample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	// trait Doer: Read → inherits ref Doer→Read with relation=extends.
	refs := inheritsRefsBySource(doc, "service.Doer")
	var found bool
	for _, r := range refs {
		if r.Target == "Read" {
			found = true
			if rel, _ := r.Metadata["relation"].(string); rel != "extends" {
				t.Errorf("Doer→Read: relation = %q, want extends", rel)
			}
		}
	}
	if !found {
		t.Errorf("missing inherits ref Doer → Read")
	}
}

func TestRustGrammar_PrivateItems(t *testing.T) {
	const src = `
pub fn public_fn() {}
fn private_fn() {}
pub struct PubStruct {}
struct PrivStruct {}
`
	h := code.New(code.NewRustGrammar())
	doc, err := h.Parse("src/vis.rs", []byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got := rustUnitTitles(doc)
	for _, want := range []string{"public_fn", "private_fn", "PubStruct", "PrivStruct"} {
		if !got[want] {
			t.Errorf("missing Unit for %q (pub+private items both expected)", want)
		}
	}
}

func TestRustGrammar_TestModuleScoping(t *testing.T) {
	const src = `
pub fn real_fn() {}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn test_real_fn() {}
}
`
	h := code.New(code.NewRustGrammar())
	doc, err := h.Parse("src/lib.rs", []byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got := rustUnitTitles(doc)
	if !got["real_fn"] {
		t.Error("missing Unit real_fn")
	}
	if !got["test_real_fn"] {
		t.Error("missing Unit test_real_fn")
	}
	for _, u := range doc.Units {
		if u.Title == "test_real_fn" && !strings.Contains(u.Path, "tests.") {
			t.Errorf("test_real_fn.Path = %q, expected scoped under tests", u.Path)
		}
	}
}

func TestRustGrammar_TraitImplMethodPath(t *testing.T) {
	h := code.New(code.NewRustGrammar())
	doc, err := h.Parse("src/service.rs", []byte(rustSample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// `impl Doer for Service` — do_it should be scoped under the type (Service),
	// not under the trait (Doer). findByPath verifies the exact path rather than
	// picking up the identically-named function_signature_item in the trait body.
	u := findByPath(doc, "service.Service.do_it")
	if u == nil {
		t.Errorf("missing Unit service.Service.do_it — trait impl methods must scope under the type name")
	}
	// The trait body's method signature should also be present, under the trait.
	if findByPath(doc, "service.Doer.do_it") == nil {
		t.Errorf("missing Unit service.Doer.do_it — trait body method signature should scope under Doer")
	}
}

func TestRustGrammar_NestedWildcardImports(t *testing.T) {
	const src = `use std::{io::*, fmt::*};`
	h := code.New(code.NewRustGrammar())
	doc, err := h.Parse("src/lib.rs", []byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got := importTargets(doc)
	for _, want := range []string{"std::io::*", "std::fmt::*"} {
		if !got[want] {
			t.Errorf("missing import %q in nested wildcard expansion (got: %v)", want, got)
		}
	}
}

func TestRustGrammar_GenericImplMethodPath(t *testing.T) {
	const src = `
pub struct Container<T> { data: Vec<T> }
impl<T> Container<T> {
    pub fn push(&mut self, item: T) {}
    pub fn len(&self) -> usize { 0 }
}
`
	h := code.New(code.NewRustGrammar())
	doc, err := h.Parse("src/lib.rs", []byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// Methods on a generic impl should scope under the bare type name.
	if u := findByPath(doc, "lib.Container.push"); u == nil {
		t.Errorf("missing Unit lib.Container.push (generic impl should use bare type name 'Container', not 'Container<T>')")
	}
	if u := findByPath(doc, "lib.Container.len"); u == nil {
		t.Errorf("missing Unit lib.Container.len")
	}
	// No Unit path should contain angle brackets.
	for _, u := range doc.Units {
		if strings.Contains(u.Path, "<") || strings.Contains(u.Path, ">") {
			t.Errorf("Unit %q Path %q contains angle brackets from generic parameters", u.Title, u.Path)
		}
	}
}

func TestRustGrammar_ReferenceTypeImpl(t *testing.T) {
	// impl for a reference/pointer type (e.g., `impl<T> Deref for Box<T>`) —
	// rustBareTypeName returns "" for reference_type / pointer_type nodes,
	// so methods land under the file stem rather than a type-scoped path.
	// This test verifies the behavior is consistent and causes no panics.
	const src = `
struct Box<T>(T);
impl<T> Box<T> {
    pub fn unbox(self) -> T { self.0 }
}
`
	h := code.New(code.NewRustGrammar())
	doc, err := h.Parse("src/lib.rs", []byte(src))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	// Box<T> is a generic_type — rustBareTypeName strips params, giving "Box".
	if u := findByPath(doc, "lib.Box.unbox"); u == nil {
		t.Errorf("missing Unit lib.Box.unbox — generic struct impl methods must scope under bare type name")
	}
	for _, u := range doc.Units {
		if strings.Contains(u.Path, "<") || strings.Contains(u.Path, ">") {
			t.Errorf("Unit %q Path %q contains angle brackets", u.Title, u.Path)
		}
	}
}

// rustUnitTitles builds a title→true set from a ParsedDoc for assertions.
func rustUnitTitles(doc *indexer.ParsedDoc) map[string]bool {
	out := map[string]bool{}
	for _, u := range doc.Units {
		out[u.Title] = true
	}
	return out
}
