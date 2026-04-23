package report_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"librarian/internal/analytics"
	"librarian/internal/report"
	"librarian/internal/store"
)

func sampleInput() *report.Input {
	nodes := []store.Node{
		{ID: "doc:a1", Kind: store.NodeKindDocument, Label: "Alpha 1"},
		{ID: "doc:a2", Kind: store.NodeKindDocument, Label: "Alpha 2"},
		{ID: "doc:b1", Kind: store.NodeKindDocument, Label: "Beta 1"},
		{ID: "doc:b2", Kind: store.NodeKindDocument, Label: "Beta 2"},
	}
	edges := []store.Edge{
		{From: "doc:a1", To: "doc:a2", Kind: "mentions", Weight: 1},
		{From: "doc:b1", To: "doc:b2", Kind: "mentions", Weight: 1},
		{From: "doc:a1", To: "doc:b1", Kind: "shared_code_ref", Weight: 1},
	}
	ar := &analytics.Report{
		TotalNodes: 4,
		TotalEdges: 3,
		Communities: []analytics.Community{
			{ID: 0, Label: "a1", Nodes: []string{"doc:a1", "doc:a2"}},
			{ID: 1, Label: "b1", Nodes: []string{"doc:b1", "doc:b2"}},
		},
		GodNodes: []analytics.GodNode{
			{NodeID: "doc:a1", Label: "Alpha 1", Kind: "document", Degree: 2, CommunityID: 0},
			{NodeID: "doc:b1", Label: "Beta 1", Kind: "document", Degree: 2, CommunityID: 1},
		},
		SurprisingConnections: []analytics.SurprisingConnection{
			{From: "doc:a1", To: "doc:b1", Kind: "shared_code_ref",
				FromCommunityID: 0, ToCommunityID: 1, Score: 0.5},
		},
		SuggestedQuestions: []analytics.SuggestedQuestion{
			{Text: "What connects Alpha 1 to Beta 1?", Template: "What connects %s to %s?",
				Args: []string{"Alpha 1", "Beta 1"}, Kind: analytics.QuestionKindGodPair},
		},
	}
	return &report.Input{
		Analytics:   ar,
		Nodes:       nodes,
		Edges:       edges,
		GeneratedAt: time.Date(2026, 4, 23, 17, 30, 0, 0, time.UTC),
	}
}

func TestRenderMarkdown_StructureAndContent(t *testing.T) {
	out := string(report.RenderMarkdown(sampleInput()))

	// Every major section heading is present.
	for _, section := range []string{
		"# Graph Report",
		"## God Nodes",
		"## Communities",
		"## Surprising Connections",
		"## Suggested Questions",
	} {
		if !strings.Contains(out, section) {
			t.Errorf("missing section %q in markdown output", section)
		}
	}

	// Generated line uses the injected timestamp (no time.Now leakage).
	if !strings.Contains(out, "2026-04-23 17:30 UTC") {
		t.Errorf("missing injected GeneratedAt timestamp:\n%s", out)
	}

	// Totals line reflects the Report.
	if !strings.Contains(out, "**Nodes:** 4") || !strings.Contains(out, "**Edges:** 3") {
		t.Errorf("totals missing or wrong:\n%s", out)
	}

	// God-node labels appear in the table.
	if !strings.Contains(out, "Alpha 1") || !strings.Contains(out, "Beta 1") {
		t.Errorf("god-node labels missing:\n%s", out)
	}

	// Suggested question rendered verbatim.
	if !strings.Contains(out, "What connects Alpha 1 to Beta 1?") {
		t.Errorf("suggested question missing:\n%s", out)
	}
}

func TestRenderMarkdown_EmptyReport(t *testing.T) {
	in := &report.Input{
		Analytics:   &analytics.Report{},
		Nodes:       nil,
		Edges:       nil,
		GeneratedAt: time.Date(2026, 4, 23, 17, 30, 0, 0, time.UTC),
	}
	out := string(report.RenderMarkdown(in))
	// Must not panic and must at least emit the header.
	if !strings.HasPrefix(out, "# Graph Report") {
		t.Errorf("empty report didn't produce expected header:\n%s", out)
	}
	// "No god nodes" / "No communities" fallbacks must kick in.
	for _, expect := range []string{
		"No high-degree nodes",
		"No communities detected",
		"No cross-community edges",
	} {
		if !strings.Contains(out, expect) {
			t.Errorf("empty-report fallback missing: %q\n%s", expect, out)
		}
	}
}

func TestRenderMarkdown_Deterministic(t *testing.T) {
	in := sampleInput()
	a := report.RenderMarkdown(in)
	b := report.RenderMarkdown(in)
	if !bytes.Equal(a, b) {
		t.Errorf("RenderMarkdown produced different output on identical input:\n--- first ---\n%s\n--- second ---\n%s", a, b)
	}
}

