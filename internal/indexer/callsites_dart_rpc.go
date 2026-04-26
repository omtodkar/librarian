package indexer

// callsites_dart_rpc.go — Dart grpc-dart call-site detector (lib-4g2.4).
//
// Detects Flutter/Dart call sites that invoke methods on grpc-dart generated
// client classes and emits call_rpc edges (symbol → proto rpc declaration).
// Runs alongside buildCallRPCEdges (the TS/JS detector from lib-4g2.3);
// the call_rpc EdgeKind and MCP surface are shared.
//
// # Supported patterns (single-file, v1)
//
//  1. Direct local binding:
//     final client = FooServiceClient(channel);
//     await client.login(req);
//
//  2. Class field assignment (constructor body or method):
//     Assignment binding: `this._authClient = AuthServiceClient(channel)` or
//     `_authClient = AuthServiceClient(channel)` (both forms are detected).
//     Call site: bare `_authClient.logout(req)` OR explicit `this._authClient.logout(req)`.
//     The `this.field.method(args)` call form uses a 4-sibling pattern in the
//     Dart AST (this + selector(.field) + selector(.method) + selector(args)).
//
//  3. Expression-body getter returning a new client instance:
//     FooServiceClient get _client => FooServiceClient(_channel);
//     _client.login(req);  // in a sibling method of the same class
//
//  4. Streaming RPCs — same patterns; ResponseStream return type is irrelevant,
//     call_rpc is emitted regardless of streaming cardinality.
//
// # Oracle: implements_rpc
//
// The binding tracker uses the implements_rpc edge graph as the "is this a
// grpc-dart client class?" oracle. It scans every proto rpc node, follows
// incoming implements_rpc edges to find Dart-generated method symbols (source
// path ends in .dart), and extracts (unqualifiedClassName → methodName →
// protoRPCSymbolID). A constructor call `FooServiceClient(channel)` is
// recorded as a binding ONLY when `FooServiceClient` appears as a class name
// in this registry — so hand-written classes that happen to share the name
// aren't matched.
//
// # v1 limitations (document here for follow-up bead filing)
//
//   - Clients acquired via dependency injection (get_it, provider, Riverpod):
//     the constructor call happens in DI setup, the invocation in a widget;
//     the binding is DI-container-mediated and cannot be followed.
//   - Clients returned from async futures or builders without a direct
//     constructor call in the same file.
//   - Flutter widget trees where the client is passed down through widget
//     constructors across files.
//   - Generated Riverpod providers wrapping a client
//     (@riverpod FooServiceClient authClient(AuthClientRef ref) => ...).
//   - Cross-file binding (client constructed in one file, called from another).
//   - Generic constructor calls (FooServiceClient<T>(channel)) are parsed by
//     tree-sitter-dart as relational_expression and are not detected.
//
// # TODO: connect-dart forward-compat
//
// connectrpc/connect-dart is nascent (uses ServiceClient<T> generic + transport
// factories rather than direct FooServiceClient construction). Its generated
// shape differs from grpc-dart and is explicitly out of scope for v1. File a
// follow-up bead when/if the package matures and lands in the user's deps.
//
// # Enclosing-function skip rule
//
// When no named function/method/getter/constructor ancestor is found for a
// call site (e.g. an anonymous closure at the top level with no named parent),
// the edge is skipped rather than emitted with a dangling source node id.

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"

	tree_sitter_dart "librarian/internal/indexer/handlers/code/tree_sitter_dart"
	"librarian/internal/store"
)

// dartCallSiteLang is the tree-sitter Dart language used by the Dart call-site
// detector. Initialised once at package load; mirrors the lang var in the
// dart handler but kept separate to avoid an import cycle.
var dartCallSiteLang = sitter.NewLanguage(tree_sitter_dart.Language())

// dartClientRegistry maps (unqualifiedClassName → (lowerCamelCase methodName → protoRPCSymbolID)).
//
// Built from the implements_rpc edge graph: for each Dart-generated method
// symbol with an outgoing implements_rpc edge, the registry records
// (className, methodName) → target proto rpc node ID.
//
// Using unqualified class names is a v1 precision trade-off: two packages
// that each declare a FooServiceClient would collide. In practice, grpc-dart
// generated names include the proto service name and are unlikely to collide
// across packages in a single project.
type dartClientRegistry map[string]map[string]string

