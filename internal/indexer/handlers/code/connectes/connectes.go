// Package connectes indexes protoc-gen-connect-es generated TypeScript/JavaScript
// service-stub files (*_connect.ts, *_connectweb.ts, *_connect.js).
//
// protoc-gen-connect-es (@connectrpc/protoc-gen-connect-es) generates service
// descriptors as plain object-literal exports rather than classes:
//
//	export const FooService = {
//	  typeName: "auth.v1.FooService",
//	  methods: {
//	    login: { name: "Login", kind: MethodKind.Unary, I: LoginRequest, O: LoginReply },
//	    logout: { name: "Logout", kind: MethodKind.Unary, I: LogoutRequest, O: LogoutReply },
//	  },
//	} as const;
//
// The generic TypeScript grammar handler does not extract object-literal keys as
// symbols, so without this handler every Connect-Web RPC stub would be invisible
// to the implements_rpc resolver. This handler fills that gap by emitting one
// symbol-kind graph node per (service, method) pair with a dotted path that
// matches the proto rpc's Unit.Path shape, enabling the existing TS candidate
// in linkRPCImplementations to link them.
//
// Registration: uses indexer.RegisterDefaultAdditional so this handler runs IN
// ADDITION TO the primary TypeScript grammar handler for .ts and .js files.
// Both handlers contribute symbols from the same file; the TypeScript grammar
// handles general code symbols and this handler contributes the RPC stubs.
package connectes

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"

	"librarian/internal/indexer"
	tree_sitter_typescript "librarian/internal/indexer/handlers/code/tree_sitter_typescript/typescript"
)

var tsLang = sitter.NewLanguage(tree_sitter_typescript.Language())

// Handler detects and parses connect-es generated service stub files.
type Handler struct{}

// New returns a Handler ready for registration.
func New() *Handler { return &Handler{} }

var _ indexer.FileHandler = (*Handler)(nil)

// Name implements indexer.FileHandler.
func (*Handler) Name() string { return "connect-es" }

// Extensions implements indexer.FileHandler.
// ".ts" and ".js" cover all protoc-gen-connect-es output variants.
func (*Handler) Extensions() []string { return []string{".ts", ".js"} }

// Parse implements indexer.FileHandler. For non-connect-es files (no connect
// suffix or no typeName property), returns an empty ParsedDoc. For connect-es
// files, emits one "method"-kind Unit per (service, method) pair with:
//
//   - Unit.Path  = "<typeName>.<methodKey>" (exact match with proto rpc Unit.Path)
//   - Unit.Kind  = "method" (projected as symbol-kind graph node by indexGraphFile)
//   - Unit.Title = methodKey
//   - Unit.Metadata = {"connect_es_stub": true, "service_typename": "...",
//     "method_key": "...", "streaming_kind": "..."}
func (h *Handler) Parse(path string, content []byte) (*indexer.ParsedDoc, error) {
	doc := &indexer.ParsedDoc{
		Path:       path,
		Format:     h.Name(),
		DocType:    "code",
		RawContent: string(content),
	}
	if !hasConnectSuffix(path) {
		return doc, nil
	}
	if !bytes.Contains(content, []byte("typeName")) {
		return doc, nil
	}
	// Parse the AST once; reuse for both detection (non-empty defs) and extraction.
	defs := parseServiceDefs(content)
	if len(defs) == 0 {
		return doc, nil
	}
	doc.Units = unitsFromDefs(defs)
	return doc, nil
}

// Chunk implements indexer.FileHandler. Returns no chunks — connect-es symbols
// are graph-only and do not participate in the docs-pass embedding store.
func (*Handler) Chunk(_ *indexer.ParsedDoc, _ indexer.ChunkOpts) ([]indexer.Chunk, error) {
	return nil, nil
}

// hasConnectSuffix reports whether the basename's stem (extension stripped) ends
// with "_connect" or "_connectweb", case-insensitive.
func hasConnectSuffix(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	return strings.HasSuffix(stem, "_connect") || strings.HasSuffix(stem, "_connectweb")
}

// serviceDef is the in-memory representation of one parsed service export.
type serviceDef struct {
	typeName string
	methods  []methodDef
}

// methodDef represents one entry under `methods: { ... }`.
type methodDef struct {
	key  string // lowerCamelCase key as written in the generated file
	kind string // streaming variant extracted from MethodKind.<variant> (may be "")
}

// parseServiceDefs uses tree-sitter to walk the AST and returns every connect-es
// service definition found in content. Returns nil on parse failure.
func parseServiceDefs(content []byte) []serviceDef {
	parser := sitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(tsLang); err != nil {
		return nil
	}
	tree := parser.ParseCtx(context.Background(), content, nil)
	if tree == nil {
		return nil
	}
	defer tree.Close()
	return collectServiceDefs(tree.RootNode(), content)
}

// collectServiceDefs walks the top-level AST children looking for export_statement
// nodes that contain a connect-es service definition.
func collectServiceDefs(root *sitter.Node, src []byte) []serviceDef {
	var defs []serviceDef
	for i := uint(0); i < root.NamedChildCount(); i++ {
		child := root.NamedChild(i)
		if child == nil || child.Kind() != "export_statement" {
			continue
		}
		def, ok := parseExportStatement(child, src)
		if ok {
			defs = append(defs, def)
		}
	}
	return defs
}

// parseExportStatement checks whether an export_statement wraps a lexical_declaration
// containing a connect-es service object.
func parseExportStatement(node *sitter.Node, src []byte) (serviceDef, bool) {
	for i := uint(0); i < node.NamedChildCount(); i++ {
		child := node.NamedChild(i)
		if child == nil || child.Kind() != "lexical_declaration" {
			continue
		}
		return parseLexicalDeclaration(child, src)
	}
	return serviceDef{}, false
}

