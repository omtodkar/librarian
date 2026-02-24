package indexer

import (
	"strings"
	"testing"
)

func TestDetectDiagramType(t *testing.T) {
	tests := []struct {
		name     string
		lang     string
		content  string
		wantType DiagramType
		wantOk   bool
	}{
		{"mermaid lang", "mermaid", "", DiagramMermaid, true},
		{"mermaid uppercase", "Mermaid", "", DiagramMermaid, true},
		{"plantuml lang", "plantuml", "", DiagramPlantUML, true},
		{"puml lang", "puml", "", DiagramPlantUML, true},
		{"ascii lang", "ascii", "", DiagramASCII, true},
		{"ascii-art lang", "ascii-art", "", DiagramASCII, true},
		{"go code - not diagram", "go", "", "", false},
		{"python code - not diagram", "python", "", "", false},
		{"empty lang with ascii content", "", "+--+--+\n|  |  |\n+--+--+\n|  |  |\n+--+--+", DiagramASCII, true},
		{"empty lang with plain text", "", "just some regular text content here", "", false},
		{"text lang with ascii content", "text", "+--+--+\n|  |  |\n+--+--+\n|  |  |\n+--+--+", DiagramASCII, true},
		{"text lang with plain text", "txt", "just some regular text that is not a diagram at all", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotType, gotOk := detectDiagramType(tt.lang, tt.content)
			if gotOk != tt.wantOk {
				t.Errorf("detectDiagramType(%q, ...) ok = %v, want %v", tt.lang, gotOk, tt.wantOk)
			}
			if gotType != tt.wantType {
				t.Errorf("detectDiagramType(%q, ...) type = %q, want %q", tt.lang, gotType, tt.wantType)
			}
		})
	}
}

