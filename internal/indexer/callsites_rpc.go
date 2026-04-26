package indexer

// callsites_rpc.go — post-graph-pass call_rpc edge emitter (lib-4g2.3).
//
// This file implements two concerns:
//
//  1. buildCallRPCEdges (the post-pass resolver): reads the store for
//     connect-es stub nodes, scans all non-stub TS/JS code files, and emits
//     call_rpc edges.
//
//  2. callSiteParser (the tree-sitter detector): pure function that parses a
//     single TS/JS/TSX file and returns (callerSymbolPath, rpcPath) pairs.
//     Co-located here rather than in internal/indexer/handlers/code/connectes/
//     to avoid an import cycle (that package imports indexer for the handler
//     interface; this file needs to import nothing from that package in return).
//
// # v1 limitations (file-scoped only)
//
//   - Re-exported clients (authClient from lib/clients.ts called in app/page.tsx)
//     — NOT linked. Cross-file v2 tracked in lib-44f.
//   - Dynamic method selection (client[methodName](req)) — NOT linked.
//   - Custom hook wrappers (useClient()) unless in the same file — NOT linked.
//   - Interceptor-wrapped clients that alias method names — NOT linked.
//
// # Enclosing-function skip rule
//
// When no named function/method/const ancestor is found for a call site (e.g.
// module-level call, inline JSX arrow prop), the edge is skipped rather than
// emitted with a dangling source node id.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"

	tree_sitter_typescript "librarian/internal/indexer/handlers/code/tree_sitter_typescript/typescript"
	"librarian/internal/store"
)

// tsCallSiteLang is the tree-sitter TypeScript language used by the call-site
// detector. Initialised once at package load; mirrors the tsLang var in the
// connectes package but kept separate to avoid an import cycle.
var tsCallSiteLang = sitter.NewLanguage(tree_sitter_typescript.Language())

// tsCallableExtensions lists the file extensions the call-site detector scans.
var tsCallableExtensions = map[string]bool{
	".ts":  true,
	".tsx": true,
	".js":  true,
	".jsx": true,
	".mts": true,
	".mjs": true,
	".cts": true,
	".cjs": true,
}

// connectStubSuffixes are the path suffixes (without extension) that identify
// a connect-es generated file. Mirrors hasConnectSuffix in the connectes
// handler.
var connectStubSuffixes = []string{"_connect", "_connectweb"}

// clientFactorySources lists npm packages whose createPromiseClient /
// createClient exports are treated as Connect-ES client factory functions.
var clientFactorySources = map[string]bool{
	"@connectrpc/connect":     true,
	"@bufbuild/connect":       true,
	"@connectrpc/connect-web": true,
}

// callSiteEdge is a (callerSymbolPath, rpcPath) pair detected in a single file.
type callSiteEdge struct {
	callerPath string // e.g. "page.Page"
	rpcPath    string // e.g. "auth.v1.AuthService.login"
}

// connectExportIndex maps the basename stem of a connect-es file to a map of
// exported const name → proto typeName.
//
// Example: "auth_connect" → {"AuthService": "auth.v1.AuthService"}
type connectExportIndex map[string]map[string]string

// buildCallRPCEdges is the post-graph-pass resolver that emits call_rpc edges
// from TS/JS call sites to their proto rpc declarations.
//
// Runs after buildImplementsRPCEdges so all connect-es stub symbol nodes
// (from lib-4g2.2) exist when this resolver probes them.
func (idx *Indexer) buildCallRPCEdges(result *GraphResult) {
	index, err := idx.buildConnectExportIndex()
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("callsites_rpc: build index: %s", err))
		return
	}
	if len(index) == 0 {
		return // No connect-es stubs in project → nothing to link.
	}

	codeFileNodes, err := idx.store.ListNodesByKind(store.NodeKindCodeFile)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("callsites_rpc: list code_file nodes: %s", err))
		return
	}

	root := idx.cfg.ProjectRoot

	for _, node := range codeFileNodes {
		path := node.SourcePath
		if path == "" {
			continue
		}
		ext := strings.ToLower(filepath.Ext(path))
		if !tsCallableExtensions[ext] {
			continue
		}
		if isConnectStubPath(path) {
			continue
		}

		content, err := readCallSiteFile(root, path)
		if err != nil {
			// Missing or unreadable: file was deleted after the per-file pass.
			continue
		}

		edges := parseCallSites(path, content, index)
		for _, e := range edges {
			callerID := store.SymbolNodeID(e.callerPath)
			stubID := store.SymbolNodeID(e.rpcPath) // lowerCamelCase stub symbol

			callerNode, err := idx.store.GetNode(callerID)
			if err != nil || callerNode == nil {
				continue
			}

			// Resolve the connect-es stub (lowerCamelCase) to its proto rpc
			// (PascalCase) via the implements_rpc outgoing edge. The stub node
			// auth.v1.AuthService.login carries an implements_rpc edge to the
			// proto rpc auth.v1.AuthService.Login. Emitting call_rpc to the proto
			// rpc keeps the edge semantics (sym:<caller> → sym:<proto_rpc>) and
			// makes runTraceRPC's Neighbors(protoRpcID, "in", EdgeKindCallRPC)
			// query return results without special-casing lowerCamelCase.
			protoEdges, err := idx.store.Neighbors(stubID, "out", store.EdgeKindImplementsRPC)
			if err != nil || len(protoEdges) == 0 {
				// Stub has no implements_rpc link (proto not yet indexed or no match).
				// Skip rather than emit a dangling call_rpc to a non-rpc node.
				continue
			}
			protoRPCID := protoEdges[0].To

			if err := idx.store.UpsertEdge(store.Edge{
				From: callerID,
				To:   protoRPCID,
				Kind: store.EdgeKindCallRPC,
			}); err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("callsites_rpc: upsert edge %s→%s: %s", callerID, protoRPCID, err))
				continue
			}
			result.EdgesAdded++
		}
	}
}

