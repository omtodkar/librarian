---
title: Deep Research Spikes
type: reference
description: Chronological log of deep-research spikes run via the deep-research workflow — questions, method, findings, and the decisions they fed.
---

# Deep Research Spikes

A running register of **deep-research spikes**: time-boxed, multi-source web-research
passes run to answer an open question that the codebase alone can't settle (model
choices, third-party tradeoffs, ecosystem shifts). Each spike is run through the
`deep-research` workflow — fan-out web searches → source fetch → adversarial
claim verification → cited synthesis — and recorded here so a future session can
see *what was asked, when, what we found, and what we decided* without re-running
the work.

This file is the **index**. The full output of each spike — the synthesized
report, the cited sources, and any raw artifacts — lives in a per-spike folder
under [`research-spikes/`](research-spikes/):

```
docs/
  research-spikes.md                      ← this index (one short entry per spike)
  research-spikes/
    YYYY-MM-DD-<slug>/
      report.md                           ← full synthesized findings
      sources.md                          ← cited sources (URL + what each backs)
      …                                   ← optional raw artifacts (claims, notes)
```

Keep the index entry **short** (headline findings + decision) and put the detail
in the folder, so this file stays scannable as the register grows.

## How to add an entry

1. Run the spike: `Workflow({ name: "deep-research", args: "<refined question>" })`
   (or invoke the `deep-research` skill, which scopes the question first).
2. When it completes, create the output folder `research-spikes/YYYY-MM-DD-<slug>/`
   and save the full synthesized report to `report.md` and the cited sources to
   `sources.md` (plus any raw artifacts worth keeping).
3. Add a new `## YYYY-MM-DD — <title>` section **at the top of the log** (newest
   first) using the template below, linking to the output folder.
4. Record the date/time the spike was *run* (with timezone), the exact question,
   the headline findings with confidence, the resulting decision, and the
   load-bearing sources. Link any bead the spike opened or closed.

Keep findings dated — reranker/embedding/model landscapes move fast, and a claim
that was true on the run date may not hold six months later. The timestamp is the
expiry signal.

### Entry template

```md
## YYYY-MM-DD — <short title>

- **Run at:** YYYY-MM-DD HH:MM:SS <TZ> (workflow `<run-id>`)
- **Output:** [`research-spikes/YYYY-MM-DD-<slug>/`](research-spikes/YYYY-MM-DD-<slug>/) — full report + sources
- **Asked by / context:** <who + why>
- **Question:** <the refined research question passed to the workflow>
- **Status:** complete | in progress | superseded by <entry>

**Findings** (confidence in parens):

- … (high)
- … (medium)

**Decision:** <what we changed / chose / deferred, + bead id if any>

**Sources:** see [`sources.md`](research-spikes/YYYY-MM-DD-<slug>/sources.md)
```

---

## 2026-05-31 — Local cross-encoder rerankers for code retrieval (Infinity-servable)

- **Run at:** 2026-05-31 12:04 IST (+0530) (workflow `wf_b49c6ebf-2fb`)
- **Output:** [`research-spikes/2026-05-31-local-rerankers/`](research-spikes/2026-05-31-local-rerankers/) — full report + sources
- **Asked by / context:** Follow-up to the docs-only / `graph.enabled` work — user
  asked whether a newer, better Apache/MIT-licensed reranker has emerged in 2025
  that should replace or supplement Librarian's default
  (`Alibaba-NLP/gte-reranker-modernbert-base`), and whether the alternatives table
  in [`configuration.md`](configuration.md#reranker-model-choice) needs updating.
- **Question:** Survey the 2025–2026 landscape of open-weight **cross-encoder**
  rerankers for code + technical-documentation retrieval that Infinity
  (`michaelfeil/infinity`) can serve — i.e. models declaring
  `AutoModelForSequenceClassification` or an equivalent cross-encoder head, **not**
  causal-LM rerankers like Qwen3-Reranker, which Infinity's `/rerank` refuses. For
  each candidate: parameter count, max context length, license (flag
  non-commercial), language coverage, and code-retrieval scores (COIR / MTEB-Code /
  BEIR). Baselines: `gte-reranker-modernbert-base` (149M, 8K ctx, Apache 2.0, COIR
  ~79.99) plus existing alternatives `BAAI/bge-reranker-v2-m3`,
  `mixedbread-ai/mxbai-rerank-base-v2`, `jinaai/jina-reranker-v2-base-multilingual`.
  Confirm Infinity-servability for each recommendation.
- **Status:** complete (5 angles → 18 sources → 80 claims → 25 verified, 22 confirmed / 3 killed)

**Findings** (confidence in parens):

- No clearly-superior Apache/MIT cross-encoder has emerged to displace the default; `gte-reranker-modernbert-base` is still the only candidate that is *simultaneously* verified-strong on code retrieval (COIR avg **79.99**), permissively licensed (Apache 2.0), and a native `AutoModelForSequenceClassification` head Infinity serves directly. (high)
- **mxbai-rerank-v2** (0.5B/1.5B, Apache 2.0, strong) is built on a Qwen-2.5 **causal-LM** head — Infinity's `/rerank` refuses it natively; servable only via the maintainer's `mxbai-rerank-{base,large}-v2-seq` conversions, which commonly need a classify→rerank proxy. (high)
- **Ettin Reranker** (2026, Apache 2.0, 8K ctx) beats the default on *English* MTEB retrieval and is faster on paper, but publishes **no code benchmarks**, its speed edge likely doesn't transfer to Infinity's serving path, and its exact servable head is **unconfirmed** (claim refuted). (medium)
- **jina-reranker-v2** is a true Infinity-servable cross-encoder with strong code retrieval, but is **CC-BY-NC-4.0 (non-commercial)** — disqualified as a permissive default; 1K native context. (high)

**Decision:** **Keep `gte-reranker-modernbert-base` as the default.** Refresh the
alternatives table in [`configuration.md`](configuration.md#reranker-model-choice) —
add mxbai-v2-seq + Ettin as Apache options with their caveats, keep jina-v2's NC flag.
Tracked in bead `lib-5sp7`. Full report: [`report.md`](research-spikes/2026-05-31-local-rerankers/report.md).

**Sources:** see [`sources.md`](research-spikes/2026-05-31-local-rerankers/sources.md)
