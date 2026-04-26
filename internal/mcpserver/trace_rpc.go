package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"librarian/internal/config"
	"librarian/internal/store"
)

// traceRPCProtoMetadataMarker is the literal substring every proto rpc symbol
// node carries in its metadata — the proto grammar's SymbolMetadata is the
// sole emitter of `"input_type":` on Unit.Metadata. Matches the marker used by
// the implements_rpc resolver (internal/indexer/implements_rpc.go).
const traceRPCProtoMetadataMarker = `"input_type":`

// traceRPCFieldMetadataMarker identifies proto field symbol nodes via the
// literal substring the proto grammar writes on every field / map_field /
// oneof_field Unit metadata. Used by the message-field resolver to pull
// every field in one scan instead of walking id prefixes.
const traceRPCFieldMetadataMarker = `"field_number":`

// traceRPCDefinition describes the proto declaration surface returned to callers.
type traceRPCDefinition struct {
	Service         string `json:"service"`
	Method          string `json:"method"`
	Package         string `json:"package,omitempty"`
	FullyQualified  string `json:"fully_qualified"`
	SymbolID        string `json:"symbol_id"`
	InputType       string `json:"input_type,omitempty"`
	OutputType      string `json:"output_type,omitempty"`
	ClientStreaming bool   `json:"client_streaming"`
	ServerStreaming bool   `json:"server_streaming"`
	ProtoFile       string `json:"proto_file,omitempty"`
	LineNumber      int    `json:"line_number,omitempty"`
	Docstring       string `json:"docstring,omitempty"`
}

// traceRPCImplementation is one generated-code binding of the rpc. LineNumber
// is best-effort (scans the file for the method name); see
// scanTraceRPCMethodLine for the heuristic and the matching follow-up in
// lib-cta to persist line numbers in graph_nodes instead.
type traceRPCImplementation struct {
	Language   string `json:"language,omitempty"`
	SymbolPath string `json:"symbol_path"`
	SymbolID   string `json:"symbol_id"`
	File       string `json:"file,omitempty"`
	LineNumber int    `json:"line_number,omitempty"`
	Kind       string `json:"kind,omitempty"` // server | client | stub | base | service
}

// traceRPCField describes a single field inside an input/output message.
type traceRPCField struct {
	Name        string `json:"name"`
	FieldNumber int    `json:"field_number,omitempty"`
	OneOf       string `json:"oneof,omitempty"`
}

// traceRPCMessage is the resolved definition of an rpc input or output type.
// NestedMessages holds recursively-resolved direct child messages — bead spec
// requires the resolver to descend into nested message types so an AI
// assistant sees the full shape, not just the top-level field list. Depth is
// capped at traceRPCMaxNestedDepth; oneof containers are inlined into Fields
// (via Field.OneOf), not duplicated under NestedMessages.
type traceRPCMessage struct {
	TypeName       string            `json:"type_name"`
	FullyQualified string            `json:"fully_qualified,omitempty"`
	SymbolID       string            `json:"symbol_id,omitempty"`
	ProtoFile      string            `json:"proto_file,omitempty"`
	Fields         []traceRPCField   `json:"fields"`
	NestedMessages []traceRPCMessage `json:"nested_messages,omitempty"`
	Resolved       bool              `json:"resolved"`
	Note           string            `json:"note,omitempty"`
}

// traceRPCRelated is one sibling rpc on the same service.
type traceRPCRelated struct {
	Method          string `json:"method"`
	SymbolID        string `json:"symbol_id"`
	InputType       string `json:"input_type,omitempty"`
	OutputType      string `json:"output_type,omitempty"`
	ClientStreaming bool   `json:"client_streaming,omitempty"`
	ServerStreaming bool   `json:"server_streaming,omitempty"`
}

// traceRPCCaller is a transitive call site reaching an implementation symbol.
// Depth is the BFS distance (1 = direct caller). LineNumber is best-effort
// via file scan for the caller's symbol name.
type traceRPCCaller struct {
	Language   string `json:"language,omitempty"`
	SymbolPath string `json:"symbol_path"`
	File       string `json:"file,omitempty"`
	LineNumber int    `json:"line_number,omitempty"`
	Depth      int    `json:"depth,omitempty"`
}

// traceRPCResult is the top-level response shape.
//
// Warnings collects non-fatal glitches the tool silently papered over (e.g.
// dangling edges, GetNode I/O errors during iteration). A reviewer
// previously flagged these as silent swallows; surfacing them here lets the
// caller see when the trace is less complete than it should be while still
// returning the parts that did resolve cleanly.
type traceRPCResult struct {
	Input           string                   `json:"input"`
	Definition      traceRPCDefinition       `json:"definition"`
	Implementations []traceRPCImplementation `json:"implementations"`
	Callers         []traceRPCCaller         `json:"callers"`
	CallersNote     string                   `json:"callers_note,omitempty"`
	InputMessage    *traceRPCMessage         `json:"input_message,omitempty"`
	OutputMessage   *traceRPCMessage         `json:"output_message,omitempty"`
	RelatedRPCs     []traceRPCRelated        `json:"related_rpcs"`
	Warnings        []string                 `json:"warnings,omitempty"`
}

// traceRPCMaxNestedDepth caps traceRPCMessage recursion into child messages
// so a self-referential proto (or indexer bug that produces a cycle) can't
// blow the stack or amplify output unboundedly. Proto nesting beyond 2–3
// levels is vanishingly rare in practice.
const traceRPCMaxNestedDepth = 3

// traceRPCMaxCallerBFSDepth caps the transitive caller walk. Bead spec calls
// for transitive callers, but an uncapped BFS on a large project's call graph
// would dominate the response. 3 hops gives "caller, caller-of-caller,
// caller-of-caller-of-caller" which is enough to trace typical gRPC handler
// chains without drowning the output.
const traceRPCMaxCallerBFSDepth = 3