// buildConnectExportIndex queries connect-es stub symbol nodes and returns a
// connectExportIndex: connectBasename → (constName → typeName).
func (idx *Indexer) buildConnectExportIndex() (connectExportIndex, error) {
	nodes, err := idx.store.ListSymbolNodesWithMetadataContaining(`"connect_es_stub"`)
	if err != nil {
		return nil, fmt.Errorf("list connect-es nodes: %w", err)
	}

	index := make(connectExportIndex)
	for _, n := range nodes {
		if n.SourcePath == "" {
			continue
		}
		var meta struct {
			ConnectESStub   bool   `json:"connect_es_stub"`
			ServiceTypeName string `json:"service_typename"`
			ConstName       string `json:"const_name"`
		}
		if n.Metadata == "" {
			continue
		}
		if err := json.Unmarshal([]byte(n.Metadata), &meta); err != nil || !meta.ConnectESStub {
			continue
		}
		if meta.ServiceTypeName == "" || meta.ConstName == "" {
			continue
		}
		basename := connectStubBasename(n.SourcePath)
		if basename == "" {
			continue
		}
		if index[basename] == nil {
			index[basename] = make(map[string]string)
		}
		index[basename][meta.ConstName] = meta.ServiceTypeName
	}
	return index, nil
}

// ── tree-sitter call-site parser ───────────────────────────────────────────

// parseCallSites finds Connect-ES client call sites in a TS/JS source file.
// Returns (callerSymbolPath, rpcPath) pairs for each detected call site.
func parseCallSites(filePath string, content []byte, index connectExportIndex) []callSiteEdge {
	if len(index) == 0 || len(content) == 0 {
		return nil
	}
	parser := sitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(tsCallSiteLang); err != nil {
		return nil
	}
	tree := parser.ParseCtx(context.Background(), content, nil)
	if tree == nil {
		return nil
	}
	defer tree.Close()

	stem := callSiteFilenameStem(filePath)
	root := tree.RootNode()

	factories, bindings := callSiteScanImports(root, content, index)
	if len(factories) == 0 && len(bindings) == 0 {
		return nil
	}

	clientVars, destructured := callSiteCollectBindings(root, content, factories, bindings)
	if len(clientVars) == 0 && len(destructured) == 0 {
		return nil
	}

	return callSiteCollectEdges(root, content, stem, clientVars, destructured)
}

// callSiteScanImports walks import_statement nodes and returns:
//   - factories: local names of createPromiseClient / createClient from Connect
//   - bindings:  local import name → proto typeName for connect-es service imports
func callSiteScanImports(root *sitter.Node, src []byte, index connectExportIndex) (factories map[string]bool, bindings map[string]string) {
	factories = make(map[string]bool)
	bindings = make(map[string]string)

	callSiteWalkNodes(root, func(n *sitter.Node) bool {
		if n.Kind() != "import_statement" {
			return true
		}
		module := callSiteImportModuleString(n, src)
		if module == "" {
			return false
		}
		if clientFactorySources[module] {
			callSiteForEachNamedImport(n, src, func(exported, local string) {
				if exported == "createPromiseClient" || exported == "createClient" {
					factories[local] = true
				}
			})
			return false
		}
		basename := connectStubBasename(module)
		if basename == "" {
			return false
		}
		exports, ok := index[basename]
		if !ok {
			return false
		}
		callSiteForEachNamedImport(n, src, func(exported, local string) {
			if typeName, found := exports[exported]; found {
				bindings[local] = typeName
			}
		})
		return false
	})
	return factories, bindings
}