func TestIsASCIIDiagram(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{
			"too short",
			"+-+",
			false,
		},
		{
			"too few lines",
			"+----------+----------+",
			false,
		},
		{
			"valid box diagram",
			"+--------+    +--------+\n|  Auth  | -> |  User  |\n+--------+    +--------+\n|        |    |        |\n+--------+    +--------+",
			true,
		},
		{
			"unicode box drawing",
			"┌────────┐\n│ Server │\n├────────┤\n│ Client │\n└────────┘",
			true,
		},
		{
			"plain text - not diagram",
			"This is just some\nplain text that does\nnot look like a diagram",
			false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isASCIIDiagram(tt.content)
			if got != tt.want {
				t.Errorf("isASCIIDiagram() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestExtractMermaidLabels(t *testing.T) {
	t.Run("flowchart with node labels", func(t *testing.T) {
		content := `graph TD
    A[User Login] --> B{Authenticated?}
    B -->|Yes| C[Dashboard]
    B -->|No| D[Error Page]`

		labels := extractMermaidLabels(content)

		expected := []string{"User Login", "Authenticated?", "Dashboard", "Error Page", "Yes", "No"}
		for _, e := range expected {
			if !labels[e] {
				t.Errorf("expected label %q not found", e)
			}
		}
	})

	t.Run("filters short node IDs", func(t *testing.T) {
		content := `graph TD
    A[Start] --> B[End]`

		labels := extractMermaidLabels(content)

		if labels["A"] {
			t.Error("short node ID 'A' should be filtered out")
		}
		if labels["B"] {
			t.Error("short node ID 'B' should be filtered out")
		}
		if !labels["Start"] {
			t.Error("label 'Start' should be present")
		}
	})

	t.Run("sequence diagram with participants", func(t *testing.T) {
		content := `sequenceDiagram
    participant Alice
    participant "Bob Service"
    Alice->>Bob Service: Request`

		labels := extractMermaidLabels(content)

		if !labels["Alice"] {
			t.Error("expected participant 'Alice'")
		}
		if !labels["Bob Service"] {
			t.Error("expected participant 'Bob Service'")
		}
	})

	t.Run("title extraction", func(t *testing.T) {
		content := `graph TD
    title: Authentication Flow
    A[Login] --> B[Verify]`

		labels := extractMermaidLabels(content)

		if !labels["Authentication Flow"] {
			t.Error("expected title 'Authentication Flow'")
		}
	})

	t.Run("subgraph extraction", func(t *testing.T) {
		content := `graph TD
    subgraph Backend
        A[API] --> B[Database]
    end`

		labels := extractMermaidLabels(content)

		if !labels["Backend"] {
			t.Error("expected subgraph 'Backend'")
		}
	})
}

func TestExtractPlantUMLLabels(t *testing.T) {
	t.Run("sequence diagram", func(t *testing.T) {
		content := `@startuml
title Authentication Sequence
actor User
participant "Auth Service"
database UserDB

User -> Auth Service: Login Request
Auth Service -> UserDB: Validate Credentials
Auth Service --> User: Token
@enduml`

		labels := extractPlantUMLLabels(content)

		expected := []string{"Authentication Sequence", "User", "Auth Service", "UserDB"}
		for _, e := range expected {
			if !labels[e] {
				t.Errorf("expected label %q not found", e)
			}
		}
	})

	t.Run("arrow labels", func(t *testing.T) {
		content := `@startuml
A -> B: Send Request
B --> A: Return Response
@enduml`

		labels := extractPlantUMLLabels(content)

		if !labels["Send Request"] {
			t.Error("expected arrow label 'Send Request'")
		}
		if !labels["Return Response"] {
			t.Error("expected arrow label 'Return Response'")
		}
	})

	t.Run("class diagram", func(t *testing.T) {
		content := `@startuml
class UserService
interface Authenticator
enum Role
@enduml`

		labels := extractPlantUMLLabels(content)

		if !labels["UserService"] {
			t.Error("expected class 'UserService'")
		}
		if !labels["Authenticator"] {
			t.Error("expected interface 'Authenticator'")
		}
		if !labels["Role"] {
			t.Error("expected enum 'Role'")
		}
	})
}

func TestExtractASCIILabels(t *testing.T) {
	t.Run("box labels", func(t *testing.T) {
		content := `+----------+    +----------+
| Frontend | -> | Backend  |
+----------+    +----------+
                | Database |
                +----------+`

		labels := extractASCIILabels(content)

		if !labels["Frontend"] {
			t.Error("expected label 'Frontend'")
		}
		if !labels["Backend"] {
			t.Error("expected label 'Backend'")
		}
		if !labels["Database"] {
			t.Error("expected label 'Database'")
		}
	})

	t.Run("filters short labels", func(t *testing.T) {
		content := `+--+
|AB|
+--+`

		labels := extractASCIILabels(content)

		if labels["AB"] {
			t.Error("short label 'AB' should be filtered (< 3 chars)")
		}
	})
}

func TestDiagramSubtype(t *testing.T) {
	tests := []struct {
		name    string
		dt      DiagramType
		content string
		want    string
	}{
		{"mermaid graph", DiagramMermaid, "graph TD\n  A-->B", "flowchart"},
		{"mermaid flowchart", DiagramMermaid, "flowchart LR\n  A-->B", "flowchart"},
		{"mermaid sequence", DiagramMermaid, "sequenceDiagram\n  participant A", "sequence diagram"},
		{"mermaid gantt", DiagramMermaid, "gantt\n  title Plan", "gantt chart"},
		{"mermaid pie", DiagramMermaid, "pie\n  title Distribution", "pie chart"},
		{"mermaid unknown", DiagramMermaid, "somethingelse\n  A-->B", "diagram"},
		{"plantuml", DiagramPlantUML, "@startuml\n...\n@enduml", "diagram"},
		{"ascii", DiagramASCII, "+--+\n|  |\n+--+", "diagram"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := diagramSubtype(tt.dt, tt.content)
			if got != tt.want {
				t.Errorf("diagramSubtype() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatLabels(t *testing.T) {
	t.Run("no labels", func(t *testing.T) {
		result := formatLabels(DiagramMermaid, "flowchart", map[string]bool{})
		want := "[Diagram: mermaid flowchart]"
		if result != want {
			t.Errorf("formatLabels() = %q, want %q", result, want)
		}
	})

	t.Run("with labels", func(t *testing.T) {
		labels := map[string]bool{"Login": true, "Dashboard": true}
		result := formatLabels(DiagramMermaid, "flowchart", labels)

		if !strings.HasPrefix(result, "[Diagram: mermaid flowchart — ") {
			t.Errorf("expected prefix '[Diagram: mermaid flowchart — ', got %q", result)
		}
		if !strings.Contains(result, "Login") {
			t.Error("expected 'Login' in output")
		}
		if !strings.Contains(result, "Dashboard") {
			t.Error("expected 'Dashboard' in output")
		}
	})

	t.Run("caps at 10 labels", func(t *testing.T) {
		labels := make(map[string]bool)
		for i := 0; i < 15; i++ {
			labels[strings.Repeat("x", i+3)] = true
		}
		result := formatLabels(DiagramMermaid, "flowchart", labels)

		// Count commas — 10 labels means 9 commas
		commas := strings.Count(result, ", ")
		if commas > 9 {
			t.Errorf("expected at most 9 commas (10 labels), got %d", commas)
		}
	})
}

func TestProcessDiagramBlock(t *testing.T) {
	t.Run("mermaid flowchart", func(t *testing.T) {
		content := `graph TD
    A[User Login] --> B{Authenticated?}
    B -->|Yes| C[Dashboard]
    B -->|No| D[Error Page]`

		info, summary, ok := ProcessDiagramBlock("mermaid", content)

		if !ok {
			t.Fatal("expected isDiagram to be true")
		}
		if info == nil {
			t.Fatal("expected non-nil DiagramInfo")
		}
		if info.Type != DiagramMermaid {
			t.Errorf("type = %q, want %q", info.Type, DiagramMermaid)
		}
		if info.RawCode != content {
			t.Error("RawCode should match input content")
		}
		if !strings.Contains(summary, "[Diagram: mermaid flowchart") {
			t.Errorf("summary should contain diagram prefix, got %q", summary)
		}
		if !strings.Contains(summary, "User Login") {
			t.Errorf("summary should contain 'User Login', got %q", summary)
		}
	})

	t.Run("plantuml diagram", func(t *testing.T) {
		content := `@startuml
title Order Processing
actor Customer
participant "Order Service"
Customer -> Order Service: Place Order
@enduml`

		info, summary, ok := ProcessDiagramBlock("plantuml", content)

		if !ok {
			t.Fatal("expected isDiagram to be true")
		}
		if info.Type != DiagramPlantUML {
			t.Errorf("type = %q, want %q", info.Type, DiagramPlantUML)
		}
		if !strings.Contains(summary, "Order Processing") {
			t.Errorf("summary should contain 'Order Processing', got %q", summary)
		}
	})

	t.Run("non-diagram code block", func(t *testing.T) {
		content := `func main() {
    fmt.Println("hello")
}`

		info, summary, ok := ProcessDiagramBlock("go", content)

		if ok {
			t.Error("expected isDiagram to be false for Go code")
		}
		if info != nil {
			t.Error("expected nil info for non-diagram")
		}
		if summary != "" {
			t.Error("expected empty summary for non-diagram")
		}
	})

	t.Run("ascii diagram auto-detected", func(t *testing.T) {
		content := "+--------+    +--------+\n|  Auth  | -> |  User  |\n+--------+    +--------+\n|        |    |        |\n+--------+    +--------+"

		info, summary, ok := ProcessDiagramBlock("", content)

		if !ok {
			t.Fatal("expected isDiagram to be true for ASCII diagram")
		}
		if info.Type != DiagramASCII {
			t.Errorf("type = %q, want %q", info.Type, DiagramASCII)
		}
		if !strings.Contains(summary, "[Diagram: ascii") {
			t.Errorf("summary should contain ascii diagram prefix, got %q", summary)
		}
	})

	t.Run("puml alias", func(t *testing.T) {
		content := `@startuml
class UserService
@enduml`

		info, _, ok := ProcessDiagramBlock("puml", content)

		if !ok {
			t.Fatal("expected isDiagram to be true for puml")
		}
		if info.Type != DiagramPlantUML {
			t.Errorf("type = %q, want %q", info.Type, DiagramPlantUML)
		}
	})
}

func TestProcessDiagramBlockIntegration(t *testing.T) {
	t.Run("mermaid auth flow searchable by authentication", func(t *testing.T) {
		content := `graph TD
    A[User Enters Credentials] --> B{Valid Credentials?}
    B -->|Yes| C[Generate JWT Token]
    B -->|No| D[Show Authentication Error]
    C --> E[Redirect to Dashboard]`

		_, summary, ok := ProcessDiagramBlock("mermaid", content)

		if !ok {
			t.Fatal("expected diagram detection")
		}

		// The summary should contain terms that make it searchable
		// for "authentication flow" queries
		searchTerms := []string{"User Enters Credentials", "Generate JWT Token", "Authentication Error"}
		for _, term := range searchTerms {
			if !strings.Contains(summary, term) {
				t.Errorf("summary should contain %q for search relevance, got %q", term, summary)
			}
		}
	})
}
