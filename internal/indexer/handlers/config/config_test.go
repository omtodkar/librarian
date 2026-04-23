package config_test

import (
	"testing"

	"librarian/internal/indexer"
	"librarian/internal/indexer/handlers/config"
)

// confirm each handler satisfies the FileHandler contract at compile time.
var _ = []indexer.FileHandler{
	config.NewEnv(),
	config.NewProperties(),
	config.NewJSON(),
	config.NewTOML(),
	config.NewYAML(),
	config.NewXML(),
}

func TestHandlers_RegisteredByDefault(t *testing.T) {
	reg := indexer.DefaultRegistry()

	for _, ext := range []string{".env", ".properties", ".json", ".toml", ".yaml", ".yml", ".xml"} {
		if reg.HandlerFor("sample"+ext) == nil {
			t.Errorf("extension %q not registered", ext)
		}
	}
}

func TestEnv_ParseAndChunk(t *testing.T) {
	content := []byte(`# database creds — TODO: rotate in prod
DB_URL=postgres://localhost:5432/app
DB_USER=admin
DB_PASS=secret

# FIXME: move to secrets manager
API_KEY=sk-xxx
`)

	h := config.NewEnv()
	doc, err := h.Parse(".env", content)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if doc.Format != "env" {
		t.Errorf("Format = %q, want env", doc.Format)
	}
	if len(doc.Units) != 1 {
		t.Fatalf("expected 1 Unit, got %d", len(doc.Units))
	}
	if len(doc.Signals) == 0 {
		t.Error("expected TODO/FIXME signals, got none")
	}
	haveTodo, haveFixme := false, false
	for _, s := range doc.Signals {
		if s.Value == "todo" {
			haveTodo = true
		}
		if s.Value == "fixme" {
			haveFixme = true
		}
	}
	if !haveTodo || !haveFixme {
		t.Errorf("missing TODO/FIXME signals: %+v", doc.Signals)
	}

	// Use a low MinTokens for this test — .env snippets are small, and the
	// purpose here is to verify the handler wiring, not the chunker's minimum
	// size policy (covered by markdown parity tests).
	chunks, err := h.Chunk(doc, indexer.ChunkConfig{MaxTokens: 512, MinTokens: 5})
	if err != nil {
		t.Fatalf("Chunk: %v", err)
	}
	if len(chunks) == 0 {
		t.Error("expected at least one chunk")
	}
}

func TestProperties_ParseAndChunk(t *testing.T) {
	content := []byte(`# HACK: test override for local dev
spring.datasource.url=jdbc:h2:mem:testdb
spring.datasource.username=sa

! NOTE: port overridden by env in prod
server.port=8080
`)

	h := config.NewProperties()
	doc, err := h.Parse("application.properties", content)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if doc.Format != "properties" {
		t.Errorf("Format = %q, want properties", doc.Format)
	}
	if len(doc.Units) != 1 {
		t.Fatalf("expected 1 Unit, got %d", len(doc.Units))
	}

	haveHack, haveNote := false, false
	for _, s := range doc.Signals {
		if s.Value == "hack" {
			haveHack = true
		}
		if s.Value == "note" {
			haveNote = true
		}
	}
	if !haveHack || !haveNote {
		t.Errorf("missing HACK/NOTE signals: %+v", doc.Signals)
	}
}

func TestJSON_ParseObjectToUnitPerKey(t *testing.T) {
	content := []byte(`{
  "name": "librarian",
  "version": "0.1.0",
  "dependencies": {
    "foo": "^1.0",
    "bar": "^2.0"
  }
}`)

	h := config.NewJSON()
	doc, err := h.Parse("package.json", content)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(doc.Units) != 3 {
		t.Fatalf("expected 3 Units (name, version, dependencies), got %d: %+v", len(doc.Units), doc.Units)
	}
	want := map[string]bool{"name": true, "version": true, "dependencies": true}
	for _, u := range doc.Units {
		if !want[u.Title] {
			t.Errorf("unexpected Unit.Title %q", u.Title)
		}
	}
}

func TestJSON_MalformedFallsBackToSingleUnit(t *testing.T) {
	content := []byte(`{not: valid json`)
	h := config.NewJSON()
	doc, err := h.Parse("bad.json", content)
	if err != nil {
		t.Fatalf("Parse should not error on malformed JSON: %v", err)
	}
	if len(doc.Units) != 1 {
		t.Errorf("expected 1 fallback Unit, got %d", len(doc.Units))
	}
	if _, ok := doc.Units[0].Metadata["parse_error"]; !ok {
		t.Error("expected parse_error in metadata")
	}
}

func TestYAML_ParseToUnitPerTopLevelKey(t *testing.T) {
	content := []byte(`# database
spring:
  datasource:
    url: jdbc:h2:mem:testdb  # FIXME: tempfile in prod

# server
server:
  port: 8080
`)

	h := config.NewYAML()
	doc, err := h.Parse("application.yml", content)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(doc.Units) != 2 {
		t.Fatalf("expected 2 Units (spring, server), got %d", len(doc.Units))
	}

	haveSpring, haveServer := false, false
	for _, u := range doc.Units {
		if u.Title == "spring" {
			haveSpring = true
		}
		if u.Title == "server" {
			haveServer = true
		}
	}
	if !haveSpring || !haveServer {
		t.Errorf("missing top-level keys: %+v", doc.Units)
	}
}

func TestTOML_ParseToUnitPerTable(t *testing.T) {
	content := []byte(`[database]
url = "postgres://localhost"
port = 5432

[server]
host = "0.0.0.0"
`)

	h := config.NewTOML()
	doc, err := h.Parse("config.toml", content)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(doc.Units) != 2 {
		t.Fatalf("expected 2 Units (database, server), got %d", len(doc.Units))
	}
}

func TestXML_ParseSingleUnitWithRationaleFromComments(t *testing.T) {
	content := []byte(`<?xml version="1.0"?>
<project>
  <!-- TODO: upgrade jackson -->
  <dependencies>
    <dependency>jackson-databind</dependency>
  </dependencies>
</project>
`)

	h := config.NewXML()
	doc, err := h.Parse("pom.xml", content)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(doc.Units) != 1 {
		t.Errorf("expected 1 Unit, got %d", len(doc.Units))
	}
	haveTodo := false
	for _, s := range doc.Signals {
		if s.Value == "todo" {
			haveTodo = true
		}
	}
	if !haveTodo {
		t.Errorf("expected TODO signal from XML comment, got %+v", doc.Signals)
	}
}