// dartCallSiteEdge is a (callerSymbolPath, className, methodName) triple
// detected in a single Dart file.
type dartCallSiteEdge struct {
	callerPath string // e.g. "my_service.AuthWidget.handleLogin"
	className  string // e.g. "AuthServiceClient"
	methodName string // e.g. "login" (lowerCamelCase, as emitted by protoc-gen-dart)
}

// buildDartCallRPCEdges is the post-graph-pass resolver that emits call_rpc
// edges from Dart call sites to their proto rpc declarations.
//
// Runs after buildCallRPCEdges (TS/JS) and buildImplementsRPCEdges so all
// Dart client symbol nodes and their implements_rpc edges exist when this
// resolver probes them.
func (idx *Indexer) buildDartCallRPCEdges(result *GraphResult) {
	registry, err := idx.buildDartClientRegistry()
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("callsites_dart_rpc: build registry: %s", err))
		return
	}
	if len(registry) == 0 {
		return // no grpc-dart client classes in project → nothing to link
	}

	codeFileNodes, err := idx.store.ListNodesByKind(store.NodeKindCodeFile)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("callsites_dart_rpc: list code_file nodes: %s", err))
		return
	}

	root := idx.cfg.ProjectRoot

	for _, node := range codeFileNodes {
		path := node.SourcePath
		if path == "" {
			continue
		}
		if strings.ToLower(filepath.Ext(path)) != ".dart" {
			continue
		}
		if isDartGeneratedPath(path) {
			continue
		}

		content, err := readCallSiteFile(root, path)
		if err != nil {
			// Missing or unreadable: file deleted after the per-file pass.
			continue
		}

		edges := parseDartCallSites(path, content, registry)
		for _, e := range edges {
			protoRPCID, ok := registry[e.className][e.methodName]
			if !ok {
				continue
			}
			callerID := store.SymbolNodeID(e.callerPath)

			callerNode, err := idx.store.GetNode(callerID)
			if err != nil || callerNode == nil {
				continue
			}

			if err := idx.store.UpsertEdge(store.Edge{
				From: callerID,
				To:   protoRPCID,
				Kind: store.EdgeKindCallRPC,
			}); err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("callsites_dart_rpc: upsert edge %s→%s: %s", callerID, protoRPCID, err))
				continue
			}
			result.EdgesAdded++
		}
	}
}

// buildDartClientRegistry queries the store for all proto rpc nodes, follows
// their incoming implements_rpc edges, and returns a registry of Dart-generated
// client classes. Only symbol nodes with a .dart source path contribute.
func (idx *Indexer) buildDartClientRegistry() (dartClientRegistry, error) {
	rpcNodes, err := idx.store.ListSymbolNodesWithMetadataContaining(protoRPCMetadataMarker)
	if err != nil {
		return nil, fmt.Errorf("list rpc nodes: %w", err)
	}

	reg := make(dartClientRegistry)
	for _, rpcNode := range rpcNodes {
		impls, err := idx.store.Neighbors(rpcNode.ID, "in", store.EdgeKindImplementsRPC)
		if err != nil {
			continue
		}
		for _, impl := range impls {
			implNode, err := idx.store.GetNode(impl.From)
			if err != nil || implNode == nil {
				continue
			}
			if strings.ToLower(filepath.Ext(implNode.SourcePath)) != ".dart" {
				continue
			}
			// impl.From is e.g. "sym:auth.v1.AuthServiceClient.login"
			symPath := strings.TrimPrefix(impl.From, "sym:")
			parts := strings.Split(symPath, ".")
			if len(parts) < 2 {
				continue
			}
			methodName := parts[len(parts)-1]
			className := parts[len(parts)-2]
			if reg[className] == nil {
				reg[className] = make(map[string]string)
			}
			reg[className][methodName] = rpcNode.ID
		}
	}
	return reg, nil
}

// ── tree-sitter Dart call-site parser ────────────────────────────────────────