// callSiteCollectBindings walks variable_declarator nodes that contain a
// createPromiseClient / createClient call.
//
//   - clientVars:   varName → typeName (const client = createPromiseClient(...))
//   - destructured: methodName → rpcPath (const { login } = createPromiseClient(...))
func callSiteCollectBindings(root *sitter.Node, src []byte, factories map[string]bool, bindings map[string]string) (clientVars, destructured map[string]string) {
	clientVars = make(map[string]string)
	destructured = make(map[string]string)

	callSiteWalkNodes(root, func(n *sitter.Node) bool {
		if n.Kind() != "variable_declarator" {
			return true
		}
		value := n.ChildByFieldName("value")
		if value == nil {
			return false
		}
		actual := value
		// Unwrap await_expression defensively.
		if actual.Kind() == "await_expression" {
			if inner := actual.NamedChild(0); inner != nil {
				actual = inner
			}
		}
		if actual.Kind() != "call_expression" {
			return false
		}
		fn := actual.ChildByFieldName("function")
		if fn == nil || !factories[fn.Utf8Text(src)] {
			return false
		}
		args := actual.ChildByFieldName("arguments")
		if args == nil {
			return false
		}
		var firstArg *sitter.Node
		for i := uint(0); i < args.NamedChildCount(); i++ {
			if c := args.NamedChild(i); c != nil {
				firstArg = c
				break
			}
		}
		if firstArg == nil || firstArg.Kind() != "identifier" {
			return false
		}
		typeName, ok := bindings[firstArg.Utf8Text(src)]
		if !ok {
			return false
		}
		nameNode := n.ChildByFieldName("name")
		if nameNode == nil {
			return false
		}
		switch nameNode.Kind() {
		case "identifier":
			clientVars[nameNode.Utf8Text(src)] = typeName
		case "object_pattern":
			for i := uint(0); i < nameNode.NamedChildCount(); i++ {
				prop := nameNode.NamedChild(i)
				if prop == nil {
					continue
				}
				var methodName string
				switch prop.Kind() {
				case "shorthand_property_identifier_pattern":
					methodName = prop.Utf8Text(src)
				case "pair_pattern":
					if keyNode := prop.ChildByFieldName("key"); keyNode != nil {
						methodName = keyNode.Utf8Text(src)
					}
				}
				if methodName != "" {
					destructured[methodName] = typeName + "." + methodName
				}
			}
		}
		return false
	})
	return clientVars, destructured
}

// callSiteCollectEdges walks call_expression nodes and emits edges for:
//   - member_expression calls: `client.login(req)` where client is in clientVars
//   - identifier calls:        `login(req)` where login is in destructured
func callSiteCollectEdges(root *sitter.Node, src []byte, stem string, clientVars, destructured map[string]string) []callSiteEdge {
	var edges []callSiteEdge
	seen := map[string]bool{}

	callSiteWalkNodes(root, func(n *sitter.Node) bool {
		if n.Kind() != "call_expression" {
			return true
		}
		fn := n.ChildByFieldName("function")
		if fn == nil {
			return true
		}
		var rpcPath string
		switch fn.Kind() {
		case "member_expression":
			obj := fn.ChildByFieldName("object")
			prop := fn.ChildByFieldName("property")
			if obj == nil || prop == nil || obj.Kind() != "identifier" {
				return true
			}
			typeName, ok := clientVars[obj.Utf8Text(src)]
			if !ok {
				return true
			}
			rpcPath = typeName + "." + prop.Utf8Text(src)
		case "identifier":
			rpcP, ok := destructured[fn.Utf8Text(src)]
			if !ok {
				return true
			}
			rpcPath = rpcP
		default:
			return true
		}

		callerPath := callSiteEnclosingSymbol(n, src, stem)
		if callerPath == "" {
			return true
		}
		key := callerPath + "→" + rpcPath
		if !seen[key] {
			seen[key] = true
			edges = append(edges, callSiteEdge{callerPath: callerPath, rpcPath: rpcPath})
		}
		return true
	})
	return edges
}

