#!/usr/bin/env bash
# benchmark.sh — measure Librarian index and query performance on a real workspace.
#
# Usage:
#   scripts/benchmark.sh [OPTIONS]
#
# Options:
#   --markdown          Print results as a Markdown table row (for submitting to docs/benchmarks.md)
#   --generate-fixtures Create synthetic Medium and Large test fixtures under /tmp/librarian-bench/
#   --fixture=PATH      Run against PATH instead of the current workspace
#   -h, --help          Show this message
#
# Requirements:
#   - A .librarian/ workspace in the current directory (or --fixture path)
#   - LIBRARIAN_EMBEDDING_API_KEY set (Gemini) OR a running Infinity server
#     configured in .librarian/config.yaml
#   - go (to build the binary if ./librarian-bench doesn't exist)
#
# The script builds ./librarian-bench from source if the binary is missing.
# It does NOT use any globally-installed 'librarian' binary — isolation first.
#
# See docs/benchmarks.md for methodology, expected numbers, and submission instructions.

set -euo pipefail

# ── Defaults ──────────────────────────────────────────────────────────────────

MARKDOWN_OUTPUT=false
GENERATE_FIXTURES=false
FIXTURE_PATH=""

# ── Argument parsing ──────────────────────────────────────────────────────────

for arg in "$@"; do
    case "$arg" in
        --markdown)          MARKDOWN_OUTPUT=true ;;
        --generate-fixtures) GENERATE_FIXTURES=true ;;
        --fixture=*)         FIXTURE_PATH="${arg#--fixture=}" ;;
        -h|--help)
            sed -n '2,/^[^#]/p' "$0" | grep '^#' | sed 's/^# \?//'
            exit 0
            ;;
        *)
            echo "Unknown option: $arg" >&2
            echo "Run with --help for usage." >&2
            exit 1
            ;;
    esac
done

# ── Helpers ───────────────────────────────────────────────────────────────────

# Print to stderr so it doesn't pollute --markdown output.
info()  { echo "==> $*" >&2; }
error() { echo "ERROR: $*" >&2; exit 1; }

# now_ms — current epoch in milliseconds, portable across BSD date (macOS) and GNU date (Linux).
# BSD date does not support %N (nanoseconds); fall back to python3 which ships on both platforms.
if date +%s%N 2>/dev/null | grep -qE '^[0-9]{15,}$'; then
    now_ms() { echo $(( $(date +%s%N) / 1000000 )); }
elif command -v gdate >/dev/null 2>&1; then
    # Homebrew coreutils provides gdate with GNU semantics.
    now_ms() { echo $(( $(gdate +%s%N) / 1000000 )); }
else
    now_ms() { python3 -c "import time; print(int(time.time() * 1000))"; }
fi

# time_cmd <label> <command…>
# Runs a command, prints wall-clock seconds, and stores the result in LAST_SECONDS.
LAST_SECONDS=""
time_cmd() {
    local label="$1"; shift
    info "Running: $label"
    local start end elapsed
    start=$(now_ms)
    "$@" >/dev/null 2>&1 || {
        info "  FAILED: $*"
        LAST_SECONDS="FAILED"
        return 0
    }
    end=$(now_ms)
    elapsed=$(( end - start ))   # ms
    if (( elapsed < 1000 )); then
        LAST_SECONDS="${elapsed}ms"
    else
        LAST_SECONDS="$(awk "BEGIN{printf \"%.1fs\", $elapsed/1000}")"
    fi
    info "  done in ${LAST_SECONDS}"
}

# db_size <workspace>
# Returns a human-readable DB size string.
db_size() {
    local db="$1/.librarian/librarian.db"
    if [[ -f "$db" ]]; then
        du -sh "$db" 2>/dev/null | awk '{print $1}'
    else
        echo "n/a"
    fi
}

# ── Build ──────────────────────────────────────────────────────────────────────

BENCH_BIN="./librarian-bench"

ensure_binary() {
    if [[ ! -x "$BENCH_BIN" ]]; then
        info "Binary not found at $BENCH_BIN — building from source…"
        if ! command -v go >/dev/null 2>&1; then
            error "go not found in PATH. Install Go 1.26+ to build librarian."
        fi
        go build -o "$BENCH_BIN" . || error "go build failed. See output above."
        info "Built $BENCH_BIN."
    fi
}

# ── Fixture generation ────────────────────────────────────────────────────────

