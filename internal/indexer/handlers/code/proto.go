package code

import (
	"strconv"
	"strings"

	tree_sitter_proto "github.com/coder3101/tree-sitter-proto/bindings/go"
	sitter "github.com/tree-sitter/go-tree-sitter"

	"librarian/internal/indexer"
)

// ProtoGrammar indexes .proto source files (Protocol Buffers).
//
// Backed by github.com/coder3101/tree-sitter-proto (ABI 15, pinned in go.mod).
// Covers proto2, proto3, and edition 2023/2024 syntaxes. Per the lib-wab spike,
// grammar shape has a few quirks that this handler has to work around:
//
//   - Names (rpc_name / message_name / service_name / enum_name) are NOT
//     field-tagged — ChildByFieldName("name") returns nil. Each lookup iterates
//     named children and matches on kind.
//   - `stream`, `public` (in `import public`), and `weak` are anonymous keyword
//     children — positional / kind-string detection required.
//   - Option LHS differs: simple option uses `identifier`, custom option uses
//     `full_ident`.
//
// Unit.Kinds emitted:
//
//   - "service"  — service declaration (container for rpc members).
//   - "rpc"      — rpc method inside a service. Metadata carries input_type,
//                  output_type, client_streaming, server_streaming.
//   - "message"  — message declaration (container for fields / oneofs /
//                  nested messages / enums / extend).
//   - "enum"     — enum declaration (no descent; enum values stay in Content).
//   - "oneof"    — oneof declaration inside a message; members descend as
//                  "field" Units carrying Metadata["oneof"]=<oneof name>.
//   - "field"    — message field / map_field / oneof_field. Metadata carries
//                  field_number (int). For [deprecated = true] field options,
//                  emits Signal{Kind:"label", Value:"deprecated"}.
//
// Reference.Kinds emitted:
//
//   - "import"   — one per top-level import directive. Metadata may carry
//                  "import_kind"=("public"|"weak") when the corresponding
//                  anonymous keyword is present.
//   - "inherits" — one per proto2 `extend Foo { ... }` block, Metadata.relation
//                  = "extends". File-scoped (no Source set) — extend is a
//                  file-level declaration without a surrounding symbol, so
//                  the graph edge originates at the file node.
//
// File-level `option go_package` / `option java_package` / etc. are stashed
// on ParsedDoc.Metadata["options"] via the parsedDocPostProcessor hook —
// feeds lib-4kb's buf.gen.yaml path-matching.
type ProtoGrammar struct{}

// NewProtoGrammar returns the Protobuf grammar implementation.
func NewProtoGrammar() *ProtoGrammar { return &ProtoGrammar{} }

func (*ProtoGrammar) Name() string               { return "proto" }
func (*ProtoGrammar) Extensions() []string       { return []string{".proto"} }
func (*ProtoGrammar) Language() *sitter.Language { return sitter.NewLanguage(tree_sitter_proto.Language()) }

// CommentNodeTypes returns an empty slice. Proto comment nodes are named
// siblings of declarations, but DocstringFromNode handles docstring attachment
// via the preceding-sibling walk below — returning ["comment"] here would
// cause the walker's pending buffer and DocstringFromNode to both fire,
// duplicating the docstring in Unit.Content.
func (*ProtoGrammar) CommentNodeTypes() []string { return []string{} }

// DocstringFromNode walks the preceding named-sibling chain while siblings
// have kind "comment" and are on adjacent source lines (no blank-line gap),
// collects their stripped text, and returns them joined with newlines. This is
// the canonical docstring mechanism for proto — comment nodes are named
// children of their parent (service, message_body, etc.) and appear as
// immediate preceding siblings of the declaration they document.
func (*ProtoGrammar) DocstringFromNode(n *sitter.Node, source []byte) string {
	var lines []string
	sib := n.PrevNamedSibling()
	for sib != nil && sib.Kind() == "comment" {
		lines = append([]string{stripCommentMarkers(sib.Utf8Text(source))}, lines...)
		prev := sib.PrevNamedSibling()
		if prev == nil || prev.Kind() != "comment" || !commentsAreConsecutive(prev, sib) {
			break
		}
		sib = prev
	}
	return strings.Join(lines, "\n")
}

