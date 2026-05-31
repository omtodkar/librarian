# Sources — local cross-encoder rerankers spike (2026-05-31)

Cited sources for [`report.md`](report.md), with what each backs and the
adversarial-verification verdict where applicable. Workflow `wf_b49c6ebf-2fb`:
18 sources fetched, 80 claims extracted, 25 verified (22 confirmed, 3 killed).

## Load-bearing sources (primary)

| Source | Backs | Quality |
|--------|-------|---------|
| [gte-reranker-modernbert-base (HF card)](https://huggingface.co/Alibaba-NLP/gte-reranker-modernbert-base) | Default: 149M, 8K ctx, Apache 2.0, native seq-classification, COIR 79.99 | primary |
| [Infinity README](https://github.com/michaelfeil/infinity/blob/main/README.md) | `/rerank` requires AutoModelForSequenceClassification "bert-style, one category" | primary |
| [Infinity discussion #558](https://github.com/michaelfeil/infinity/discussions/558) | Maintainer: causal-LM rerankers refused; mxbai-v2 `ModelNotDeployedError`; `-seq` fix | primary |
| [mixedbread mxbai-rerank-v2 blog](https://www.mixedbread.com/blog/mxbai-rerank-v2) | base 0.5B / large 1.5B, Apache 2.0, built on Qwen-2.5 via GRPO | primary |
| [mxbai-rerank-base-v2 (HF card)](https://huggingface.co/mixedbread-ai/mxbai-rerank-base-v2) | config `Qwen2ForCausalLM`, AutoModelForCausalLM usage, 109 langs + code | primary |
| [michaelfeil/mxbai-rerank-base-v2-seq (HF card)](https://huggingface.co/michaelfeil/mxbai-rerank-base-v2-seq) | "rewritten as Classifier", `Qwen2ForSequenceClassification`, num_labels=2, Apache 2.0 | primary |
| [Ettin Reranker blog (T. Aarsen)](https://huggingface.co/blog/ettin-reranker) | 17.6M–1B, Apache 2.0, 8K ctx; 150M MTEB 0.5994 vs 0.5843; speedup caveats | primary |
| [jina-reranker-v2 (HF card)](https://huggingface.co/jinaai/jina-reranker-v2-base-multilingual) | 278M, AutoModelForSequenceClassification, CC-BY-NC-4.0, 1024 ctx | primary |
| [jina-reranker-v2 announcement](https://jina.ai/news/jina-reranker-v2-for-agentic-rag-ultra-fast-multilingual-function-calling-and-code-search/) | CodeSearchNet MRR@10 71.36 vs bge-v2-m3 62.86 | primary |
| [CoIR benchmark (GitHub)](https://github.com/CoIR-team/coir) | 10 datasets / 8 tasks / 7 domains / ~2M docs; ACL 2025 Main | primary |
| [CoIR paper (arXiv:2407.02883)](https://arxiv.org/abs/2407.02883) / [ACL Anthology](https://aclanthology.org/2025.acl-long.1072/) | COIR authority / acceptance | primary |
| [qdrddr/infinity-mxbai-rerank-seq-v2](https://github.com/qdrddr/infinity-mxbai-rerank-seq-v2) | classify→rerank proxy commonly needed for mxbai-v2-seq on Infinity | forum |

## Supporting / secondary

| Source | Backs | Quality |
|--------|-------|---------|
| [ModernBERT blog](https://huggingface.co/blog/modernbert) | ModernBERT encoder architecture context | primary |
| [jina-reranker-v3 announcement](https://jina.ai/news/jina-reranker-v3-0-6b-listwise-reranker-for-sota-multilingual-retrieval/) | newest jina (listwise; not a seq-classification cross-encoder) | primary |
| [Qwen3-Reranker-0.6B discussion #3](https://huggingface.co/Qwen/Qwen3-Reranker-0.6B/discussions/3) | causal-LM reranker serving constraints | forum |
| [PromptLayer: mxbai-rerank-base-v2](https://www.promptlayer.com/models/mxbai-rerank-base-v2/) | corroborating mxbai specs | secondary |
| [Toolify: gte-reranker-modernbert-base](https://www.toolify.ai/ai-model/alibaba-nlp-gte-reranker-modernbert-base) | corroborating default specs | secondary |
| [ZeroEntropy: 2025 reranker guide](https://zeroentropy.dev/articles/ultimate-guide-to-choosing-the-best-reranking-model-in-2025/) | landscape framing | blog |

## Killed claims (refuted ≥2/3 — did NOT make the report)

| Refuted claim | Vote | Source |
|---|---|---|
| The mxbai `-seq` variant is "officially recommended for Infinity" | 1-2 | michaelfeil/mxbai-rerank-base-v2-seq |
| Infinity *only* supports seq-classification (stated too absolutely) | 1-2 | Infinity README |
| Ettin uses plain `AutoModel`+Pooling/Dense (not seq-classification) | 1-2 | Ettin Reranker blog |

> The first two were killed for overstatement, not because the underlying fact is
> false — Infinity *does* require seq-classification heads (finding #2) and the
> `-seq` variant *is* servable (finding #4); the killed phrasings overclaimed
> "recommended"/"only". The third leaves Ettin's exact head **unconfirmed** — the
> reason Ettin is `medium` confidence, not `high`.