// parseDartCallSites finds grpc-dart client call sites in a .dart source file.
// Returns (callerSymbolPath, className, methodName) triples for each detected
// call site.
func parseDartCallSites(filePath string, content []byte, registry dartClientRegistry) []dartCallSiteEdge {
	if len(registry) == 0 || len(content) == 0 {
		return nil
	}
	parser := sitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(dartCallSiteLang); err != nil {
		return nil
	}
	tree := parser.ParseCtx(context.Background(), content, nil)
	if tree == nil {
		return nil
	}
	defer tree.Close()

	root := tree.RootNode()
	stem := dartCallSiteFileStem(filePath, root, content)

	// Phase 1: collect bindings: varName/fieldName/getterName → className.
	bindings := dartCollectBindings(root, content, registry)
	if len(bindings) == 0 {
		return nil
	}

	// Phase 2: find method calls on known client bindings.
	return dartCollectCallEdges(root, content, stem, bindings)
}

// dartCallSiteFileStem returns the symbol-path prefix for a Dart source file.
// Uses the `library` directive name when present, otherwise falls back to the
// file basename without extension — mirroring DartGrammar.PackageName().
func dartCallSiteFileStem(filePath string, root *sitter.Node, src []byte) string {
	var libName string
	dartWalkNodes(root, func(n *sitter.Node) bool {
		if n.Kind() != "library_name" {
			return true
		}
		var parts []string
		dartWalkNodes(n, func(c *sitter.Node) bool {
			if c.Kind() == "identifier" {
				parts = append(parts, strings.TrimSpace(c.Utf8Text(src)))
			}
			return true
		})
		libName = strings.Join(parts, ".")
		return false // stop after first library_name
	})
	if libName != "" {
		return libName
	}
	base := filepath.Base(filePath)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// dartCollectBindings scans the AST for direct-constructor bindings to
// grpc-dart client classes. Returns varName/fieldName/getterName → className.
//
// Three shapes are handled:
//
//  1. initialized_variable_definition:  final client = AuthServiceClient(ch);
//  2. assignment_expression:            _client = AuthServiceClient(ch);
//     (also: this._client = AuthServiceClient(ch);)
//  3. getter with expression body:      AuthServiceClient get _c => AuthServiceClient(ch);
func dartCollectBindings(root *sitter.Node, src []byte, registry dartClientRegistry) map[string]string {
	bindings := make(map[string]string)

	dartWalkNodes(root, func(n *sitter.Node) bool {
		switch n.Kind() {
		case "initialized_variable_definition":
			varName, className := dartExtractVarBinding(n, src)
			if varName != "" && className != "" {
				if _, ok := registry[className]; ok {
					bindings[varName] = className
				}
			}
		case "assignment_expression":
			fieldName, className := dartExtractFieldAssignmentBinding(n, src)
			if fieldName != "" && className != "" {
				if _, ok := registry[className]; ok {
					bindings[fieldName] = className
				}
			}
		case "getter_signature":
			// getter_signature is a child of method_signature.
			// The adjacent sibling of method_signature in class_body is function_body.
			getterName := dartGetterName(n, src)
			if getterName == "" {
				break
			}
			ms := n.Parent()
			if ms == nil || ms.Kind() != "method_signature" {
				break
			}
			fb := ms.NextNamedSibling()
			if fb == nil || fb.Kind() != "function_body" {
				break
			}
			className := dartGetterConstructorClass(fb, src)
			if className == "" {
				break
			}
			if _, ok := registry[className]; ok {
				bindings[getterName] = className
			}
		}
		return true
	})
	return bindings
}

// dartExtractVarBinding extracts (varName, constructorClass) from an
// initialized_variable_definition node.
//
//	final client = AuthServiceClient(channel);         → ("client", "AuthServiceClient")
//	AuthServiceClient client = AuthServiceClient(ch);  → ("client", "AuthServiceClient")
//
// Returns ("", "") when the RHS is not a direct constructor call (e.g. GetIt
// chained calls, async futures, or non-constructor expressions).
func dartExtractVarBinding(n *sitter.Node, src []byte) (varName, className string) {
	foundEq := false
	var lastIdentBeforeEq string

	for i := uint(0); i < n.ChildCount(); i++ {
		c := n.Child(i)
		if c == nil {
			continue
		}
		if !foundEq {
			if c.Kind() == "=" {
				foundEq = true
				varName = lastIdentBeforeEq
				continue
			}
			if c.Kind() == "identifier" {
				lastIdentBeforeEq = strings.TrimSpace(c.Utf8Text(src))
			}
			continue
		}
		// After `=`: look for identifier immediately before selector(argument_part).
		// Direct constructor: ClassName(args) → identifier + selector(argument_part)
		// GetIt chain:        GetIt.I<T>()   → identifier + selector(uas) + selector(ap)
		// We only accept the pattern where the FIRST selector after the identifier
		// has argument_part as its direct child — excluding any method-chain selectors.
		if c.Kind() == "identifier" {
			next := c.NextSibling()
			if next != nil && next.Kind() == "selector" && dartSelectorHasArgumentPart(next) {
				className = strings.TrimSpace(c.Utf8Text(src))
				return varName, className
			}
		}
	}
	return "", ""
}

// dartExtractFieldAssignmentBinding extracts (fieldName, constructorClass) from
// an assignment_expression node.
//
//	_client = AuthServiceClient(channel);      → ("_client", "AuthServiceClient")
//	this._chat = ChatServiceClient(channel);   → ("_chat", "ChatServiceClient")
func dartExtractFieldAssignmentBinding(n *sitter.Node, src []byte) (fieldName, className string) {
	foundEq := false

	for i := uint(0); i < n.ChildCount(); i++ {
		c := n.Child(i)
		if c == nil {
			continue
		}
		if !foundEq {
			if c.Kind() == "assignable_expression" {
				fieldName = dartExtractFieldNameFromAssignable(c, src)
			}
			if c.Kind() == "=" {
				foundEq = true
			}
			continue
		}
		// After `=`: same direct-constructor check as in dartExtractVarBinding.
		if c.Kind() == "identifier" {
			next := c.NextSibling()
			if next != nil && next.Kind() == "selector" && dartSelectorHasArgumentPart(next) {
				className = strings.TrimSpace(c.Utf8Text(src))
				return fieldName, className
			}
		}
	}
	return "", ""
}

// dartExtractFieldNameFromAssignable extracts the field name from an
// assignable_expression node:
//
//	`_client`           → "_client"
//	`this._chatClient`  → "_chatClient"
func dartExtractFieldNameFromAssignable(n *sitter.Node, src []byte) string {
	for i := uint(0); i < n.ChildCount(); i++ {
		c := n.Child(i)
		if c == nil {
			continue
		}
		if c.Kind() == "identifier" {
			return strings.TrimSpace(c.Utf8Text(src))
		}
		if c.Kind() == "unconditional_assignable_selector" {
			for j := uint(0); j < c.ChildCount(); j++ {
				cc := c.Child(j)
				if cc != nil && cc.Kind() == "identifier" {
					return strings.TrimSpace(cc.Utf8Text(src))
				}
			}
		}
	}
	return ""
}

// dartGetterName returns the name identifier from a getter_signature node.
func dartGetterName(n *sitter.Node, src []byte) string {
	for i := uint(0); i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		if c != nil && c.Kind() == "identifier" {
			return strings.TrimSpace(c.Utf8Text(src))
		}
	}
	return ""
}