// SymbolKinds maps proto AST node types to Unit.Kind. `extend` is
// deliberately absent — we emit a file-level `inherits` Reference instead
// (see Imports).
func (*ProtoGrammar) SymbolKinds() map[string]string {
	return map[string]string{
		"service":      "service",
		"rpc":          "rpc",
		"message":      "message",
		"enum":         "enum",
		"oneof":        "oneof",
		"field":        "field",
		"map_field":    "field",
		"oneof_field":  "field",
		"group":        "message", // proto2 groups project as inline messages
	}
}

// ContainerKinds lists wrappers the walker descends through. Hybrid nodes
// (symbol + container) appear in both SymbolKinds and here.
func (*ProtoGrammar) ContainerKinds() map[string]bool {
	return map[string]bool{
		// Hybrid — emit a Unit AND descend.
		"service": true,
		"message": true,
		"oneof":   true,
		"group":   true,
		// Pure containers — descend, no Unit.
		"message_body": true,
	}
}

// SymbolName returns the display name for a proto declaration node.
func (*ProtoGrammar) SymbolName(n *sitter.Node, source []byte) string {
	switch n.Kind() {
	case "service":
		return protoChildOfKindIdentifier(n, "service_name", source)
	case "rpc":
		return protoChildOfKindIdentifier(n, "rpc_name", source)
	case "message", "group":
		return protoChildOfKindIdentifier(n, "message_name", source)
	case "enum":
		return protoChildOfKindIdentifier(n, "enum_name", source)
	case "oneof":
		// oneof's name is a direct `identifier` named child (not wrapped).
		for i := uint(0); i < n.NamedChildCount(); i++ {
			c := n.NamedChild(i)
			if c != nil && c.Kind() == "identifier" {
				return strings.TrimSpace(c.Utf8Text(source))
			}
		}
	case "field", "map_field", "oneof_field":
		// Field name is the first DIRECT `identifier` named child. The
		// surrounding type shapes (`type`, `key_type`, `message_or_enum_type`)
		// all wrap any inner identifiers one level deeper, and `field_number`
		// is a distinct node kind — so NamedChild iteration can only hit an
		// identifier on the field name itself, regardless of whether the
		// value type is a scalar (no nested identifiers) or a custom message
		// (identifier is a grandchild, filtered out by NamedChild's
		// direct-children semantics).
		for i := uint(0); i < n.NamedChildCount(); i++ {
			c := n.NamedChild(i)
			if c != nil && c.Kind() == "identifier" {
				return strings.TrimSpace(c.Utf8Text(source))
			}
		}
	}
	return ""
}

// protoChildOfKindIdentifier returns the text of the first `identifier`
// descendant inside a named child of the given kind, e.g. rpc_name's
// identifier child. Used for rpc_name / service_name / message_name /
// enum_name — all of which wrap a single identifier.
func protoChildOfKindIdentifier(n *sitter.Node, kind string, source []byte) string {
	for i := uint(0); i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		if c == nil || c.Kind() != kind {
			continue
		}
		for j := uint(0); j < c.NamedChildCount(); j++ {
			id := c.NamedChild(j)
			if id != nil && id.Kind() == "identifier" {
				return strings.TrimSpace(id.Utf8Text(source))
			}
		}
	}
	return ""
}

// PackageName extracts the dotted name from `package foo.bar;`.
func (*ProtoGrammar) PackageName(root *sitter.Node, source []byte) string {
	for i := uint(0); i < root.NamedChildCount(); i++ {
		c := root.NamedChild(i)
		if c == nil || c.Kind() != "package" {
			continue
		}
		fi := findDescendant(c, "full_ident")
		if fi != nil {
			return protoFullIdentText(fi, source)
		}
	}
	return ""
}

// protoFullIdentText reconstructs a dotted identifier from a `full_ident` node
// by concatenating its identifier children with '.'.
func protoFullIdentText(n *sitter.Node, source []byte) string {
	var parts []string
	for i := uint(0); i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		if c != nil && c.Kind() == "identifier" {
			parts = append(parts, strings.TrimSpace(c.Utf8Text(source)))
		}
	}
	if len(parts) == 0 {
		return strings.TrimSpace(n.Utf8Text(source))
	}
	return strings.Join(parts, ".")
}