// callSiteEnclosingSymbol walks up the AST parent chain and returns the dotted
// symbol path of the nearest named function/method/const scope.
//
// Returns "" when no named enclosure is found — the caller skips the edge.
// See the package godoc for the full skip rule.
func callSiteEnclosingSymbol(node *sitter.Node, src []byte, stem string) string {
	var funName string
	var classNames []string

	for p := node.Parent(); p != nil; p = p.Parent() {
		switch p.Kind() {
		case "function_declaration", "method_definition":
			if funName == "" {
				if nameNode := p.ChildByFieldName("name"); nameNode != nil {
					funName = nameNode.Utf8Text(src)
				}
			}
		case "variable_declarator":
			if funName == "" {
				value := p.ChildByFieldName("value")
				if value != nil && (value.Kind() == "arrow_function" || value.Kind() == "function_expression") {
					if nameNode := p.ChildByFieldName("name"); nameNode != nil {
						funName = nameNode.Utf8Text(src)
					}
				}
			}
		case "class_declaration", "class_expression", "class":
			if nameNode := p.ChildByFieldName("name"); nameNode != nil {
				classNames = append(classNames, nameNode.Utf8Text(src))
			}
		}
	}
	if funName == "" {
		return ""
	}
	// classNames is innermost-first; reverse for outermost-first.
	parts := make([]string, 0, 2+len(classNames))
	parts = append(parts, stem)
	for i := len(classNames) - 1; i >= 0; i-- {
		parts = append(parts, classNames[i])
	}
	parts = append(parts, funName)
	return strings.Join(parts, ".")
}

// ── helpers ────────────────────────────────────────────────────────────────

// connectStubBasename returns the lower-cased basename stem if path ends with
// a connect-es stub suffix ("_connect" / "_connectweb"), otherwise "".
func connectStubBasename(path string) string {
	base := filepath.Base(path)
	ext := filepath.Ext(base)
	switch ext {
	case ".ts", ".js", ".tsx", ".jsx", ".mts", ".mjs", ".cts", ".cjs":
		base = strings.TrimSuffix(base, ext)
	}
	lower := strings.ToLower(base)
	for _, suf := range connectStubSuffixes {
		if strings.HasSuffix(lower, suf) {
			return lower
		}
	}
	return ""
}

// isConnectStubPath reports whether path is a connect-es generated stub file.
func isConnectStubPath(path string) bool {
	return connectStubBasename(path) != ""
}

// callSiteFilenameStem returns the basename without the final extension,
// matching the TS/JS grammar handler's PackageName fallback.
func callSiteFilenameStem(path string) string {
	base := filepath.Base(path)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// readCallSiteFile reads a workspace-relative source file from disk.
func readCallSiteFile(root, path string) ([]byte, error) {
	abs := path
	if !filepath.IsAbs(path) {
		if root != "" {
			abs = filepath.Join(root, path)
		} else {
			var err error
			abs, err = filepath.Abs(path)
			if err != nil {
				return nil, err
			}
		}
	}
	return os.ReadFile(abs)
}

// callSiteImportModuleString extracts the module specifier string from an
// import_statement node.
func callSiteImportModuleString(n *sitter.Node, src []byte) string {
	for i := uint(0); i < n.ChildCount(); i++ {
		c := n.Child(i)
		if c == nil || c.Kind() != "string" {
			continue
		}
		return callSiteStripQuotes(c.Utf8Text(src))
	}
	return ""
}

// callSiteForEachNamedImport calls fn for every named import specifier.
// fn(exportedName, localName): for `{ X }` both are "X"; for `{ X as Y }`
// exportedName="X", localName="Y".
func callSiteForEachNamedImport(n *sitter.Node, src []byte, fn func(exported, local string)) {
	for i := uint(0); i < n.NamedChildCount(); i++ {
		clause := n.NamedChild(i)
		if clause == nil || clause.Kind() != "import_clause" {
			continue
		}
		for j := uint(0); j < clause.NamedChildCount(); j++ {
			namedImports := clause.NamedChild(j)
			if namedImports == nil || namedImports.Kind() != "named_imports" {
				continue
			}
			for k := uint(0); k < namedImports.NamedChildCount(); k++ {
				spec := namedImports.NamedChild(k)
				if spec == nil || spec.Kind() != "import_specifier" {
					continue
				}
				nameNode := spec.ChildByFieldName("name")
				aliasNode := spec.ChildByFieldName("alias")
				if nameNode == nil {
					continue
				}
				exported := nameNode.Utf8Text(src)
				local := exported
				if aliasNode != nil {
					local = aliasNode.Utf8Text(src)
				}
				fn(exported, local)
			}
		}
	}
}

// callSiteStripQuotes removes a single surrounding pair of quotes from a
// tree-sitter string node's raw text. Mirrors stripTSStringQuotes in connectes.
func callSiteStripQuotes(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 {
		first, last := s[0], s[len(s)-1]
		if (first == '"' || first == '\'' || first == '`') && first == last {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// callSiteWalkNodes visits every node in the AST (DFS pre-order). The visitor
// returns true to descend into children, false to skip the subtree.
func callSiteWalkNodes(root *sitter.Node, visit func(*sitter.Node) bool) {
	if root == nil || !visit(root) {
		return
	}
	for i := uint(0); i < root.ChildCount(); i++ {
		callSiteWalkNodes(root.Child(i), visit)
	}
}