// dartGetterConstructorClass extracts the constructor class from a getter's
// function_body when it is a simple expression body:
//
//	=> AuthServiceClient(_channel);   → "AuthServiceClient"
//
// Returns "" for block bodies or bodies that return non-constructor expressions.
func dartGetterConstructorClass(fb *sitter.Node, src []byte) string {
	// function_body children: ["async"] "=>" identifier selector(argument_part) ";"
	for i := uint(0); i < fb.ChildCount(); i++ {
		c := fb.Child(i)
		if c == nil || c.Kind() != "identifier" {
			continue
		}
		next := c.NextSibling()
		if next != nil && next.Kind() == "selector" && dartSelectorHasArgumentPart(next) {
			return strings.TrimSpace(c.Utf8Text(src))
		}
	}
	return ""
}

// dartSelectorHasArgumentPart reports whether a `selector` node's first
// non-anonymous child is argument_part. This distinguishes direct constructor
// calls (ClassName(args) → selector has argument_part first) from method
// chains (obj.method(args) → selector has unconditional_assignable_selector
// first).
func dartSelectorHasArgumentPart(n *sitter.Node) bool {
	for i := uint(0); i < n.ChildCount(); i++ {
		c := n.Child(i)
		if c == nil {
			continue
		}
		switch c.Kind() {
		case "argument_part":
			return true
		case "unconditional_assignable_selector", "conditional_assignable_selector":
			return false
		}
	}
	return false
}