generate_fixture() {
    local name="$1"   # e.g. "medium"
    local n_go="$2"   # e.g. 950
    local n_md="$3"   # e.g. 50
    local out_dir="/tmp/librarian-bench/$name"

    info "Generating $name fixture → $out_dir  (${n_go} Go files + ${n_md} Markdown files)"
    rm -rf "$out_dir"
    mkdir -p "$out_dir/internal/pkg" "$out_dir/docs"

    # Go source template — one exported function per file, varies by index.
    for i in $(seq 1 "$n_go"); do
        cat >"$out_dir/internal/pkg/file_${i}.go" <<GOEOF
package pkg

import "fmt"

// Entity${i} is a synthetic struct for benchmarking purposes.
type Entity${i} struct {
    ID   int
    Name string
}

// Process${i} performs a placeholder operation on Entity${i}.
func Process${i}(e Entity${i}) string {
    return fmt.Sprintf("entity-%d-%s", e.ID, e.Name)
}
GOEOF
    done

    # Markdown doc template — each doc has a heading, two sections, and a code block.
    for i in $(seq 1 "$n_md"); do
        cat >"$out_dir/docs/doc_${i}.md" <<MDEOF
# Document ${i}

This is synthetic documentation for benchmarking purposes.

## Overview

Document ${i} covers the concepts introduced in \`internal/pkg/file_${i}.go\`.
The primary type is \`Entity${i}\` which holds an identifier and a name.

## Usage

\`\`\`go
e := pkg.Entity${i}{ID: ${i}, Name: "example"}
result := pkg.Process${i}(e)
fmt.Println(result) // entity-${i}-example
\`\`\`

## Notes

This document is generated and exists solely to provide a realistic chunk count
for performance measurement. It does not represent real functionality.
MDEOF
    done

    # Minimal config so librarian can index without a real workspace parent.
    mkdir -p "$out_dir/.librarian"
    cat >"$out_dir/.librarian/config.yaml" <<CFGEOF
docs_dir: docs
project_root: .
CFGEOF

    info "Fixture $name generated: $(find "$out_dir" -type f | wc -l | tr -d ' ') files"
    echo "$out_dir"
}

if $GENERATE_FIXTURES; then
    generate_fixture "medium" 950  50
    generate_fixture "large"  9800 200
    info "Fixtures written to /tmp/librarian-bench/"
    exit 0
fi

# ── Main benchmark ────────────────────────────────────────────────────────────

ensure_binary

WORKSPACE="."
if [[ -n "$FIXTURE_PATH" ]]; then
    WORKSPACE="$FIXTURE_PATH"
fi

if [[ ! -d "$WORKSPACE/.librarian" ]]; then
    error "No .librarian/ workspace found at $WORKSPACE. Run 'librarian init' first, or use --fixture."
fi

info "Benchmarking workspace: $(realpath "$WORKSPACE")"

# Cold index (--force re-indexes everything).
time_cmd "cold index (--force)" "$BENCH_BIN" index --force
T_COLD="$LAST_SECONDS"

# Incremental re-index (no changes — should be near-instant).
time_cmd "incremental re-index" "$BENCH_BIN" index
T_INCR="$LAST_SECONDS"

# Single search query.
time_cmd "search: 'embedding'" "$BENCH_BIN" search "embedding"
T_SEARCH="$LAST_SECONDS"

# get_context (heavier graph-joined query, routed through the CLI).
time_cmd "context: 'MCP server'" "$BENCH_BIN" context "MCP server"
T_CONTEXT="$LAST_SECONDS"

# DB size on disk.
T_DBSIZE=$(db_size "$WORKSPACE")

info "Done."

# ── Output ────────────────────────────────────────────────────────────────────

HARDWARE="$(uname -m) / $(uname -s)"

if $MARKDOWN_OUTPUT; then
    # Single row ready to paste into docs/benchmarks.md.
    echo ""
    echo "| ${HARDWARE} | ${T_COLD} | ${T_INCR} | ${T_SEARCH} | ${T_CONTEXT} | ${T_DBSIZE} |"
    echo ""
    echo "<!-- Paste the row above into the Results Table in docs/benchmarks.md -->"
    echo "<!-- Add a footnote with: hardware, OS version, embedder used, date -->"
else
    # Human-readable summary.
    cat <<EOF

╔══════════════════════════════════════════════════════╗
║            Librarian Benchmark Results               ║
╠══════════════════════════════════════════════════════╣
║  Workspace   : $(realpath "$WORKSPACE")
║  Hardware    : ${HARDWARE}
╠══════════════════════════════════════════════════════╣
║  Cold index (--force)         : ${T_COLD}
║  Incremental re-index         : ${T_INCR}
║  search "embedding"           : ${T_SEARCH}
║  context "MCP server"         : ${T_CONTEXT}
║  DB size on disk              : ${T_DBSIZE}
╚══════════════════════════════════════════════════════╝

To submit these numbers, re-run with --markdown and follow the instructions
in docs/benchmarks.md § "Submitting Your Numbers".
EOF
fi
