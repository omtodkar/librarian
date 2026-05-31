# Local cross-encoder rerankers for code retrieval — full report

> Output of deep-research spike **2026-05-31** (workflow `wf_b49c6ebf-2fb`).
> Index entry: [`../../research-spikes.md`](../../research-spikes.md).
> Sources + verification verdicts: [`sources.md`](sources.md).
>
> Method: 5 search angles → 18 sources fetched → 80 claims extracted → 25
> verified with 3-vote adversarial verification (need 2/3 refutes to kill) →
> 22 confirmed, 3 killed → synthesized to 7 findings.

## Question

Survey the 2025–2026 landscape of open-weight **cross-encoder** rerankers for
code + technical-documentation retrieval that Infinity (`michaelfeil/infinity`)
can serve — models declaring `AutoModelForSequenceClassification` or an
equivalent cross-encoder head, **not** causal-LM rerankers like Qwen3-Reranker,
which Infinity's `/rerank` refuses. Baselines: `gte-reranker-modernbert-base`
(149M, 8K ctx, Apache 2.0, COIR ~79.99) plus `BAAI/bge-reranker-v2-m3`,
`mixedbread-ai/mxbai-rerank-base-v2`, `jinaai/jina-reranker-v2-base-multilingual`.
Determine whether a newer/better Apache-or-MIT reranker should replace or
supplement the default, and whether the alternatives table needs updating.

## Bottom line

**No clearly-superior Apache/MIT cross-encoder has emerged to displace the
current default.** `gte-reranker-modernbert-base` remains the only candidate that
is *simultaneously* (a) verified-strong on a real code-retrieval benchmark (COIR
avg **79.99** / 20 tasks), (b) confirmed permissive (Apache 2.0), and (c) a
confirmed **native `AutoModelForSequenceClassification`** head that Infinity's
`/rerank` serves directly. **Keep it as the default.** Update the alternatives
table (see "Decision" → applied changes below).

## Findings (with verification confidence)

### 1. The current default is still a correct, strong baseline — `high` (3-0)
`Alibaba-NLP/gte-reranker-modernbert-base`: **149M** params, **8192**-token
context, ModernBERT encoder-only, **Apache 2.0**, native
`AutoModelForSequenceClassification` (and `CrossEncoder`), **COIR code-retrieval
avg 79.99** across 20 tasks. Encoder-only + seq-classification head = satisfies
Infinity's `/rerank` requirement. Corroborated by the HF model card plus
independent catalogs.