// dartCollectCallEdges walks the AST and emits dartCallSiteEdge records for
// every method call on a known client binding.
//
// Two AST patterns are detected:
//
// Pattern A — bare identifier call (3-sibling):
//
//	identifier(client)  selector(.method)  selector(args)
//
// Pattern B — this-qualified call (4-sibling):
//
//	this  selector(._field)  selector(.method)  selector(args)
//
// Both patterns appear as consecutive siblings in expression_statement,
// await_expression, return_statement, and other expression contexts.
func dartCollectCallEdges(root *sitter.Node, src []byte, stem string, bindings map[string]string) []dartCallSiteEdge {
	var edges []dartCallSiteEdge
	seen := map[string]bool{}

	dartWalkNodes(root, func(n *sitter.Node) bool {
		cnt := n.ChildCount()
		for i := uint(0); i < cnt; i++ {
			c0 := n.Child(i)
			if c0 == nil {
				continue
			}

			switch c0.Kind() {
			case "identifier":
				// Pattern A: identifier(client) + selector(.method) + selector(args)
				if i+2 >= cnt {
					continue
				}
				c1 := n.Child(i + 1)
				c2 := n.Child(i + 2)
				if c1 == nil || c2 == nil {
					continue
				}
				if c1.Kind() != "selector" || c2.Kind() != "selector" {
					continue
				}
				methodName := dartSelectorMethodName(c1, src)
				if methodName == "" || !dartSelectorHasArgumentPart(c2) {
					continue
				}
				varName := strings.TrimSpace(c0.Utf8Text(src))
				className, ok := bindings[varName]
				if !ok {
					continue
				}
				callerPath := dartEnclosingSymbol(n, src, stem)
				if callerPath == "" {
					continue
				}
				key := callerPath + "→" + className + "." + methodName
				if !seen[key] {
					seen[key] = true
					edges = append(edges, dartCallSiteEdge{callerPath: callerPath, className: className, methodName: methodName})
				}

			case "this":
				// Pattern B: this + selector(._field) + selector(.method) + selector(args)
				if i+3 >= cnt {
					continue
				}
				c1 := n.Child(i + 1)
				c2 := n.Child(i + 2)
				c3 := n.Child(i + 3)
				if c1 == nil || c2 == nil || c3 == nil {
					continue
				}
				if c1.Kind() != "selector" || c2.Kind() != "selector" || c3.Kind() != "selector" {
					continue
				}
				fieldName := dartSelectorMethodName(c1, src) // ._field → "field"
				if fieldName == "" {
					continue
				}
				methodName := dartSelectorMethodName(c2, src)
				if methodName == "" || !dartSelectorHasArgumentPart(c3) {
					continue
				}
				className, ok := bindings[fieldName]
				if !ok {
					continue
				}
				callerPath := dartEnclosingSymbol(n, src, stem)
				if callerPath == "" {
					continue
				}
				key := callerPath + "→" + className + "." + methodName
				if !seen[key] {
					seen[key] = true
					edges = append(edges, dartCallSiteEdge{callerPath: callerPath, className: className, methodName: methodName})
				}
			}
		}
		return true
	})
	return edges
}

// dartSelectorMethodName returns the method name from a `selector` node
// containing an unconditional_assignable_selector:
//
//	selector → unconditional_assignable_selector → . → identifier("login")
//
// Returns "" for selectors that contain argument_part or other kinds.
func dartSelectorMethodName(n *sitter.Node, src []byte) string {
	for i := uint(0); i < n.ChildCount(); i++ {
		c := n.Child(i)
		if c == nil || c.Kind() != "unconditional_assignable_selector" {
			continue
		}
		for j := uint(0); j < c.ChildCount(); j++ {
			cc := c.Child(j)
			if cc != nil && cc.Kind() == "identifier" {
				return strings.TrimSpace(cc.Utf8Text(src))
			}
		}
	}
	return ""
}