// parseLexicalDeclaration extracts a service definition from
// `const X = { typeName: "...", methods: { ... } }` (possibly `as const`).
func parseLexicalDeclaration(node *sitter.Node, src []byte) (serviceDef, bool) {
	for i := uint(0); i < node.NamedChildCount(); i++ {
		child := node.NamedChild(i)
		if child == nil || child.Kind() != "variable_declarator" {
			continue
		}
		valueNode := child.ChildByFieldName("value")
		if valueNode == nil {
			continue
		}
		// Unwrap `as const` / `as SomeType` (TypeScript as_expression).
		if valueNode.Kind() == "as_expression" {
			if inner := valueNode.NamedChild(0); inner != nil {
				valueNode = inner
			}
		}
		if valueNode.Kind() != "object" {
			continue
		}
		def, ok := parseServiceObject(valueNode, src)
		if ok {
			return def, true
		}
	}
	return serviceDef{}, false
}

// parseServiceObject extracts typeName and methods from an object literal.
// Returns (def, true) only when both typeName (non-empty string) and at
// least one method are present.
func parseServiceObject(node *sitter.Node, src []byte) (serviceDef, bool) {
	var typeName string
	var methods []methodDef
	for i := uint(0); i < node.NamedChildCount(); i++ {
		pair := node.NamedChild(i)
		if pair == nil || pair.Kind() != "pair" {
			continue
		}
		keyNode := pair.ChildByFieldName("key")
		valNode := pair.ChildByFieldName("value")
		if keyNode == nil || valNode == nil {
			continue
		}
		// keyNode is typically property_identifier (unquoted) for generated files.
		// Quoted string keys (e.g. "typeName": "...") are not emitted by
		// protoc-gen-connect-es but are handled defensively below.
		key := stripTSStringQuotes(keyNode.Utf8Text(src))
		switch key {
		case "typeName":
			if valNode.Kind() == "string" {
				typeName = stripTSStringQuotes(valNode.Utf8Text(src))
			}
		case "methods":
			if valNode.Kind() == "object" {
				methods = parseMethods(valNode, src)
			}
		}
	}
	if typeName == "" || len(methods) == 0 {
		return serviceDef{}, false
	}
	return serviceDef{typeName: typeName, methods: methods}, true
}

// parseMethods extracts method entries from the `methods: { ... }` object.
func parseMethods(node *sitter.Node, src []byte) []methodDef {
	var out []methodDef
	for i := uint(0); i < node.NamedChildCount(); i++ {
		pair := node.NamedChild(i)
		if pair == nil || pair.Kind() != "pair" {
			continue
		}
		keyNode := pair.ChildByFieldName("key")
		if keyNode == nil {
			continue
		}
		methodKey := stripTSStringQuotes(keyNode.Utf8Text(src))
		if methodKey == "" {
			continue
		}
		var streamKind string
		valNode := pair.ChildByFieldName("value")
		if valNode != nil && valNode.Kind() == "object" {
			streamKind = extractStreamingKind(valNode, src)
		}
		out = append(out, methodDef{key: methodKey, kind: streamKind})
	}
	return out
}

// extractStreamingKind looks for `kind: MethodKind.<Variant>` inside a method
// object and returns the variant string (e.g. "Unary", "ServerStreaming").
// Returns "" when absent or unparseable.
func extractStreamingKind(methodObj *sitter.Node, src []byte) string {
	for i := uint(0); i < methodObj.NamedChildCount(); i++ {
		pair := methodObj.NamedChild(i)
		if pair == nil || pair.Kind() != "pair" {
			continue
		}
		keyNode := pair.ChildByFieldName("key")
		if keyNode == nil || keyNode.Utf8Text(src) != "kind" {
			continue
		}
		valNode := pair.ChildByFieldName("value")
		if valNode == nil {
			continue
		}
		// MethodKind.Unary is a member_expression: object=MethodKind, property=Unary.
		if valNode.Kind() == "member_expression" {
			if propNode := valNode.ChildByFieldName("property"); propNode != nil {
				return propNode.Utf8Text(src)
			}
		}
	}
	return ""
}

// unitsFromDefs converts pre-parsed service definitions into indexer Units.
// One "method"-kind Unit is emitted per (service, method) pair.
func unitsFromDefs(defs []serviceDef) []indexer.Unit {
	var units []indexer.Unit
	for _, def := range defs {
		for _, m := range def.methods {
			unitPath := def.typeName + "." + m.key
			units = append(units, indexer.Unit{
				Kind:  "method",
				Title: m.key,
				Path:  unitPath,
				// Content is a concise text representation; the important data is in Metadata.
				Content: connectESUnitContent(def.typeName, m.key, m.kind),
				Metadata: map[string]any{
					"connect_es_stub":  true,
					"service_typename": def.typeName,
					"method_key":       m.key,
					"streaming_kind":   m.kind,
				},
			})
		}
	}
	return units
}

// stripTSStringQuotes removes a single surrounding pair of double or single
// quotes (or backtick) from a tree-sitter string node's raw text, mirroring
// the protoStripQuotes pattern in proto.go. Unlike strings.Trim it strips at
// most one pair so internal quotes are preserved.
func stripTSStringQuotes(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 {
		first, last := s[0], s[len(s)-1]
		if (first == '"' || first == '\'' || first == '`') && first == last {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// connectESUnitContent returns a concise text representation suitable for
// embedding. Kept simple — the important data is in Unit.Metadata.
func connectESUnitContent(typeName, methodKey, streamingKind string) string {
	if streamingKind != "" {
		return typeName + "." + methodKey + " (" + streamingKind + ")"
	}
	return typeName + "." + methodKey
}

func init() {
	indexer.RegisterDefaultAdditional(New())
}