### 2. Infinity's `/rerank` is BERT-style-classification only — `high` (3-0)
Infinity requires "an `AutoModelForSequenceClassification` compatible model with
one class classification" / "bert-style classification Models with one category".
Maintainer (Michael Feil, discussion #558): *"/rerank is not going to cut it, it
needs to be a sequence classification / classify endpoint model."* This confirms
the spike's filtering premise: causal-LM rerankers are out.

### 3. mxbai-rerank-v2 is strong but causal-LM → NOT natively servable — `high` (3-0)
`mixedbread-ai/mxbai-rerank-v2` (base **0.5B** / large **1.5B**, **Apache 2.0**,
109 languages incl. code) is built **on Qwen-2.5** via RL (GRPO) and declares
`Qwen2ForCausalLM`. Infinity's `/rerank` rejects it verbatim:
`ModelNotDeployedError: model=mixedbread-ai/mxbai-rerank-large-v2 does not support
rerank ... Options are {embed}.` This is exactly the causal-LM mismatch the
question warned about.

### 4. mxbai-v2 is servable only via the maintainer's `-seq` conversions — `high` (3-0)
`michaelfeil/mxbai-rerank-{base,large}-v2-seq` (by the Infinity author) rewrite
the reranker as a true `Qwen2ForSequenceClassification` (num_labels=2, Apache
2.0). **Caveats:** the "officially recommended for Infinity" framing was
**refuted (1-2)**, and in practice a `classify→rerank` proxy
(`qdrddr/infinity-mxbai-rerank-seq-v2`) is commonly needed — servable, but not
friction-free.

### 5. Ettin Reranker — new 2026 Apache option, but unproven for code/Infinity — `medium`
`cross-encoder/ettin-reranker-{17m..1b}-v1` (Johns Hopkins Ettin / Sentence
Transformers, **Apache 2.0**, 8K ctx, 17.6M–1.00B). The 150M variant beats the
default on **English MTEB(eng,v2)** retrieval (0.5994 vs 0.5843 NDCG@10) and is
faster on paper. **Three caveats block a drop-in recommendation:** (1) **no
COIR/MTEB-Code numbers published** — its edge is English-generic retrieval, not
code; (2) the 2.3× speed gain is harness-dependent and "only available for models
built with a modular Transformer" — Infinity's `AutoModelForSequenceClassification`
path "lands you in the slower bf16+FA2 w. padding column", so the speedup likely
does **not** transfer; (3) the claim that it uses a plain `AutoModel`+Pooling/Dense
head was **refuted (1-2)**, so its exact Infinity-servable head is unconfirmed.

### 6. jina-reranker-v2 is servable + strong on code, but non-commercial — `high` (3-0)
`jinaai/jina-reranker-v2-base-multilingual`: **278M**, true
`AutoModelForSequenceClassification` (XLM-RoBERTa), Infinity-servable, strong code
retrieval (CodeSearchNet MRR@10 **71.36** vs bge-reranker-v2-m3 62.86). **But
licensed CC-BY-NC-4.0 (non-commercial)** — disqualifying as a permissive default —
and native context is only **1024 tokens** (vs 8K for gte/Ettin).

### 7. COIR is the right yardstick — `high` (3-0)
COIR (ACL 2025 Main, arXiv:2407.02883): 10 curated code datasets, 8 tasks, 7
domains, ~2M docs. Validates the 79.99 baseline as measured on an authoritative
code-IR benchmark.

## Candidate comparison

| Model | Params | Ctx | License | Languages | Code score | Infinity-servable? |
|---|---|---|---|---|---|---|
| **gte-reranker-modernbert-base** (default) | 149M | 8K | **Apache 2.0** | English | **COIR 79.99** (20 tasks) | ✅ native seq-classification |
| bge-reranker-v2-m3 | 568M | 8K | Apache 2.0 | 100+ | CodeSearchNet MRR@10 62.86 | ✅ native |
| mxbai-rerank-base-v2 | 0.5B | — | Apache 2.0 | 109 + code | single CoSQA point 31.73 | ❌ native (Qwen2 causal-LM) → ⚠️ via `-seq` + proxy |
| mxbai-rerank-large-v2 | 1.5B | — | Apache 2.0 | 109 + code | — | ❌ native → ⚠️ via `-seq` + proxy |
| Ettin-reranker-150m-v1 | 150M | 8K | Apache 2.0 | English | **none published** | ⚠️ unconfirmed head; speed gain unlikely on Infinity path |
| jina-reranker-v2-base-multilingual | 278M | 1K | **CC-BY-NC-4.0** ⚠️ | 100+ | CodeSearchNet MRR@10 71.36 | ✅ native |

> Benchmark scores are **not on a common scale** — gte reports COIR, jina reports
> CodeSearchNet MRR@10, mxbai a single CoSQA point, Ettin only English MTEB with
> no code benchmark. They cannot be ranked head-to-head for code retrieval.

## Recommendation

1. **Keep `gte-reranker-modernbert-base` as Librarian's default reranker.** It is
   the only option meeting all three bars (verified code score + permissive
   license + native Infinity head).
2. **Refresh the alternatives table** in [`../../configuration.md`](../../configuration.md#reranker-model-choice):
   - Add the **mxbai-rerank-v2-seq** conversions as Apache-licensed options, noting
     native mxbai-v2 is causal-LM (not `/rerank`-servable) and the `-seq` path
     commonly needs a classify→rerank proxy.
   - Add **Ettin-reranker** as a new 2026 Apache option, flagged "no code
     benchmarks; Infinity speed/servability unconfirmed."
   - **Flag jina-v2's CC-BY-NC-4.0** non-commercial license more prominently
     (already noted; keep).

## Caveats

- **Time-sensitive:** mxbai-v2 is a 2025 release, Ettin is May-2026 — re-verify
  before adopting. The timestamp on this spike is the staleness signal.
- **Weak comparability:** scores span different benchmarks (above); no common scale.
- **Self-reported numbers:** jina / mixedbread / Ettin figures are vendor-published,
  not independently re-run here.
- One minor source discrepancy: one fetch read mxbai large-v2 as "2B"; **1.5B** is
  correct per first-party blog/HF (Qwen2.5-1.5B base).

## Open questions

1. Does Ettin Reranker actually expose an `AutoModelForSequenceClassification`
   single-class head that loads directly on Infinity's `/rerank`, or does it need a
   sentence-transformers `CrossEncoder` head Infinity may not serve? (refuted claim
   — unresolved)
2. What is Ettin's **code-retrieval** performance (COIR / MTEB-Code)? None published.
3. On Infinity's seq-classification path (not the modular-Transformer fast path),
   what is Ettin-150M's real throughput vs the default? The 2.3× gap likely collapses.
4. What COIR-comparable score do the mxbai-v2-seq conversions reach, and is the
   classify→rerank proxy operationally acceptable for Librarian vs the current
   Apache default?