// dartEnclosingSymbol walks up the AST parent chain and returns the dotted
// symbol path of the nearest named function/method/getter/constructor scope.
//
// Returns "" when no named enclosure is found — the caller skips the edge.
// Anonymous closures (function_expression) are skipped without stopping the
// walk; only a named enclosure terminates the search.
//
// # Dart AST quirk: function_signature and function_body are siblings
//
// Unlike TypeScript where a function_declaration contains both the identifier
// and the body as children, Dart's tree-sitter grammar emits function_signature
// and function_body (or method_signature and function_body) as sibling nodes
// at the same level in the parent (program or class_body). This means walking
// up from a call site never encounters function_signature as an ancestor.
//
// The workaround: when we walk into a function_body, look at its
// PrevNamedSibling to find the associated function_signature / method_signature,
// and extract the name from there.
func dartEnclosingSymbol(node *sitter.Node, src []byte, stem string) string {
	var funName string
	var classNames []string

	for p := node; p != nil; p = p.Parent() {
		switch p.Kind() {
		case "function_body":
			// function_body and function_signature are siblings in Dart's AST.
			// Peek at the previous named sibling to get the associated signature.
			if funName == "" {
				if prev := p.PrevNamedSibling(); prev != nil {
					funName = dartExtractSignatureName(prev, src)
				}
			}
		case "class_definition":
			for i := uint(0); i < p.NamedChildCount(); i++ {
				c := p.NamedChild(i)
				if c != nil && c.Kind() == "identifier" {
					classNames = append(classNames, strings.TrimSpace(c.Utf8Text(src)))
					break
				}
			}
		}
	}

	if funName == "" {
		return ""
	}
	// classNames is innermost-first; reverse for outermost-first ordering.
	parts := make([]string, 0, 2+len(classNames))
	parts = append(parts, stem)
	for i := len(classNames) - 1; i >= 0; i-- {
		parts = append(parts, classNames[i])
	}
	parts = append(parts, funName)
	return strings.Join(parts, ".")
}

// dartExtractSignatureName extracts the function or method name from a
// function_signature or method_signature node — the node that is the
// PrevNamedSibling of a function_body in Dart's sibling-based AST layout.
func dartExtractSignatureName(n *sitter.Node, src []byte) string {
	switch n.Kind() {
	case "function_signature":
		// Top-level or nested function: function_signature → identifier (name)
		for i := uint(0); i < n.NamedChildCount(); i++ {
			c := n.NamedChild(i)
			if c != nil && c.Kind() == "identifier" {
				return strings.TrimSpace(c.Utf8Text(src))
			}
		}
	case "method_signature":
		// Class member: method_signature → (function_signature | getter_signature |
		// setter_signature | constructor_signature | factory_constructor_signature)
		for i := uint(0); i < n.NamedChildCount(); i++ {
			c := n.NamedChild(i)
			if c == nil {
				continue
			}
			switch c.Kind() {
			case "function_signature", "getter_signature", "setter_signature":
				for j := uint(0); j < c.NamedChildCount(); j++ {
					cc := c.NamedChild(j)
					if cc != nil && cc.Kind() == "identifier" {
						return strings.TrimSpace(cc.Utf8Text(src))
					}
				}
			case "constructor_signature", "factory_constructor_signature":
				return dartEnclosingCtorName(c, src)
			}
		}
	}
	return ""
}

// dartEnclosingCtorName extracts the symbol name from a constructor_signature
// or factory_constructor_signature node. Named constructors (Foo.named) return
// only the named segment so the path reads pkg.Foo.named — not pkg.Foo.Foo.named.
func dartEnclosingCtorName(n *sitter.Node, src []byte) string {
	var ids []string
	for i := uint(0); i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		if c != nil && c.Kind() == "identifier" {
			ids = append(ids, strings.TrimSpace(c.Utf8Text(src)))
		}
	}
	switch len(ids) {
	case 0:
		return ""
	case 1:
		return ids[0]
	default:
		return ids[1] // named constructor — use the named segment
	}
}

// isDartGeneratedPath reports whether a .dart path is a protoc-generated file.
// Generated files are the definition side (linked via implements_rpc), not
// call sites. Scanning them would produce false positives and waste CPU.
func isDartGeneratedPath(path string) bool {
	base := filepath.Base(path)
	for _, suf := range []string{
		".pb.dart",
		".pbenum.dart",
		".pbgrpc.dart",
		".pbjson.dart",
		".g.dart",
		".freezed.dart",
	} {
		if strings.HasSuffix(base, suf) {
			return true
		}
	}
	return false
}

// dartWalkNodes visits every node in the AST (DFS pre-order). The visitor
// returns true to descend into children, false to skip the subtree.
func dartWalkNodes(root *sitter.Node, visit func(*sitter.Node) bool) {
	if root == nil || !visit(root) {
		return
	}
	for i := uint(0); i < root.ChildCount(); i++ {
		dartWalkNodes(root.Child(i), visit)
	}
}