// registerTraceRPC wires the trace_rpc tool into the MCP server. projectRoot
// (usually cfg.ProjectRoot) is used to resolve SourcePath values stored in the
// graph to absolute filesystem paths when the tool reads proto sources for
// best-effort line/docstring extraction. Empty string → fall back to CWD.
func registerTraceRPC(s *server.MCPServer, st *store.Store, cfg *config.Config) {
	tool := mcp.NewTool("trace_rpc",
		mcp.WithDescription("End-to-end trace of a gRPC rpc in one call: proto declaration, every language's generated-code implementation linked by implements_rpc edges, input/output message fields, and sibling rpcs on the same service. Accepts the rpc identifier by full symbol ID, dotted path, service+method, or file+method."),
		mcp.WithString("rpc",
			mcp.Required(),
			mcp.Description("RPC identifier. Accepted forms: full symbol ID ('sym:auth.v1.AuthService.Login'), dotted path ('auth.v1.AuthService.Login'), service+method suffix ('AuthService.Login'), or file+method ('api/auth.proto:Login')."),
		),
		mcp.WithString("format",
			mcp.Description("Output format: 'markdown' (default, human/TTY-friendly) or 'json' (structured)."),
			mcp.DefaultString("markdown"),
			mcp.Enum("markdown", "json"),
		),
		mcp.WithReadOnlyHintAnnotation(true),
	)

	projectRoot := ""
	if cfg != nil {
		projectRoot = cfg.ProjectRoot
	}

	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		rpcInput, err := req.RequireString("rpc")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		format := strings.ToLower(strings.TrimSpace(req.GetString("format", "markdown")))
		if format == "" {
			format = "markdown"
		}
		if format != "markdown" && format != "json" {
			return mcp.NewToolResultError(fmt.Sprintf("trace_rpc: unsupported format %q (want 'markdown' or 'json')", format)), nil
		}

		result, err := runTraceRPC(st, projectRoot, rpcInput)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		if format == "json" {
			buf, jerr := json.MarshalIndent(result, "", "  ")
			if jerr != nil {
				return mcp.NewToolResultError(fmt.Sprintf("trace_rpc: marshal json: %v", jerr)), nil
			}
			return mcp.NewToolResultText(string(buf)), nil
		}
		return mcp.NewToolResultText(renderTraceRPCMarkdown(result)), nil
	})
}

// runTraceRPC is the core logic behind the MCP tool. Split from the MCP
// registration so tests can drive it directly with an in-process Store,
// without threading a full server through fixtures.
//
// projectRoot resolves SourcePath (workspace-relative) to absolute paths for
// best-effort proto-file reads. Empty → filepath.Abs on SourcePath (i.e. CWD).
func runTraceRPC(st *store.Store, projectRoot, rpcInput string) (*traceRPCResult, error) {
	rpcInput = strings.TrimSpace(rpcInput)
	if rpcInput == "" {
		return nil, fmt.Errorf("trace_rpc: empty rpc input")
	}

	rpcNodes, err := st.ListSymbolNodesWithMetadataContaining(traceRPCProtoMetadataMarker)
	if err != nil {
		return nil, fmt.Errorf("trace_rpc: list rpc nodes: %w", err)
	}

	node, err := resolveRPCNode(rpcInput, rpcNodes)
	if err != nil {
		return nil, err
	}

	result := &traceRPCResult{
		Input:           rpcInput,
		Implementations: []traceRPCImplementation{},
		Callers:         []traceRPCCaller{},
		RelatedRPCs:     []traceRPCRelated{},
	}

	// Definition
	def, err := buildTraceRPCDefinition(node, projectRoot)
	if err != nil {
		return nil, err
	}
	result.Definition = def
	pkg := def.Package
	rpcPath := strings.TrimPrefix(node.ID, "sym:")

	// Implementations: incoming implements_rpc edges.
	impls, err := st.Neighbors(node.ID, "in", store.EdgeKindImplementsRPC)
	if err != nil {
		return nil, fmt.Errorf("trace_rpc: neighbors implements_rpc: %w", err)
	}
	for _, e := range impls {
		implNode, err := st.GetNode(e.From)
		if err != nil {
			// Defensive: unreachable under current sqlite semantics —
			// store.GetNode normalises sql.ErrNoRows to (nil, nil), so the
			// error branch fires only on driver-layer I/O failures (disk
			// corruption, mid-query close) we can't trigger from
			// single-process tests without a storeReader interface seam.
			// Tracked as lib-edm.
			result.Warnings = append(result.Warnings, fmt.Sprintf("trace_rpc: failed to load implementation node %s: %v", e.From, err))
			continue
		}
		if implNode == nil {
			// Dangling edge — the target node was deleted between the
			// Neighbors call and this GetNode. Surface it so partial
			// results don't silently hide stale state.
			result.Warnings = append(result.Warnings, fmt.Sprintf("trace_rpc: implementation node %s missing (dangling edge)", e.From))
			continue
		}
		result.Implementations = append(result.Implementations, buildTraceRPCImplementation(implNode, def.Service, projectRoot))
	}
	sort.Slice(result.Implementations, func(i, j int) bool {
		a, b := result.Implementations[i], result.Implementations[j]
		if a.Language != b.Language {
			return a.Language < b.Language
		}
		return a.SymbolPath < b.SymbolPath
	})

	// Related rpcs: all rpc symbol nodes that share the service prefix (excluding
	// the target rpc itself). Covers the whole service even if other files contain
	// the same service declaration — proto doesn't allow split services, but
	// the suffix check is cheap and self-consistent.
	result.RelatedRPCs = findTraceRPCRelated(rpcPath, rpcNodes)

	// Input / output message resolution.
	if def.InputType != "" {
		m := resolveTraceRPCMessage(st, pkg, def.InputType)
		result.InputMessage = &m
	}
	if def.OutputType != "" {
		m := resolveTraceRPCMessage(st, pkg, def.OutputType)
		result.OutputMessage = &m
	}

	// Callers: two sources, merged before the final sort.
	//
	// (1) Transitive incoming "call" edges seeded from every implementation
	//     symbol, walked BFS up to traceRPCMaxCallerBFSDepth hops. This covers
	//     Go/Dart server-side callers that invoke the implementation via "call"
	//     edges.
	//
	// (2) Direct incoming "call_rpc" edges on the proto rpc node itself (lib-4g2.3).
	//     These are emitted by the TS/JS Connect-ES call-site detector for
	//     Next.js callers that use createPromiseClient / createClient. Depth=1
	//     since the edge directly joins caller → proto rpc.
	seeds := make([]string, 0, len(result.Implementations))
	for _, impl := range result.Implementations {
		seeds = append(seeds, impl.SymbolID)
	}
	result.Callers = walkTraceRPCCallers(st, seeds, traceRPCMaxCallerBFSDepth, projectRoot, &result.Warnings)

	// Collect call_rpc direct callers on the proto rpc node, deduplicating
	// against any already surfaced via the "call" BFS.
	callRPCEdges, err := st.Neighbors(node.ID, "in", store.EdgeKindCallRPC)
	if err != nil {
		result.Warnings = append(result.Warnings, fmt.Sprintf("trace_rpc: neighbors(call_rpc) on %s: %v", node.ID, err))
	} else {
		seenPaths := make(map[string]bool, len(result.Callers))
		for _, c := range result.Callers {
			seenPaths[c.SymbolPath] = true
		}
		for _, e := range callRPCEdges {
			callerNode, err := st.GetNode(e.From)
			if err != nil {
				result.Warnings = append(result.Warnings, fmt.Sprintf("trace_rpc: failed to load call_rpc caller %s: %v", e.From, err))
				continue
			}
			if callerNode == nil {
				result.Warnings = append(result.Warnings, fmt.Sprintf("trace_rpc: call_rpc caller node %s missing (dangling edge)", e.From))
				continue
			}
			symPath := strings.TrimPrefix(callerNode.ID, "sym:")
			if seenPaths[symPath] {
				continue
			}
			seenPaths[symPath] = true
			line := 0
			if callerNode.SourcePath != "" {
				symName := symPath
				if idx := strings.LastIndex(symPath, "."); idx >= 0 {
					symName = symPath[idx+1:]
				}
				line = scanTraceRPCMethodLine(resolveTraceRPCPath(projectRoot, callerNode.SourcePath), symName)
			}
			result.Callers = append(result.Callers, traceRPCCaller{
				Language:   traceRPCLanguageFromSourcePath(callerNode.SourcePath),
				SymbolPath: symPath,
				File:       callerNode.SourcePath,
				LineNumber: line,
				Depth:      1,
			})
		}
	}

	if len(result.Callers) == 0 {
		result.CallersNote = "No call edges found. The indexer does not emit call edges at this pass, so caller trace is unavailable — absence does not imply the rpc has no callers."
	}
	sort.Slice(result.Callers, func(i, j int) bool {
		if result.Callers[i].Depth != result.Callers[j].Depth {
			return result.Callers[i].Depth < result.Callers[j].Depth
		}
		return result.Callers[i].SymbolPath < result.Callers[j].SymbolPath
	})

	return result, nil
}

