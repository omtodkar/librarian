#!/usr/bin/env bash
# infinity.sh — manage a local Infinity server for embedding + rerank.
#
# Defaults to Qwen3-Embedding-0.6B (1024-dim, top-MTEB) for the embedder and
# gte-reranker-modernbert-base (149M params, Apache 2.0, strong code-retrieval
# quality, Infinity-native auto-detect) for the reranker. Override either via
# INFINITY_EMBED_MODEL / INFINITY_RERANK_MODEL.
#
# Runs a single Infinity process serving both via /embeddings and /rerank
# (Infinity uses those paths directly — no /v1/ prefix). Works on Mac (MPS),
# NVIDIA Linux (CUDA), and CPU fallback. No Docker required — Infinity is a
# Python package, installed into an isolated venv at
# ~/.local/share/librarian/infinity/.venv by `make infinity-setup`.
#
# Reranker note: Qwen3-Reranker-0.6B is tempting but incompatible with
# Infinity — its Qwen3ForCausalLM architecture isn't auto-detected as a
# cross-encoder; Infinity loads it as an embedder with mean pooling, and
# /rerank refuses it. gte-reranker-modernbert-base sidesteps this with a
# real AutoModelForSequenceClassification head and scores better on code.
#
# See docs/configuration.md § "Local embedding + rerank via Infinity" for the
# full end-to-end flow (install → start → point librarian at it → reindex).

set -euo pipefail

# ---- Configuration (overridable via env) --------------------------------

PORT="${INFINITY_PORT:-7997}"
EMBED_MODEL="${INFINITY_EMBED_MODEL:-Qwen/Qwen3-Embedding-0.6B}"
RERANK_MODEL="${INFINITY_RERANK_MODEL:-Alibaba-NLP/gte-reranker-modernbert-base}"
ENGINE="${INFINITY_ENGINE:-torch}"

# Pick sensible device default per platform. User can override with
# INFINITY_DEVICE=cpu to force CPU even on accelerator-capable hosts.
if [[ -z "${INFINITY_DEVICE:-}" ]]; then
    case "$(uname -s)" in
        Darwin) INFINITY_DEVICE="mps" ;;
        Linux)
            if command -v nvidia-smi >/dev/null 2>&1; then
                INFINITY_DEVICE="cuda"
            else
                INFINITY_DEVICE="cpu"
            fi
            ;;
        *) INFINITY_DEVICE="cpu" ;;
    esac
fi
DEVICE="$INFINITY_DEVICE"

STATE_DIR="${XDG_STATE_HOME:-$HOME/.local/state}/librarian/infinity"
DATA_DIR="${XDG_DATA_HOME:-$HOME/.local/share}/librarian/infinity"
VENV_BIN="$DATA_DIR/.venv/bin/infinity_emb"
PIDFILE="$STATE_DIR/infinity.pid"
LOGFILE="$STATE_DIR/infinity.log"

# ---- Helpers ------------------------------------------------------------

usage() {
    cat <<EOF
Usage: $0 <command>

Commands:
  start     Start Infinity serving both models on port $PORT
  stop      Stop the running Infinity server
  restart   stop + start
  status    Show pid, port, and loaded models via /v1/models
  logs      Tail the server log
  smoke     Hit both /v1/embeddings and /v1/rerank to verify

Environment overrides:
  INFINITY_PORT          default $PORT
  INFINITY_EMBED_MODEL   default Qwen/Qwen3-Embedding-0.6B
  INFINITY_RERANK_MODEL  default Alibaba-NLP/gte-reranker-modernbert-base
  INFINITY_ENGINE        default torch
  INFINITY_DEVICE        default mps (Mac) / cuda (NVIDIA Linux) / cpu

Paths:
  venv binary: $VENV_BIN
  pidfile:     $PIDFILE
  logfile:     $LOGFILE
EOF
}

ensure_dirs() {
    mkdir -p "$STATE_DIR" "$DATA_DIR"
}

is_running() {
    [[ -f "$PIDFILE" ]] && kill -0 "$(cat "$PIDFILE")" 2>/dev/null
}

require_venv() {
    if [[ ! -x "$VENV_BIN" ]]; then
        cat <<EOF >&2
infinity_emb not found at $VENV_BIN

Install it with:
    make infinity-setup

Or manually:
    mkdir -p $DATA_DIR
    uv venv --python 3.12 $DATA_DIR/.venv
    uv pip install --python $DATA_DIR/.venv/bin/python 'infinity-emb[all]'
EOF
        return 1
    fi
}

