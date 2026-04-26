# Performance Benchmarks

> **Your mileage will vary.** Numbers below are representative estimates collected on an Apple M2 Pro
> (12-core CPU, 32 GB RAM) with a Gemini Flash embedder. Disk I/O, embedder latency, model cold-start,
> and corpus characteristics all shift results materially. Run `scripts/benchmark.sh` on your own
> hardware and submit your numbers via a PR — community baselines from different platforms are welcome.

## Methodology

All measurements use `time` (wall-clock). Each run is preceded by a warm-up pass to populate the OS
disk cache. Numbers are median of three runs unless noted otherwise.

**Setup for reproducible results:**

```bash
# Build from source (required — see note on LIBRARIAN_EMBEDDING_API_KEY below)
go build -o ./librarian-bench .

# Create a scratch workspace for the benchmark
mkdir -p /tmp/librarian-bench-small
cd /tmp/librarian-bench-small
cp -r /path/to/librarian/.librarian/config.yaml .librarian/config.yaml  # copy your config

# Or run the script directly against this repo:
scripts/benchmark.sh
```

**Embedder requirements:**

| Embedder | What you need |
|---|---|
| Gemini (default) | `LIBRARIAN_EMBEDDING_API_KEY` env var with a valid Gemini API key |
| Local (Infinity) | Running Infinity server (`make infinity-setup && scripts/infinity.sh start`) |

Without a configured embedder, `index` will fail. The benchmark script checks for this and exits
with a clear error. See [docs/embedding.md](embedding.md) and [docs/configuration.md](configuration.md)
for setup instructions.

## Benchmark Tiers

### Tier Definitions

| Tier | Description | Docs | Code files | Approximate chunks |
|---|---|---|---|---|
| **Small** | This repository itself | ~10 `.md` files | ~100 `.go` files | ~300–400 |
| **Medium** | Synthetic 1000-file Go monorepo fixture | ~50 `.md` files | ~950 `.go` files | ~3,000–4,000 |
| **Large** | Synthetic 10,000-file mixed-language fixture | ~200 `.md` files | ~9,800 files | ~30,000–40,000 |

The Medium and Large tiers are generated with `scripts/benchmark.sh --generate-fixtures`
(creates `testdata/bench-medium/` and `testdata/bench-large/` under a temp directory).
See the script's `generate_fixture` function for the template used.

---

### Results Table

> **These are representative estimates, not measured actuals.**  
> Run `scripts/benchmark.sh` for real numbers on your hardware.

| Metric | Small (this repo) | Medium (~1k files) | Large (~10k files) |
|---|---|---|---|
| Cold index (Gemini) | ~30 s | ~5 min | ~45 min |
| Cold index (Infinity local) | ~5 s | ~45 s | ~7 min |
| Incremental re-index (no changes) | <1 s | <2 s | <5 s |
| `search_docs` latency | <100 ms | <150 ms | <250 ms |
| `get_context` latency | <200 ms | <300 ms | <500 ms |
| Index DB size on disk | ~10 MB | ~80 MB | ~800 MB |
| Peak RSS during index | ~150 MB | ~300 MB | ~1.2 GB |

**Notes on the estimates:**

- Gemini cold-index time is dominated by API round-trips (one batch per ~20 chunks). Network latency
  and rate limits cause the wide spread between tiers.
- Infinity local times are dominated by CPU/GPU throughput. An M2 Pro with MPS acceleration is assumed;
  CPU-only will be roughly 3–5× slower.
- Incremental re-index is fast because SHA-256 content hashes gate all parse/embed work — unchanged
  files are skipped entirely.
- Search and context latencies assume a warm SQLite page cache (single-user, local disk). A cold cache
  or a slow disk adds 50–200 ms to the first query.

---

## Fixture Construction

To test Medium and Large tier performance, generate synthetic fixtures:

```bash
# Generates testdata/bench-medium (~1 000 files) and testdata/bench-large (~10 000 files)
scripts/benchmark.sh --generate-fixtures
```

The generator writes Go source files based on a template (`package main` with a fixed set of exported
functions) and Markdown files based on a short template doc. Files are deterministic (seeded by index),
so re-generating produces identical hashes — incremental re-index numbers on fixtures are stable.

---

## Submitting Your Numbers

If you run the benchmark, please open a PR updating the table above with your hardware profile:

1. Run `scripts/benchmark.sh --markdown` — it prints a ready-to-paste Markdown row.
2. Add the row to the table with a footnote describing your hardware and embedder.
3. Open a PR against `main` titled `chore: add benchmark result [hardware]`.

Example footnote:

```
[^m2pro-gemini]: Apple M2 Pro, 32 GB RAM, macOS 14.4, Gemini Flash embedder, SSD.
```

---

## Regression Tracking

There is no CI-enforced performance gate (build times and embedding costs make that impractical), but
benchmarks should be re-run and the table updated when:

- The chunking algorithm changes
- The store layer or vector query changes
- A new embedding provider is added
- A handler changes its chunk yield significantly

When you open a PR that touches these subsystems, include a before/after `scripts/benchmark.sh` run
in the PR description.