// walkTraceRPCCallers does BFS over incoming "call" edges starting from seeds.
// Each hop past the seeds (depth 1+) produces a traceRPCCaller. The seeds
// themselves are excluded — they're the implementation symbols, not callers.
// maxDepth=0 returns an empty list; practical values are 2–3.
//
// Accumulates structural glitches into warnings rather than aborting: a
// single failed Neighbors or GetNode should not wipe the whole caller trace
// when other branches are healthy. The BFS is bounded so an unexpectedly
// dense call graph can't blow memory — seen[] dedupes revisits.
func walkTraceRPCCallers(st *store.Store, seeds []string, maxDepth int, projectRoot string, warnings *[]string) []traceRPCCaller {
	if maxDepth <= 0 {
		return nil
	}
	seen := map[string]bool{}
	for _, s := range seeds {
		seen[s] = true
	}
	frontier := append([]string(nil), seeds...)
	var out []traceRPCCaller
	for depth := 1; depth <= maxDepth && len(frontier) > 0; depth++ {
		var next []string
		for _, node := range frontier {
			edges, err := st.Neighbors(node, "in", "call")
			if err != nil {
				*warnings = append(*warnings, fmt.Sprintf("trace_rpc: neighbors(call) on %s: %v", node, err))
				continue
			}
			for _, e := range edges {
				if seen[e.From] {
					continue
				}
				// Mark seen before the GetNode check so a failed lookup
				// isn't retried on a later iteration. But only queue for
				// the next BFS depth AFTER the node resolves successfully
				// — queuing a dangling/errored source would spend a
				// round-trip on Neighbors(badID) that'd return zero edges
				// and a warning.
				seen[e.From] = true
				callerNode, err := st.GetNode(e.From)
				if err != nil {
					// Defensive: unreachable under current sqlite semantics —
					// store.GetNode normalises sql.ErrNoRows to (nil, nil),
					// so this branch only fires on driver-layer I/O failures
					// (disk corruption, mid-query close) that aren't
					// triggerable from single-process tests without a
					// storeReader interface seam. Tracked as lib-edm.
					*warnings = append(*warnings, fmt.Sprintf("trace_rpc: failed to load caller node %s: %v", e.From, err))
					continue
				}
				if callerNode == nil {
					*warnings = append(*warnings, fmt.Sprintf("trace_rpc: caller node %s missing (dangling edge)", e.From))
					continue
				}
				next = append(next, e.From)
				symPath := strings.TrimPrefix(callerNode.ID, "sym:")
				line := 0
				if callerNode.SourcePath != "" {
					symName := symPath
					if idx := strings.LastIndex(symPath, "."); idx >= 0 {
						symName = symPath[idx+1:]
					}
					line = scanTraceRPCMethodLine(resolveTraceRPCPath(projectRoot, callerNode.SourcePath), symName)
				}
				out = append(out, traceRPCCaller{
					Language:   traceRPCLanguageFromSourcePath(callerNode.SourcePath),
					SymbolPath: symPath,
					File:       callerNode.SourcePath,
					LineNumber: line,
					Depth:      depth,
				})
			}
		}
		frontier = next
	}
	return out
}