func TestRenderJSON_Parseable(t *testing.T) {
	out := report.RenderJSON(sampleInput())

	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("RenderJSON output isn't valid JSON: %v", err)
	}

	// Schema version is present and matches the package constant.
	sv, _ := got["schema_version"].(float64)
	if int(sv) != report.SchemaVersion {
		t.Errorf("schema_version = %v, want %d", sv, report.SchemaVersion)
	}

	totals, ok := got["totals"].(map[string]any)
	if !ok {
		t.Fatalf("missing 'totals' object:\n%s", out)
	}
	if totals["nodes"].(float64) != 4 || totals["edges"].(float64) != 3 {
		t.Errorf("totals wrong: %v", totals)
	}

	nodes, ok := got["nodes"].([]any)
	if !ok || len(nodes) != 4 {
		t.Errorf("expected 4 nodes, got %v", nodes)
	}

	edges, ok := got["edges"].([]any)
	if !ok || len(edges) != 3 {
		t.Errorf("expected 3 edges, got %v", edges)
	}

	// At least one edge is flagged cross_community.
	crossSeen := false
	for _, e := range edges {
		m := e.(map[string]any)
		if cross, _ := m["cross_community"].(bool); cross {
			crossSeen = true
			break
		}
	}
	if !crossSeen {
		t.Errorf("expected at least one cross_community edge")
	}

	// Community members are enriched with id + label + kind (A2).
	analyticsObj, _ := got["analytics"].(map[string]any)
	comms, _ := analyticsObj["communities"].([]any)
	if len(comms) == 0 {
		t.Fatal("expected at least one community in analytics.communities")
	}
	first, _ := comms[0].(map[string]any)
	members, _ := first["nodes"].([]any)
	if len(members) == 0 {
		t.Fatal("expected community to have member nodes")
	}
	m0, _ := members[0].(map[string]any)
	if _, ok := m0["id"]; !ok {
		t.Errorf("community member missing 'id' field: %+v", m0)
	}
	if _, ok := m0["label"]; !ok {
		t.Errorf("community member missing 'label' field: %+v", m0)
	}
	if _, ok := m0["kind"]; !ok {
		t.Errorf("community member missing 'kind' field: %+v", m0)
	}
}

func TestRenderJSON_Deterministic(t *testing.T) {
	in := sampleInput()
	a := report.RenderJSON(in)
	b := report.RenderJSON(in)
	if !bytes.Equal(a, b) {
		t.Errorf("RenderJSON produced different output on identical input")
	}
}

func TestRenderHTML_ContainsKeyPieces(t *testing.T) {
	out := string(report.RenderHTML(sampleInput()))

	// DOCTYPE + self-contained style.
	if !strings.HasPrefix(out, "<!DOCTYPE html>") {
		t.Errorf("output is not a standalone HTML document")
	}
	if !strings.Contains(out, "<style>") || !strings.Contains(out, "<script>") {
		t.Errorf("expected inline style + script; output isn't self-contained")
	}
	// No external script/stylesheet references — truly offline-capable.
	if strings.Contains(out, "<script src=\"http") || strings.Contains(out, "rel=\"stylesheet\"") {
		t.Errorf("output references external resources; not truly self-contained")
	}

	// One SVG circle per node, one line per non-self-loop edge.
	circles := strings.Count(out, "<circle")
	if circles != 4 {
		t.Errorf("expected 4 <circle> nodes, got %d", circles)
	}
	lines := strings.Count(out, "<line")
	if lines != 3 {
		t.Errorf("expected 3 <line> edges, got %d", lines)
	}

	// Timestamp injected properly.
	if !strings.Contains(out, "2026-04-23 17:30 UTC") {
		t.Errorf("timestamp missing from HTML")
	}
}

func TestRenderHTML_Deterministic(t *testing.T) {
	in := sampleInput()
	a := report.RenderHTML(in)
	b := report.RenderHTML(in)
	if !bytes.Equal(a, b) {
		t.Errorf("RenderHTML produced different output on identical input")
	}
}

func TestRenderHTML_EscapesSpecialChars(t *testing.T) {
	// A label containing HTML-dangerous characters should be escaped.
	nodes := []store.Node{
		{ID: "doc:x", Kind: "document", Label: "<script>alert(1)</script>"},
	}
	in := &report.Input{
		Analytics:   &analytics.Report{TotalNodes: 1},
		Nodes:       nodes,
		Edges:       nil,
		GeneratedAt: time.Date(2026, 4, 23, 17, 30, 0, 0, time.UTC),
	}
	out := string(report.RenderHTML(in))
	if strings.Contains(out, "<script>alert(1)</script>") {
		t.Error("label with raw <script> was not escaped — XSS risk")
	}
	if !strings.Contains(out, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Errorf("expected HTML-escaped label, not found in:\n%s", out)
	}
}
