package code_test

import (
	"strings"
	"testing"

	"librarian/internal/indexer"
	"librarian/internal/indexer/handlers/code"
)

const rubySample = `
require 'rails'
require 'json'

module Animals
  # Feline base.
  class Cat
    include Comparable
    extend Enumerable

    attr_accessor :name, :age
    attr_reader :id

    # Build a Cat from name.
    def initialize(name)
      @name = name
    end

    def speak
      "meow"
    end

    def self.create(name)
      new(name)
    end
  end
end

# Top-level class with inheritance.
class Dog < Animal
  prepend Logable

  def bark
    "woof"
  end
end
`

func TestRubyGrammar_Invariants(t *testing.T) {
	h := code.New(code.NewRubyGrammar())
	// Use Parse (no require_relative) so the import-target invariant checks pass
	// without needing AbsPath.
	code.AssertGrammarInvariants(t, h, "app/models/cat.rb", []byte(rubySample))
}

func TestRubyGrammar_SymbolKinds(t *testing.T) {
	h := code.New(code.NewRubyGrammar())
	doc, err := h.Parse("app/models/cat.rb", []byte(rubySample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if doc.Title != "cat" {
		t.Errorf("Title = %q, want %q", doc.Title, "cat")
	}
	if doc.Format != "ruby" {
		t.Errorf("Format = %q, want %q", doc.Format, "ruby")
	}

	wantKinds := map[string]string{
		"Animals": "class", // module
		"Cat":     "class",
		"Dog":     "class",
		"initialize": "method",
		"speak":      "method",
		"create":     "method", // singleton_method
		"bark":       "method",
	}
	gotKinds := map[string]string{}
	for _, u := range doc.Units {
		gotKinds[u.Title] = u.Kind
	}
	for name, wantKind := range wantKinds {
		got, ok := gotKinds[name]
		if !ok {
			t.Errorf("missing Unit for %q (all: %v)", name, rubyUnitTitles(doc))
			continue
		}
		if got != wantKind {
			t.Errorf("Unit %q Kind = %q, want %q", name, got, wantKind)
		}
	}
}

func TestRubyGrammar_Paths(t *testing.T) {
	h := code.New(code.NewRubyGrammar())
	doc, err := h.Parse("app/models/cat.rb", []byte(rubySample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	wantPaths := map[string]string{
		"Animals":    "cat.Animals",
		"Cat":        "cat.Animals.Cat",
		"initialize": "cat.Animals.Cat.initialize",
		"speak":      "cat.Animals.Cat.speak",
		"create":     "cat.Animals.Cat.create",
		"Dog":        "cat.Dog",
		"bark":       "cat.Dog.bark",
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

func TestRubyGrammar_Imports(t *testing.T) {
	h := code.New(code.NewRubyGrammar())
	doc, err := h.Parse("app/models/cat.rb", []byte(rubySample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got := importTargets(doc)
	for _, want := range []string{"rails", "json"} {
		if !got[want] {
			t.Errorf("missing import %q (got: %v)", want, got)
		}
	}
}

func TestRubyGrammar_RequireRelativeResolution(t *testing.T) {
	const src = `
require_relative 'helper'
require_relative '../utils/tools'
`
	h := code.New(code.NewRubyGrammar())
	doc, err := h.ParseCtx("app/models/user.rb", []byte(src), indexer.ParseContext{
		AbsPath: "/proj/app/models/user.rb",
	})
	if err != nil {
		t.Fatalf("ParseCtx: %v", err)
	}
	got := importTargets(doc)
	// require_relative 'helper' → app/models/helper.rb (relative to file dir)
	if !got["app/models/helper.rb"] {
		t.Errorf("missing resolved require_relative 'helper' (got: %v)", got)
	}
	// require_relative '../utils/tools' → app/utils/tools.rb
	if !got["app/utils/tools.rb"] {
		t.Errorf("missing resolved require_relative '../utils/tools' (got: %v)", got)
	}
}

func TestRubyGrammar_Inheritance(t *testing.T) {
	h := code.New(code.NewRubyGrammar())
	doc, err := h.Parse("app/models/cat.rb", []byte(rubySample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	// Cat includes Comparable and extends Enumerable → mixes edges.
	catRefs := inheritsRefsBySource(doc, "cat.Animals.Cat")
	checkMixin := func(target, relation string) {
		t.Helper()
		for _, r := range catRefs {
			if r.Target == target {
				if rel, _ := r.Metadata["relation"].(string); rel != relation {
					t.Errorf("Cat → %s: relation = %q, want %q", target, rel, relation)
				}
				return
			}
		}
		t.Errorf("missing inherits ref Cat → %s (all: %v)", target, catRefs)
	}
	checkMixin("Comparable", "mixes")
	checkMixin("Enumerable", "mixes")

	// Dog < Animal → extends edge.
	dogRefs := inheritsRefsBySource(doc, "cat.Dog")
	var foundAnimal bool
	for _, r := range dogRefs {
		if r.Target == "Animal" {
			foundAnimal = true
			if rel, _ := r.Metadata["relation"].(string); rel != "extends" {
				t.Errorf("Dog → Animal: relation = %q, want extends", rel)
			}
		}
	}
	if !foundAnimal {
		t.Errorf("missing inherits ref Dog → Animal (all: %v)", dogRefs)
	}

	// Dog prepend Logable → mixes edge.
	var foundLogable bool
	for _, r := range dogRefs {
		if r.Target == "Logable" {
			foundLogable = true
			if rel, _ := r.Metadata["relation"].(string); rel != "mixes" {
				t.Errorf("Dog → Logable: relation = %q, want mixes", rel)
			}
		}
	}
	if !foundLogable {
		t.Errorf("missing inherits ref Dog → Logable (all: %v)", dogRefs)
	}
}

func TestRubyGrammar_AttrAccessorExpansion(t *testing.T) {
	const src = `
class User
  attr_accessor :first_name, :last_name, :email
  attr_reader :id
end
`
	h := code.New(code.NewRubyGrammar())
	doc, err := h.Parse("app/user.rb", []byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	// All three attr_accessor symbols + attr_reader should emit field Units.
	wantFields := []string{"first_name", "last_name", "email", "id"}
	got := rubyUnitTitles(doc)
	for _, f := range wantFields {
		if !got[f] {
			t.Errorf("missing field Unit %q (all: %v)", f, got)
		}
	}
	// All fields should be scoped under User.
	for _, u := range doc.Units {
		if u.Kind != "field" {
			continue
		}
		if !strings.HasPrefix(u.Path, "user.User.") {
			t.Errorf("field %q Path = %q, expected prefix user.User.", u.Title, u.Path)
		}
	}
}

func TestRubyGrammar_ModuleNesting(t *testing.T) {
	const src = `
module Outer
  module Inner
    class Thing
      def action; end
    end
  end
end
`
	h := code.New(code.NewRubyGrammar())
	doc, err := h.Parse("lib/thing.rb", []byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	wantPaths := map[string]string{
		"Outer":  "thing.Outer",
		"Inner":  "thing.Outer.Inner",
		"Thing":  "thing.Outer.Inner.Thing",
		"action": "thing.Outer.Inner.Thing.action",
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

func TestRubyGrammar_ClassReopen(t *testing.T) {
	const src = `
class Dog
  def bark; "woof"; end
end

class Dog
  def fetch; "got it"; end
end
`
	h := code.New(code.NewRubyGrammar())
	doc, err := h.Parse("app/dog.rb", []byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	// Both methods from the reopened class should appear.
	got := rubyUnitTitles(doc)
	if !got["bark"] {
		t.Error("missing Unit bark from first Dog definition")
	}
	if !got["fetch"] {
		t.Error("missing Unit fetch from reopened Dog definition")
	}
}

func TestRubyGrammar_ScopedClassName(t *testing.T) {
	const src = `
class Pets::Dog
  def bark; "woof"; end
end
`
	h := code.New(code.NewRubyGrammar())
	doc, err := h.Parse("app/dog.rb", []byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got := rubyUnitTitles(doc)
	if !got["Pets::Dog"] {
		t.Errorf("missing Unit for scoped class Pets::Dog (all: %v)", got)
	}
	if !got["bark"] {
		t.Errorf("missing method Unit bark inside scoped class (all: %v)", got)
	}
	// Method must be scoped under the class path.
	for _, u := range doc.Units {
		if u.Title == "bark" && !strings.HasPrefix(u.Path, "dog.Pets::Dog.") {
			t.Errorf("bark.Path = %q, want prefix dog.Pets::Dog.", u.Path)
		}
	}
}

func TestRubyGrammar_OperatorAndSetterMethods(t *testing.T) {
	const src = `
class Vector
  def ==(other)
    @x == other.x
  end

  def [](idx)
    @data[idx]
  end

  def name=(val)
    @name = val
  end
end
`
	h := code.New(code.NewRubyGrammar())
	doc, err := h.Parse("lib/vector.rb", []byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got := rubyUnitTitles(doc)
	for _, want := range []string{"==", "[]", "name="} {
		if !got[want] {
			t.Errorf("missing method Unit %q (operator/setter method not indexed; all: %v)", want, got)
		}
	}
}

// rubyUnitTitles builds a title→true set from a ParsedDoc.
func rubyUnitTitles(doc *indexer.ParsedDoc) map[string]bool {
	out := map[string]bool{}
	for _, u := range doc.Units {
		out[u.Title] = true
	}
	return out
}