// Imports walks the entire AST for import directives and `extend Foo { ... }`
// blocks, returning the union as ImportRefs.
//
// Imports:
//
//	import "x.proto"         → ImportRef{Path: "x.proto"}
//	import public "y.proto"  → ImportRef{Path, Metadata{"import_kind":"public"}}
//	import weak "z.proto"    → ImportRef{Path, Metadata{"import_kind":"weak"}}
//
// Extends:
//
//	extend Foo { ... }              → ImportRef{Kind:"inherits", Path:"Foo",
//	                                              Metadata{"relation":"extends"}}
//	message M { extend Bar { ... } } → emitted the same way. proto2 allows
//	                                    extends nested inside messages; a
//	                                    top-level-only walk would drop them
//	                                    on the floor.
//
// Reusing the Imports hook for `extend` follows the Dart grammar precedent
// where `part 'foo.dart'` rides the same channel with a distinct Kind.
func (*ProtoGrammar) Imports(root *sitter.Node, source []byte) []ImportRef {
	var out []ImportRef
	walk(root, func(n *sitter.Node) bool {
		switch n.Kind() {
		case "import":
			if ref := protoParseImport(n, source); ref != nil {
				out = append(out, *ref)
			}
			return false
		case "extend":
			if ref := protoParseExtend(n, source); ref != nil {
				out = append(out, *ref)
			}
			// Don't descend — `extend`'s body holds field decls that
			// belong to the target message, not additional imports /
			// extends to emit at file scope.
			return false
		}
		return true
	})
	return out
}

// protoParseImport reads an `import` node's `string` child (strip quotes) and
// the anonymous `public`/`weak` marker if present.
func protoParseImport(n *sitter.Node, source []byte) *ImportRef {
	path := ""
	for i := uint(0); i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		if c != nil && c.Kind() == "string" {
			path = protoStripQuotes(c.Utf8Text(source))
			break
		}
	}
	if path == "" {
		return nil
	}
	ref := &ImportRef{Path: path}
	// `public` / `weak` are anonymous keyword children of `import`.
	for i := uint(0); i < n.ChildCount(); i++ {
		c := n.Child(i)
		if c == nil {
			continue
		}
		switch c.Kind() {
		case "public":
			if ref.Metadata == nil {
				ref.Metadata = map[string]any{}
			}
			ref.Metadata["import_kind"] = "public"
		case "weak":
			if ref.Metadata == nil {
				ref.Metadata = map[string]any{}
			}
			ref.Metadata["import_kind"] = "weak"
		}
	}
	return ref
}

// protoParseExtend extracts the target message name from an `extend` node
// (`full_ident` child) and returns an inherits ImportRef.
func protoParseExtend(n *sitter.Node, source []byte) *ImportRef {
	var target string
	for i := uint(0); i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		if c == nil {
			continue
		}
		switch c.Kind() {
		case "full_ident":
			target = protoFullIdentText(c, source)
		case "message_or_enum_type":
			target = strings.TrimSpace(c.Utf8Text(source))
		}
		if target != "" {
			break
		}
	}
	if target == "" {
		return nil
	}
	return &ImportRef{
		Kind:     "inherits",
		Path:     target,
		Metadata: map[string]any{"relation": "extends"},
	}
}