// resolveRPCNode picks the unique rpc graph_node matching rpcInput, or returns
// an error that echoes the input back (and, on ambiguity, enumerates the
// conflicting candidates so the caller can narrow the query).
//
// Accepted input shapes:
//
//  1. Full symbol ID starting with "sym:" — looked up verbatim.
//  2. Dotted path ("pkg.Svc.Method" or "Svc.Method") — full match preferred,
//     then trailing-segment suffix match across all rpc nodes. A bare
//     "Method" also works if it's unambiguous project-wide.
//  3. File + method ("api/auth.proto:Login") — narrows by source_path and
//     method basename. The separator is ":" so it can't collide with the
//     "sym:" prefix.
func resolveRPCNode(rpcInput string, candidates []store.Node) (*store.Node, error) {
	if strings.HasPrefix(rpcInput, "sym:") {
		for i := range candidates {
			if candidates[i].ID == rpcInput {
				return &candidates[i], nil
			}
		}
		return nil, fmt.Errorf("trace_rpc: rpc %q not found (no proto rpc node with that symbol id)", rpcInput)
	}

	// file+method form. Must not have been caught by the sym: branch above,
	// so any colon here signals "file:method".
	if idx := strings.Index(rpcInput, ":"); idx >= 0 {
		file := strings.TrimSpace(rpcInput[:idx])
		method := strings.TrimSpace(rpcInput[idx+1:])
		if file == "" || method == "" {
			return nil, fmt.Errorf("trace_rpc: malformed file+method input %q (expected 'path/to.proto:MethodName')", rpcInput)
		}
		var matches []*store.Node
		for i := range candidates {
			if candidates[i].SourcePath != file {
				continue
			}
			if traceRPCMethodFromID(candidates[i].ID) == method {
				matches = append(matches, &candidates[i])
			}
		}
		if len(matches) == 0 {
			return nil, fmt.Errorf("trace_rpc: rpc %q not found (no rpc named %q declared in %q)", rpcInput, method, file)
		}
		if len(matches) > 1 {
			return nil, ambiguousTraceRPCError(rpcInput, matches)
		}
		return matches[0], nil
	}

	// Dotted path. Try an exact full match first — wins when the caller
	// supplied the fully-qualified dotted path.
	full := "sym:" + rpcInput
	var fullMatches []*store.Node
	for i := range candidates {
		if candidates[i].ID == full {
			fullMatches = append(fullMatches, &candidates[i])
		}
	}
	if len(fullMatches) == 1 {
		return fullMatches[0], nil
	}

	// Suffix match: "Svc.Method" or bare "Method" should match any rpc whose
	// dotted path ends with ".<input>". Covers the service+method case and
	// the just-give-me-the-method case.
	suffix := "." + rpcInput
	var suffixMatches []*store.Node
	for i := range candidates {
		path := strings.TrimPrefix(candidates[i].ID, "sym:")
		if path == rpcInput || strings.HasSuffix(path, suffix) {
			suffixMatches = append(suffixMatches, &candidates[i])
		}
	}
	if len(suffixMatches) == 1 {
		return suffixMatches[0], nil
	}
	if len(suffixMatches) == 0 {
		return nil, fmt.Errorf("trace_rpc: rpc %q not found (no proto rpc matches that identifier)", rpcInput)
	}
	return nil, ambiguousTraceRPCError(rpcInput, suffixMatches)
}

// ambiguousTraceRPCError builds a deterministic error listing the candidate
// rpc IDs that matched an under-qualified input.
func ambiguousTraceRPCError(input string, matches []*store.Node) error {
	ids := make([]string, 0, len(matches))
	for _, m := range matches {
		ids = append(ids, m.ID)
	}
	sort.Strings(ids)
	return fmt.Errorf("trace_rpc: rpc %q is ambiguous; %d candidates: %s — qualify with package, service, or file to disambiguate", input, len(ids), strings.Join(ids, ", "))
}

// traceRPCMethodFromID returns the last dotted segment of a symbol id
// (the rpc method name). Returns "" for malformed ids.
func traceRPCMethodFromID(symID string) string {
	path := strings.TrimPrefix(symID, "sym:")
	idx := strings.LastIndex(path, ".")
	if idx < 0 {
		return ""
	}
	return path[idx+1:]
}

// buildTraceRPCDefinition fills the definition struct from the rpc node's
// metadata JSON and does the best-effort line+docstring read of the proto
// source file.
func buildTraceRPCDefinition(node *store.Node, projectRoot string) (traceRPCDefinition, error) {
	path := strings.TrimPrefix(node.ID, "sym:")
	parts := strings.Split(path, ".")
	if len(parts) < 2 {
		return traceRPCDefinition{}, fmt.Errorf("trace_rpc: rpc symbol id %q has too few segments (need at least Service.Method)", node.ID)
	}
	method := parts[len(parts)-1]
	service := parts[len(parts)-2]
	pkg := ""
	if len(parts) > 2 {
		pkg = strings.Join(parts[:len(parts)-2], ".")
	}

	def := traceRPCDefinition{
		Service:        service,
		Method:         method,
		Package:        pkg,
		FullyQualified: path,
		SymbolID:       node.ID,
		ProtoFile:      node.SourcePath,
	}

	// Metadata JSON — shape emitted by internal/indexer/handlers/code/proto.go's
	// protoRPCMetadata. Unknown / malformed metadata is treated as "nothing to
	// add" rather than a fatal error so the tool still returns the shape that
	// can be derived from the ID alone.
	var meta struct {
		InputType       string `json:"input_type"`
		OutputType      string `json:"output_type"`
		ClientStreaming bool   `json:"client_streaming"`
		ServerStreaming bool   `json:"server_streaming"`
	}
	if node.Metadata != "" {
		_ = json.Unmarshal([]byte(node.Metadata), &meta)
	}
	def.InputType = meta.InputType
	def.OutputType = meta.OutputType
	def.ClientStreaming = meta.ClientStreaming
	def.ServerStreaming = meta.ServerStreaming

	// Best-effort line + docstring. Falls back to 0 / "" on any read error —
	// the graph doesn't persist proto line numbers, so reading the source file
	// is the only way to populate these fields without modifying the grammar.
	if node.SourcePath != "" {
		abs := resolveTraceRPCPath(projectRoot, node.SourcePath)
		if line, doc := scanTraceRPCDefinitionSite(abs, method); line > 0 {
			def.LineNumber = line
			def.Docstring = doc
		}
	}

	return def, nil
}

// buildTraceRPCImplementation projects a generated-code symbol node into the
// response shape, including a best-effort Language + Kind classification and
// a best-effort LineNumber via file scan for the method name.
func buildTraceRPCImplementation(node *store.Node, service, projectRoot string) traceRPCImplementation {
	path := strings.TrimPrefix(node.ID, "sym:")
	impl := traceRPCImplementation{
		Language:   traceRPCLanguageFromSourcePath(node.SourcePath),
		SymbolPath: path,
		SymbolID:   node.ID,
		File:       node.SourcePath,
		Kind:       traceRPCImplKind(path, service),
	}
	if node.SourcePath != "" {
		method := path
		if idx := strings.LastIndex(path, "."); idx >= 0 {
			method = path[idx+1:]
		}
		impl.LineNumber = scanTraceRPCMethodLine(resolveTraceRPCPath(projectRoot, node.SourcePath), method)
	}
	return impl
}

