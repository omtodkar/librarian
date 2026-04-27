package indexer

// White-box benchmarks for callsites_rpc.go internal functions.
// Uses package indexer (not package indexer_test) to access unexported symbols.

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"librarian/internal/store"
)

// BenchmarkBuildClientExportIndex measures the pre-pass cost introduced by
// lib-r4s.3. The pre-pass is O(N) in the number of TS/JS source files, each
// parsed once with the shared tree-sitter parser instance. On a fixture of N
// files (half with exported clients, half plain), this should scale linearly
// and stay well under a 2× multiple of the existing per-file scan cost.
//
// Run with:
//
//	go test -tags fts5 ./internal/indexer -bench=BenchmarkBuildClientExportIndex -benchmem
func BenchmarkBuildClientExportIndex(b *testing.B) {
	const fileCount = 20 // enough to amortise setup, small enough to run fast

	dir := b.TempDir()

	// connectExportIndex with one service entry (mirrors the real index shape).
	index := connectExportIndex{
		"auth_connect": {"AuthService": "auth.v1.AuthService"},
	}

	// Write the connect-es stub that clients will import from.
	stubPath := filepath.Join(dir, "auth_connect.ts")
	stubContent := `export const AuthService = {
  typeName: "auth.v1.AuthService",
  methods: { login: { name: "Login" } },
} as const;
`
	if err := os.WriteFile(stubPath, []byte(stubContent), 0o600); err != nil {
		b.Fatalf("write stub: %v", err)
	}

	// Half the files export a client; half are plain TS files with no client.
	var nodes []store.Node
	for i := 0; i < fileCount; i++ {
		var content string
		if i%2 == 0 {
			content = fmt.Sprintf(`import { createPromiseClient } from "@connectrpc/connect";
import { AuthService } from "./auth_connect";
const transport = {};
export const client%d = createPromiseClient(AuthService, transport);
`, i)
		} else {
			content = fmt.Sprintf(`export function helper%d() { return %d; }
`, i, i)
		}
		p := filepath.Join(dir, fmt.Sprintf("mod%d.ts", i))
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			b.Fatalf("write file %d: %v", i, err)
		}
		// SourcePath relative to dir, matching how the store records it.
		nodes = append(nodes, store.Node{SourcePath: fmt.Sprintf("mod%d.ts", i)})
	}

	b.ResetTimer()
	for range b.N {
		exports := buildClientExportIndex(nodes, index, dir)
		if len(exports) == 0 {
			b.Fatal("expected non-empty exports")
		}
	}
}
