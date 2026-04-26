package tree_sitter_dart_test

import (
	"context"
	"testing"

	tree_sitter_dart "librarian/internal/indexer/handlers/code/tree_sitter_dart"

	sitter "github.com/tree-sitter/go-tree-sitter"
)

func TestCanLoadGrammarAndParseDart3(t *testing.T) {
	lang := sitter.NewLanguage(tree_sitter_dart.Language())
	if lang == nil {
		t.Fatal("Dart language failed to load (ABI mismatch?)")
	}
	parser := sitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(lang); err != nil {
		t.Fatalf("SetLanguage: %v", err)
	}
	src := []byte(`import 'package:flutter/material.dart';

enum Color { red, green, blue }

class Dog extends Animal implements Comparable<Dog> with Barker {
  @override
  final String name;
  Dog(this.name);
  // Dart 3.10 dot shorthand — the whole reason lib-1v2 exists.
  Color favorite() => .red;
}

mixin Barker on Animal { void bark(); }

extension StringX on String { String slug() => toLowerCase(); }

extension type UserId(int id) {}

sealed class Shape {}
final class Circle extends Shape {}
`)
	tree := parser.ParseCtx(context.Background(), src, nil)
	if tree == nil {
		t.Fatal("parse returned nil tree")
	}
	defer tree.Close()
	root := tree.RootNode()
	if got, want := root.Kind(), "program"; got != want {
		t.Errorf("root kind = %q, want %q", got, want)
	}
	// Each top-level declaration should produce a named child.
	want := []string{
		"import_or_export", "enum_declaration", "class_definition",
		"mixin_declaration", "extension_declaration",
		"extension_type_declaration", "class_definition", "class_definition",
	}
	if got := int(root.NamedChildCount()); got != len(want) {
		t.Fatalf("named children = %d, want %d", got, len(want))
	}
	for i, w := range want {
		if got := root.NamedChild(uint(i)).Kind(); got != w {
			t.Errorf("child[%d] = %q, want %q", i, got, w)
		}
	}
}