// traceRPCLanguageFromSourcePath maps a SourcePath's extension to a language
// label. Covers the four generated-code languages today's implements_rpc
// resolver cross-links (Go, Dart, TS, JS). Unknown extensions → "".
func traceRPCLanguageFromSourcePath(path string) string {
	if path == "" {
		return ""
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		return "go"
	case ".dart":
		return "dart"
	case ".ts", ".tsx":
		return "ts"
	case ".js", ".jsx", ".mjs", ".cjs":
		return "js"
	case ".proto":
		return "proto"
	case ".java":
		return "java"
	case ".kt", ".kts":
		return "kotlin"
	case ".py":
		return "python"
	case ".swift":
		return "swift"
	}
	return ""
}

// traceRPCImplKind classifies a generated-code symbol path as server, client,
// stub, or base — matches the three naming families the implements_rpc
// resolver derives. Empty string when the heuristic doesn't recognise it.
func traceRPCImplKind(path, service string) string {
	if service == "" {
		return ""
	}
	parts := strings.Split(path, ".")
	if len(parts) < 2 {
		return ""
	}
	container := parts[len(parts)-2]
	switch {
	case container == "Unimplemented"+service+"Server":
		return "stub"
	case container == service+"Server":
		return "server"
	case container == service+"Client":
		return "client"
	case container == service+"Base":
		return "base"
	case container == service:
		// ts Svc.methodName derivation — the service-named class doubles as
		// an implementation surface.
		return "service"
	}
	return ""
}

// findTraceRPCRelated returns the other rpc nodes on the same service as
// rpcPath (everything sharing the "<pkg>.<svc>." prefix, excluding rpcPath
// itself). Sorted by method name for stable output.
func findTraceRPCRelated(rpcPath string, candidates []store.Node) []traceRPCRelated {
	idx := strings.LastIndex(rpcPath, ".")
	if idx < 0 {
		return nil
	}
	servicePrefix := rpcPath[:idx+1] // includes trailing dot
	var related []traceRPCRelated
	for i := range candidates {
		path := strings.TrimPrefix(candidates[i].ID, "sym:")
		if path == rpcPath {
			continue
		}
		if !strings.HasPrefix(path, servicePrefix) {
			continue
		}
		// Reject deeper nested paths (e.g. a hypothetical future grammar that
		// emits nested rpc children) — sibling methods have exactly one
		// additional segment.
		if strings.Contains(path[len(servicePrefix):], ".") {
			continue
		}
		var meta struct {
			InputType       string `json:"input_type"`
			OutputType      string `json:"output_type"`
			ClientStreaming bool   `json:"client_streaming"`
			ServerStreaming bool   `json:"server_streaming"`
		}
		if candidates[i].Metadata != "" {
			_ = json.Unmarshal([]byte(candidates[i].Metadata), &meta)
		}
		related = append(related, traceRPCRelated{
			Method:          path[len(servicePrefix):],
			SymbolID:        candidates[i].ID,
			InputType:       meta.InputType,
			OutputType:      meta.OutputType,
			ClientStreaming: meta.ClientStreaming,
			ServerStreaming: meta.ServerStreaming,
		})
	}
	sort.Slice(related, func(i, j int) bool { return related[i].Method < related[j].Method })
	return related
}

// resolveTraceRPCMessage looks up the message declaration for a rpc input /
// output type and projects its fields + nested messages into the response
// shape. Recursion is bounded by traceRPCMaxNestedDepth so a self-referential
// proto (or indexer bug that produces a cycle) can't blow the stack.
//
// typeRef may be bare ("LoginRequest"), package-qualified ("auth.LoginRequest"),
// or fully-qualified with a leading dot (".auth.LoginRequest"). The resolver
// prefers an exact match on the package the rpc itself lives in; when the
// typeRef is absolute it takes precedence.
func resolveTraceRPCMessage(st *store.Store, rpcPackage, typeRef string) traceRPCMessage {
	fq := resolveTraceRPCTypeFQ(rpcPackage, typeRef)
	return resolveTraceRPCMessageFQ(st, fq, typeRef, 0)
}