// protoStripQuotes removes a single surrounding pair of double or single
// quotes from a parsed `string` node's raw text. Unlike strings.Trim, it
// doesn't strip multiple consecutive quote characters — a value like
// `"""x"""` collapses to `""x""`, not `x`, preserving the original literal
// shape if it had internal quotes.
func protoStripQuotes(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 {
		first, last := s[0], s[len(s)-1]
		if (first == '"' || first == '\'') && first == last {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// SymbolMetadata implements symbolMetadataExtractor. Each symbol kind can
// contribute structured metadata the shared walker merges into Unit.Metadata.
//
//   - "rpc"     → input_type, output_type, client_streaming (bool),
//                 server_streaming (bool). Types are extracted from the
//                 `message_or_enum_type` children flanking `returns`; streaming
//                 is detected via the two anonymous `stream` keyword children.
//   - "field"   → field_number (int), plus Metadata["oneof"]=<oneof name>
//                 when the field lives inside a oneof container.
//   - "group"   → field_number (int) — proto2 groups inline both a message
//                 and a field declaration; the walker emits the group as a
//                 message Unit but the field_number still belongs on it.
func (*ProtoGrammar) SymbolMetadata(n *sitter.Node, source []byte) map[string]any {
	switch n.Kind() {
	case "rpc":
		return protoRPCMetadata(n, source)
	case "field", "map_field", "oneof_field":
		return protoFieldMetadata(n, source)
	case "group":
		// group inlines a field+message; only the field_number is
		// available directly — the fields inside will carry their own.
		if num, ok := protoFieldNumber(n, source); ok {
			return map[string]any{"field_number": num}
		}
	}
	return nil
}

// protoRPCMetadata parses an `rpc` node. Shape:
//
//	rpc NAME "(" [stream] INPUT ")" returns "(" [stream] OUTPUT ")" ";"
//
// Both INPUT and OUTPUT are `message_or_enum_type` named children; the
// `stream` keyword before each is anonymous. We detect streaming by byte
// position of the `stream` token relative to the `returns` keyword — any
// `stream` before `returns` marks client-streaming; any after marks
// server-streaming.
func protoRPCMetadata(n *sitter.Node, source []byte) map[string]any {
	var input, output string
	clientStream, serverStream := false, false
	returnsOffset := -1

	// First pass: locate `returns` keyword and walk all children for stream
	// flags. Anonymous children aren't visible via NamedChild, so use ChildCount.
	for i := uint(0); i < n.ChildCount(); i++ {
		c := n.Child(i)
		if c != nil && c.Kind() == "returns" {
			returnsOffset = int(c.StartByte())
			break
		}
	}
	for i := uint(0); i < n.ChildCount(); i++ {
		c := n.Child(i)
		if c == nil || c.Kind() != "stream" {
			continue
		}
		// A malformed rpc node without a `returns` keyword shouldn't have
		// any stream tokens tagged — position-based classification requires
		// a pivot. Skip rather than default-tag as client-streaming.
		if returnsOffset < 0 {
			continue
		}
		if int(c.StartByte()) > returnsOffset {
			serverStream = true
		} else {
			clientStream = true
		}
	}
	// Second pass: two message_or_enum_type children, in source order:
	// input first, output second.
	for i := uint(0); i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		if c == nil || c.Kind() != "message_or_enum_type" {
			continue
		}
		text := strings.TrimSpace(c.Utf8Text(source))
		if input == "" {
			input = text
		} else if output == "" {
			output = text
		}
	}

	out := map[string]any{}
	if input != "" {
		out["input_type"] = input
	}
	if output != "" {
		out["output_type"] = output
	}
	out["client_streaming"] = clientStream
	out["server_streaming"] = serverStream
	return out
}

// protoFieldMetadata pulls structured metadata off a field / map_field /
// oneof_field node.
//
// Keys always present when available:
//   - "field_number" (int)         — the field tag number.
//   - "oneof"        (string)      — enclosing oneof name when the field is
//                                    declared inside a oneof block.
//
// Type-shape keys (absent rather than empty when not applicable):
//   - "type"         (string)      — scalar or message type for field /
//                                    oneof_field ("string", "int32",
//                                    "MyMessage", …).
//   - "repeated"     (bool)        — true only for repeated fields; omitted
//                                    otherwise.
//   - "map_key_type" (string)      — key type for map_field only.
//   - "map_value_type" (string)    — value type for map_field only.
func protoFieldMetadata(n *sitter.Node, source []byte) map[string]any {
	out := map[string]any{}
	if num, ok := protoFieldNumber(n, source); ok {
		out["field_number"] = num
	}
	// Ancestor walk to see if we're inside a oneof — if so, record its name.
	if oneof := findAncestor(n, "oneof"); oneof != nil {
		for i := uint(0); i < oneof.NamedChildCount(); i++ {
			c := oneof.NamedChild(i)
			if c != nil && c.Kind() == "identifier" {
				out["oneof"] = strings.TrimSpace(c.Utf8Text(source))
				break
			}
		}
	}

	switch n.Kind() {
	case "field", "oneof_field":
		// Extract the scalar or message type from the `type` named child.
		for i := uint(0); i < n.NamedChildCount(); i++ {
			c := n.NamedChild(i)
			if c != nil && c.Kind() == "type" {
				if t := strings.TrimSpace(c.Utf8Text(source)); t != "" {
					out["type"] = t
				}
				break
			}
		}
		// `repeated` is an anonymous keyword child — not in NamedChildCount.
		for i := uint(0); i < n.ChildCount(); i++ {
			c := n.Child(i)
			if c != nil && c.Kind() == "repeated" {
				out["repeated"] = true
				break
			}
		}
	case "map_field":
		// map_field has key_type and type (value type) as named children.
		for i := uint(0); i < n.NamedChildCount(); i++ {
			c := n.NamedChild(i)
			if c == nil {
				continue
			}
			switch c.Kind() {
			case "key_type":
				if t := strings.TrimSpace(c.Utf8Text(source)); t != "" {
					out["map_key_type"] = t
				}
			case "type":
				if t := strings.TrimSpace(c.Utf8Text(source)); t != "" {
					out["map_value_type"] = t
				}
			}
		}
	}

	if len(out) == 0 {
		return nil
	}
	return out
}

// protoFieldNumber extracts the integer value of a field_number node. Returns
// (0, false) if the node is missing or the value can't be parsed.
func protoFieldNumber(n *sitter.Node, source []byte) (int, bool) {
	for i := uint(0); i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		if c == nil || c.Kind() != "field_number" {
			continue
		}
		text := strings.TrimSpace(c.Utf8Text(source))
		if v, err := strconv.Atoi(text); err == nil {
			return v, true
		}
		// Hex (0x...) / octal (0...) fallback — decimal covers the 99% case;
		// rare tag numbers may still be expressed in other bases.
		if v, err := strconv.ParseInt(text, 0, 64); err == nil {
			return int(v), true
		}
	}
	return 0, false
}

// SymbolExtraSignals implements extraSignalsExtractor. Surfaces
// `[deprecated = true]` field options as Signal{Kind:"label", Value:"deprecated"}.
func (*ProtoGrammar) SymbolExtraSignals(n *sitter.Node, source []byte) []indexer.Signal {
	if n.Kind() != "field" && n.Kind() != "map_field" && n.Kind() != "oneof_field" {
		return nil
	}
	// field_options > field_option children; each field_option has an
	// identifier LHS and a constant RHS. We flag deprecated=true specifically.
	fo := findDescendant(n, "field_options")
	if fo == nil {
		return nil
	}
	var out []indexer.Signal
	for i := uint(0); i < fo.NamedChildCount(); i++ {
		c := fo.NamedChild(i)
		if c == nil || c.Kind() != "field_option" {
			continue
		}
		name, value := "", ""
		for j := uint(0); j < c.NamedChildCount(); j++ {
			cc := c.NamedChild(j)
			if cc == nil {
				continue
			}
			switch cc.Kind() {
			case "identifier", "full_ident":
				if name == "" {
					name = strings.TrimSpace(cc.Utf8Text(source))
				}
			case "constant":
				value = strings.TrimSpace(cc.Utf8Text(source))
			}
		}
		if name == "deprecated" && value == "true" {
			out = append(out, indexer.Signal{Kind: "label", Value: "deprecated"})
		}
	}
	return out
}

// protoFileOptions walks the source root and collects top-level `option`
// declarations whose LHS is a simple identifier (e.g. `option go_package`,
// `option java_package`). Returns a name→value map; quoted string values
// have their surrounding quotes stripped. Custom options (`option
// (foo.bar)`) land as `full_ident` LHS and are skipped — they feed runtime
// behaviour, not package routing.
//
// Called by PostProcess; the resulting map lands on
// ParsedDoc.Metadata["options"] and is intended to feed lib-4kb's
// buf.gen.yaml path-matching once that bead lands.
func protoFileOptions(root *sitter.Node, source []byte) map[string]string {
	out := map[string]string{}
	for i := uint(0); i < root.NamedChildCount(); i++ {
		c := root.NamedChild(i)
		if c == nil || c.Kind() != "option" {
			continue
		}
		var name, value string
		for j := uint(0); j < c.NamedChildCount(); j++ {
			cc := c.NamedChild(j)
			if cc == nil {
				continue
			}
			switch cc.Kind() {
			case "identifier":
				if name == "" {
					name = strings.TrimSpace(cc.Utf8Text(source))
				}
			case "constant":
				value = protoStripQuotes(cc.Utf8Text(source))
			}
		}
		if name != "" {
			out[name] = value
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Compile-time assertion that ProtoGrammar implements the optional
// parsedDocPostProcessor hook — the shared CodeHandler invokes PostProcess
// after imports and inheritance have been emitted so the grammar can stash
// file-level state on ParsedDoc.Metadata.
var _ parsedDocPostProcessor = (*ProtoGrammar)(nil)

// PostProcess sets ParsedDoc.Metadata["options"] to the file-level option map
// collected by protoFileOptions.
func (*ProtoGrammar) PostProcess(doc *indexer.ParsedDoc, root *sitter.Node, source []byte) {
	if opts := protoFileOptions(root, source); opts != nil {
		if doc.Metadata == nil {
			doc.Metadata = map[string]any{}
		}
		doc.Metadata["options"] = opts
	}
}