# ---- Commands -----------------------------------------------------------

cmd_start() {
    ensure_dirs
    require_venv
    if is_running; then
        echo "Infinity already running (pid $(cat "$PIDFILE"), port $PORT)"
        return 0
    fi
    echo "Starting Infinity: port=$PORT engine=$ENGINE device=$DEVICE"
    echo "  embed:  $EMBED_MODEL"
    echo "  rerank: $RERANK_MODEL"
    # --no-bettertransformer: bettertransformer needs transformers<4.49 which
    # is too old for Qwen3. We uninstall optimum in `make infinity-setup` to
    # dodge the import chain, and disable the feature at the CLI to avoid the
    # NameError when check_if_bettertransformer_possible runs. MPS doesn't use
    # bettertransformer anyway (the engine short-circuits on MPS), so this
    # flag is effectively a no-op on Mac but required on Linux/CUDA for the
    # same optimum-free setup.
    nohup "$VENV_BIN" v2 \
        --model-id "$EMBED_MODEL" \
        --model-id "$RERANK_MODEL" \
        --engine "$ENGINE" \
        --device "$DEVICE" \
        --no-bettertransformer \
        --port "$PORT" \
        >"$LOGFILE" 2>&1 &
    echo $! >"$PIDFILE"
    echo "PID $(cat "$PIDFILE") — logs: $LOGFILE"
    echo "First run downloads ~1.2 GB of model weights from HuggingFace; ~30-60s warmup."
    echo "Tail logs: $0 logs    |    Smoke-test: $0 smoke"
}

cmd_stop() {
    if ! is_running; then
        echo "Not running."
        rm -f "$PIDFILE"
        return 0
    fi
    local pid
    pid="$(cat "$PIDFILE")"
    echo "Stopping pid $pid..."
    kill "$pid" 2>/dev/null || true
    # Wait up to 10s for graceful shutdown.
    for _ in {1..20}; do
        kill -0 "$pid" 2>/dev/null || break
        sleep 0.5
    done
    kill -9 "$pid" 2>/dev/null || true
    rm -f "$PIDFILE"
    echo "Stopped."
}

cmd_status() {
    if is_running; then
        echo "Running: pid $(cat "$PIDFILE"), port $PORT"
        echo ""
        echo "Loaded models (/v1/models):"
        curl -sf "http://127.0.0.1:$PORT/v1/models" 2>/dev/null |
            python3 -m json.tool 2>/dev/null ||
            echo "  (endpoint not yet responding — still warming up or failed; check: $0 logs)"
    else
        echo "Not running."
    fi
}

cmd_logs() {
    if [[ ! -f "$LOGFILE" ]]; then
        echo "No log file at $LOGFILE"
        return 1
    fi
    tail -f "$LOGFILE"
}

cmd_smoke() {
    if ! is_running; then
        echo "Not running — start first with: $0 start" >&2
        return 1
    fi
    # Note: Infinity serves /embeddings and /rerank WITHOUT the /v1/ prefix
    # OpenAI uses — this is a deliberate divergence. Librarian's openai
    # provider works by setting base_url=http://127.0.0.1:$PORT (no /v1).
    echo "== /embeddings =="
    curl -sf "http://127.0.0.1:$PORT/embeddings" \
        -H "Content-Type: application/json" \
        -d "{\"model\": \"$EMBED_MODEL\", \"input\": \"semantic search for documentation\"}" |
        python3 -c "import sys,json; r=json.load(sys.stdin); print('dim =', len(r['data'][0]['embedding']))" ||
        echo "  embedding endpoint FAILED — check $0 logs"
    echo ""
    echo "== /rerank =="
    curl -sf "http://127.0.0.1:$PORT/rerank" \
        -H "Content-Type: application/json" \
        -d "{\"model\": \"$RERANK_MODEL\", \"query\": \"what is sqlite-vec\", \"documents\": [\"sqlite-vec is a SQLite extension that provides vector search for embeddings.\", \"Paris is the capital of France.\"]}" |
        python3 -m json.tool ||
        echo "  rerank endpoint FAILED — likely a model-detection issue (see docs/configuration.md § Infinity rerank notes)"
}

# ---- Dispatch -----------------------------------------------------------

cmd="${1:-}"
case "$cmd" in
    start) cmd_start ;;
    stop) cmd_stop ;;
    restart)
        cmd_stop
        cmd_start
        ;;
    status) cmd_status ;;
    logs) cmd_logs ;;
    smoke) cmd_smoke ;;
    *)
        usage
        exit 1
        ;;
esac