// resolveTraceRPCMessageFQ is the recursion-carrying implementation of
// resolveTraceRPCMessage. depth counts the recursion level: the top-level
// (input/output) message is depth 0; its nested messages are depth 1; etc.
// At traceRPCMaxNestedDepth, recursion stops and the Note field documents
// the cap so the caller isn't mislead about completeness.
//
// Known edge case (tracked, not a bug worth blocking on): proto2 `group`
// declarations inline a message AND a field with the same path prefix. The
// proto grammar emits the group as a message Unit plus a field Unit with
// the group's field_number. In our graph, that means the group's name
// appears as both a traceRPCField on the parent (with field_number set) AND
// can be projected as a nested message (if it has its own fields). Proto2
// groups are legacy and rare; downstream consumers can detect the
// duplication by matching Field.Name against NestedMessage.TypeName. If
// this becomes load-bearing for any caller, bead a follow-up to dedupe.
func resolveTraceRPCMessageFQ(st *store.Store, fq, displayName string, depth int) traceRPCMessage {
	msg := traceRPCMessage{
		TypeName:       displayName,
		FullyQualified: fq,
		Fields:         []traceRPCField{},
	}
	if fq == "" {
		msg.Note = "trace_rpc: unable to derive fully-qualified type name"
		return msg
	}
	node, err := st.GetNode(store.SymbolNodeID(fq))
	if err != nil || node == nil {
		msg.Note = fmt.Sprintf("trace_rpc: message %q not indexed (no symbol node at sym:%s)", displayName, fq)
		return msg
	}
	msg.Resolved = true
	msg.SymbolID = node.ID
	msg.ProtoFile = node.SourcePath

	// Using the metadata marker ("field_number") instead of an id-prefix scan
	// because store.FindNodes does substring matches on id/label/source_path
	// (not prefix-only), so a narrow prefix query would false-negative on
	// nodes whose label is the bare field name. Matching on the metadata
	// marker is O(field-count) across the whole project but cheap: a single
	// SQLite LIKE scan is faster than re-reading every proto file.
	fieldPrefix := "sym:" + fq + "."
	fieldNodes, err := st.ListNodesByKindWithMetadataContaining(store.NodeKindSymbol, traceRPCFieldMetadataMarker)
	if err != nil {
		msg.Note = fmt.Sprintf("trace_rpc: list fields: %v", err)
		return msg
	}

	// oneofNames collects the last-segment names of oneof containers
	// ("payload" in sym:pkg.Msg.payload) inferred from fields carrying
	// Metadata.oneof. Used below to skip those paths when scanning for
	// nested messages so we don't duplicate the oneof as a nested message.
	oneofNames := map[string]struct{}{}

	for _, fn := range fieldNodes {
		if !strings.HasPrefix(fn.ID, fieldPrefix) {
			continue
		}
		rest := strings.TrimPrefix(fn.ID, fieldPrefix)
		if rest == "" {
			continue
		}
		var fieldMeta struct {
			FieldNumber int    `json:"field_number"`
			OneOf       string `json:"oneof"`
		}
		if fn.Metadata != "" {
			_ = json.Unmarshal([]byte(fn.Metadata), &fieldMeta)
		}
		// Distinguish four cases at this point (rest always contains at
		// least one segment because we trimmed "sym:<fq>." above):
		//
		//   rest="user"                  (0 dots) → direct field of THIS message
		//   rest="payload.text"          (1 dot + OneOf set) → oneof member
		//   rest="Nested.inner"          (1 dot + OneOf empty) → NESTED MSG'S field, skip
		//   rest="Nested.Deeper.field"   (2+ dots) → nested-of-nested, skip
		//
		// The reviewer's M4 regression: previously we accepted any rest with
		// ≤1 dot, leaking nested-message fields as if they belonged to this
		// message. The fix (require OneOf for the 1-dot case) relies on the
		// proto grammar's invariant that oneof members are the only fields
		// whose Unit.Path is one level deeper than their enclosing message.
		if strings.Contains(rest, ".") {
			if fieldMeta.OneOf == "" {
				// Belongs to a nested message, not this one.
				continue
			}
			// Remember the oneof-container name so nested-message discovery
			// below doesn't list it as its own nested message.
			first := rest[:strings.Index(rest, ".")]
			oneofNames[first] = struct{}{}
			// A oneof member with two dots (rest="payload.deep.x") would be
			// a nested-of-nested — skip. Proto disallows this in practice
			// but the guard keeps the code honest if a future grammar bug
			// produces such a path.
			if strings.Count(rest, ".") > 1 {
				continue
			}
		}
		msg.Fields = append(msg.Fields, traceRPCField{
			Name:        fn.Label,
			FieldNumber: fieldMeta.FieldNumber,
			OneOf:       fieldMeta.OneOf,
		})
	}
	sort.Slice(msg.Fields, func(i, j int) bool {
		if msg.Fields[i].FieldNumber != msg.Fields[j].FieldNumber {
			return msg.Fields[i].FieldNumber < msg.Fields[j].FieldNumber
		}
		return msg.Fields[i].Name < msg.Fields[j].Name
	})

	// Nested messages: recurse into direct-child messages unless we've hit
	// the depth cap. A "direct-child message" is any symbol whose id starts
	// with sym:<fq>.<segment> (no further dots) AND which itself has
	// nested-field descendants (i.e. at least one field_number-carrying
	// symbol rooted under it). That descendant-existence test doubles as a
	// kind filter: plain enums and oneofs won't match because only messages
	// contain field declarations.
	//
	// Cap handling has two cases:
	//   depth+1 < cap   → recurse freely (children get fully populated).
	//   depth+1 >= cap  → discover potential nested names without recursing.
	//                     NestedMessages stays empty; the unresolved names
	//                     are listed in msg.Note so consumers can see what
	//                     was skipped and re-query explicitly if needed.
	potential := findTraceRPCPotentialNestedNames(fq, fieldNodes, oneofNames)
	if len(potential) > 0 {
		if depth+1 < traceRPCMaxNestedDepth {
			msg.NestedMessages = make([]traceRPCMessage, 0, len(potential))
			for _, name := range potential {
				msg.NestedMessages = append(msg.NestedMessages,
					resolveTraceRPCMessageFQ(st, fq+"."+name, name, depth+1))
			}
		} else {
			// At or past the cap — surface the fact that nested messages
			// exist here without recursing. Give the caller enough info to
			// re-query explicitly if they want to descend further.
			msg.Note = fmt.Sprintf("trace_rpc: nested recursion capped at depth %d; deeper messages (%s) not expanded", traceRPCMaxNestedDepth, strings.Join(potential, ", "))
		}
	}
	return msg
}

// findTraceRPCPotentialNestedNames identifies direct-child nested-message
// names under parentFQ by looking for field_number-carrying descendants
// exactly two levels deeper. Oneof containers are excluded. Returns a
// deterministic sorted slice. Kept separate from findTraceRPCNestedMessages
// so the "discover vs recurse" decision can happen once and cap-handling
// can short-circuit without re-walking fieldNodes.
func findTraceRPCPotentialNestedNames(parentFQ string, fieldNodes []store.Node, oneofNames map[string]struct{}) []string {
	nested := map[string]struct{}{}
	prefix := "sym:" + parentFQ + "."
	for _, fn := range fieldNodes {
		if !strings.HasPrefix(fn.ID, prefix) {
			continue
		}
		rest := strings.TrimPrefix(fn.ID, prefix)
		dot := strings.Index(rest, ".")
		if dot < 0 {
			continue
		}
		first := rest[:dot]
		if _, isOneof := oneofNames[first]; isOneof {
			continue
		}
		nested[first] = struct{}{}
	}
	if len(nested) == 0 {
		return nil
	}
	out := make([]string, 0, len(nested))
	for n := range nested {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// resolveTraceRPCTypeFQ computes the fully-qualified dotted path for a proto
// typeRef as it appears in an rpc declaration. Proto spec rules:
//
//   - Leading dot (".auth.LoginRequest") is absolute — strip the dot.
//   - Dotted name ("auth.LoginRequest") is treated as already qualified.
//   - Bare name ("LoginRequest") is relative to the rpc's proto package.
func resolveTraceRPCTypeFQ(rpcPackage, typeRef string) string {
	typeRef = strings.TrimSpace(typeRef)
	if typeRef == "" {
		return ""
	}
	if strings.HasPrefix(typeRef, ".") {
		return strings.TrimPrefix(typeRef, ".")
	}
	if strings.Contains(typeRef, ".") {
		return typeRef
	}
	if rpcPackage == "" {
		return typeRef
	}
	return rpcPackage + "." + typeRef
}

// resolveTraceRPCPath joins projectRoot with a workspace-relative SourcePath.
// Absolute SourcePaths (ever) are returned verbatim. Empty projectRoot →
// filepath.Abs, matching get_document.go's CWD-relative resolution.
func resolveTraceRPCPath(projectRoot, sourcePath string) string {
	if filepath.IsAbs(sourcePath) {
		return sourcePath
	}
	if projectRoot != "" {
		return filepath.Join(projectRoot, sourcePath)
	}
	abs, err := filepath.Abs(sourcePath)
	if err != nil {
		return sourcePath
	}
	return abs
}

// scanTraceRPCDefinitionSite does a best-effort line-based scan of a proto
// source file for an `rpc <method>` declaration. Returns the 1-indexed line
// number and, if present, a contiguous block of leading `//` comment lines
// stripped of their markers and joined with spaces. Returns (0, "") when the
// file can't be read or the declaration isn't found.
//
// The proto grammar does NOT emit docstrings (DocstringFromNode returns ""),
// so a graph-only approach would leave the docstring field permanently empty.
// Scanning the source at query time keeps the field useful without touching
// the grammar.
func scanTraceRPCDefinitionSite(absPath, method string) (int, string) {
	data, err := os.ReadFile(absPath)
	if err != nil {
		return 0, ""
	}
	lines := strings.Split(string(data), "\n")
	prefix := "rpc " + method
	for i, raw := range lines {
		trimmed := strings.TrimSpace(raw)
		if !strings.HasPrefix(trimmed, prefix) {
			continue
		}
		// Guard: "rpc LoginExtra" would start with "rpc Login" too — require the
		// character after the method name to be whitespace or an opening paren.
		rest := trimmed[len(prefix):]
		if rest != "" {
			c := rest[0]
			if c != ' ' && c != '\t' && c != '(' {
				continue
			}
		}
		return i + 1, collectTraceRPCLeadingComments(lines, i)
	}
	return 0, ""
}

// scanTraceRPCMethodLine does a best-effort line-based scan of a source file
// for a whole-word occurrence of methodName followed by an opening paren
// ("methodName(" possibly separated by whitespace). Returns the 1-indexed
// line number of the first match or 0 on any failure / miss.
//
// Heuristic vs surgical: we don't try to distinguish the declaration line
// from a call site. In generated gRPC code the declaration is invariably
// at / near the top of a class body and a pre-declaration call would be a
// compiler error, so the first `methodName(` is overwhelmingly the
// declaration. A regression that points at a call line is still useful
// context for the caller. Same no-touch-grammar rationale as
// scanTraceRPCDefinitionSite: graph_nodes doesn't persist Loc.Line, and the
// file scan keeps the feature working without grammar mutations (tracked as
// lib-cta follow-up).
func scanTraceRPCMethodLine(absPath, methodName string) int {
	if absPath == "" || methodName == "" {
		return 0
	}
	data, err := os.ReadFile(absPath)
	if err != nil {
		return 0
	}
	for i, raw := range strings.Split(string(data), "\n") {
		idx := 0
		for idx < len(raw) {
			hit := strings.Index(raw[idx:], methodName)
			if hit < 0 {
				break
			}
			start := idx + hit
			end := start + len(methodName)
			// Whole-word boundary check: the character before must not be
			// an identifier rune (so we don't match `MyMethodName` when
			// searching for `MethodName`), and the character after must be
			// `(` (optionally preceded by whitespace).
			if start > 0 && isTraceRPCIdentByte(raw[start-1]) {
				idx = end
				continue
			}
			rest := raw[end:]
			for len(rest) > 0 && (rest[0] == ' ' || rest[0] == '\t') {
				rest = rest[1:]
			}
			if len(rest) > 0 && rest[0] == '(' {
				return i + 1
			}
			idx = end
		}
	}
	return 0
}

// isTraceRPCIdentByte reports whether b is a byte that can appear in an
// identifier — used for whole-word boundary checks in scanTraceRPCMethodLine.
// Unicode identifiers are approximated by treating any byte ≥ 0x80 as part
// of an identifier; the proto-rpc names we scan for are ASCII in practice
// (generated code) so the approximation is safe.
func isTraceRPCIdentByte(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z':
		return true
	case b >= 'A' && b <= 'Z':
		return true
	case b >= '0' && b <= '9':
		return true
	case b == '_' || b == '$':
		return true
	case b >= 0x80:
		return true
	}
	return false
}

// collectTraceRPCLeadingComments walks back from the rpc declaration line
// collecting contiguous // comment lines. Returns them joined with single
// spaces, with leading "// " stripped. Blank lines or non-comment lines
// terminate the block. Used only for the best-effort docstring field.
func collectTraceRPCLeadingComments(lines []string, rpcLineIdx int) string {
	var block []string
	for i := rpcLineIdx - 1; i >= 0; i-- {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "" {
			break
		}
		if !strings.HasPrefix(trimmed, "//") {
			break
		}
		cleaned := strings.TrimSpace(strings.TrimPrefix(trimmed, "//"))
		block = append([]string{cleaned}, block...)
	}
	return strings.Join(block, " ")
}

// renderTraceRPCMarkdown produces the TTY-friendly markdown view of a
// traceRPCResult. Format is capped at well under 200 lines even for services
// with many rpcs — only the definition, implementations, input/output fields,
// callers, and sibling rpcs show up; nested messages and complex type
// resolution live in the JSON mode.
func renderTraceRPCMarkdown(r *traceRPCResult) string {
	var b strings.Builder
	def := r.Definition
	fqTitle := def.FullyQualified
	if fqTitle == "" {
		fqTitle = def.Method
	}
	fmt.Fprintf(&b, "# trace_rpc: %s\n\n", fqTitle)

	// Definition block
	b.WriteString("## Definition\n\n")
	fmt.Fprintf(&b, "- **Service:** %s\n", def.Service)
	fmt.Fprintf(&b, "- **Method:** %s\n", def.Method)
	if def.Package != "" {
		fmt.Fprintf(&b, "- **Package:** %s\n", def.Package)
	}
	fmt.Fprintf(&b, "- **Symbol:** `%s`\n", def.SymbolID)
	if def.InputType != "" || def.OutputType != "" {
		fmt.Fprintf(&b, "- **Signature:** `%s(%s%s) returns (%s%s)`\n",
			def.Method,
			streamingToken(def.ClientStreaming),
			def.InputType,
			streamingToken(def.ServerStreaming),
			def.OutputType)
	}
	if def.ProtoFile != "" {
		if def.LineNumber > 0 {
			fmt.Fprintf(&b, "- **Source:** %s:%d\n", def.ProtoFile, def.LineNumber)
		} else {
			fmt.Fprintf(&b, "- **Source:** %s\n", def.ProtoFile)
		}
	}
	if def.Docstring != "" {
		fmt.Fprintf(&b, "- **Docstring:** %s\n", def.Docstring)
	}
	b.WriteString("\n")

	// Implementations
	b.WriteString("## Implementations\n\n")
	if len(r.Implementations) == 0 {
		b.WriteString("_No generated-code implementations linked via `implements_rpc` edges._\n\n")
	} else {
		for _, impl := range r.Implementations {
			lang := impl.Language
			if lang == "" {
				lang = "?"
			}
			kind := impl.Kind
			if kind == "" {
				kind = "?"
			}
			fmt.Fprintf(&b, "- `%s` — **%s** (%s)", impl.SymbolPath, lang, kind)
			if impl.File != "" {
				if impl.LineNumber > 0 {
					fmt.Fprintf(&b, " — %s:%d", impl.File, impl.LineNumber)
				} else {
					fmt.Fprintf(&b, " — %s", impl.File)
				}
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	// Input message
	b.WriteString("## Input message\n\n")
	renderTraceRPCMessageBlock(&b, r.InputMessage)

	// Output message
	b.WriteString("## Output message\n\n")
	renderTraceRPCMessageBlock(&b, r.OutputMessage)

	// Related rpcs
	b.WriteString("## Related RPCs (same service)\n\n")
	if len(r.RelatedRPCs) == 0 {
		b.WriteString("_No other rpcs on this service._\n\n")
	} else {
		for _, rel := range r.RelatedRPCs {
			fmt.Fprintf(&b, "- `%s` — `%s(%s%s) returns (%s%s)`\n",
				rel.Method,
				rel.Method,
				streamingToken(rel.ClientStreaming),
				rel.InputType,
				streamingToken(rel.ServerStreaming),
				rel.OutputType)
		}
		b.WriteString("\n")
	}

	// Callers
	b.WriteString("## Callers\n\n")
	if r.CallersNote != "" {
		fmt.Fprintf(&b, "_%s_\n\n", r.CallersNote)
	}
	if len(r.Callers) == 0 && r.CallersNote == "" {
		b.WriteString("_None._\n\n")
	}
	for _, c := range r.Callers {
		fmt.Fprintf(&b, "- `%s`", c.SymbolPath)
		if c.Language != "" {
			fmt.Fprintf(&b, " (%s)", c.Language)
		}
		if c.File != "" {
			if c.LineNumber > 0 {
				fmt.Fprintf(&b, " — %s:%d", c.File, c.LineNumber)
			} else {
				fmt.Fprintf(&b, " — %s", c.File)
			}
		}
		if c.Depth > 1 {
			fmt.Fprintf(&b, " (hop %d)", c.Depth)
		}
		b.WriteString("\n")
	}

	// Warnings (if any): surface non-fatal glitches so the reader isn't
	// misled by a silently-incomplete trace.
	if len(r.Warnings) > 0 {
		b.WriteString("\n## Warnings\n\n")
		for _, w := range r.Warnings {
			fmt.Fprintf(&b, "- %s\n", w)
		}
	}

	return b.String()
}

func renderTraceRPCMessageBlock(b *strings.Builder, m *traceRPCMessage) {
	if m == nil {
		b.WriteString("_No type indicated on the rpc declaration._\n\n")
		return
	}
	renderTraceRPCMessageAtDepth(b, m, 0)
}

// renderTraceRPCMessageAtDepth renders a message at the given nesting depth,
// prefixing each line with indent. Nested messages recurse with depth+1;
// indentation keeps the markdown readable without exploding past the 200-line
// cap (one nested message is typically a handful of extra lines).
func renderTraceRPCMessageAtDepth(b *strings.Builder, m *traceRPCMessage, depth int) {
	indent := strings.Repeat("  ", depth)
	fmt.Fprintf(b, "%s**Type:** `%s`", indent, m.TypeName)
	if m.FullyQualified != "" && m.FullyQualified != m.TypeName {
		fmt.Fprintf(b, " (`%s`)", m.FullyQualified)
	}
	b.WriteString("\n")
	if !m.Resolved {
		if m.Note != "" {
			fmt.Fprintf(b, "%s_%s_\n\n", indent, m.Note)
		} else {
			fmt.Fprintf(b, "%s_Message not resolved._\n\n", indent)
		}
		return
	}
	if len(m.Fields) == 0 {
		fmt.Fprintf(b, "%s_No fields indexed for this message._\n", indent)
	}
	for _, f := range m.Fields {
		line := fmt.Sprintf("%s- `%s`", indent, f.Name)
		if f.FieldNumber > 0 {
			line += fmt.Sprintf(" = %d", f.FieldNumber)
		}
		if f.OneOf != "" {
			line += fmt.Sprintf(" (oneof %s)", f.OneOf)
		}
		b.WriteString(line + "\n")
	}
	for i := range m.NestedMessages {
		fmt.Fprintf(b, "%s- **nested:**\n", indent)
		renderTraceRPCMessageAtDepth(b, &m.NestedMessages[i], depth+1)
	}
	if m.Note != "" {
		// Resolved but notable — e.g. recursion cap notice.
		fmt.Fprintf(b, "%s_%s_\n", indent, m.Note)
	}
	if depth == 0 {
		b.WriteString("\n")
	}
}

// streamingToken returns "stream " when streaming is set, empty otherwise —
// used to assemble rpc signature strings.
func streamingToken(streaming bool) string {
	if streaming {
		return "stream "
	}
	return ""
}
