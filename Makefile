.PHONY: build install clean test \
        infinity-setup infinity-start infinity-stop infinity-restart infinity-status infinity-logs infinity-smoke

BINARY_NAME=librarian

# Infinity venv lives under XDG_DATA_HOME so it survives repo clones /
# branch switches and stays out of the working tree. scripts/infinity.sh
# references the same path.
INFINITY_DATA_DIR ?= $(HOME)/.local/share/librarian/infinity
INFINITY_VENV = $(INFINITY_DATA_DIR)/.venv

build:
	go build -o $(BINARY_NAME) .

install:
	go install .

clean:
	rm -f $(BINARY_NAME)

test:
	go test ./...

# Local Infinity server for Qwen3-Embedding + Qwen3-Reranker.
# First-time setup creates an isolated Python venv and installs infinity-emb.
# Subsequent starts are ~instant (plus ~30-60s of first-run HF model download).
# See docs/configuration.md § "Local embedding + rerank via Infinity".

infinity-setup:
	@command -v uv >/dev/null 2>&1 || { echo "uv not found; install with: brew install uv"; exit 1; }
	mkdir -p $(INFINITY_DATA_DIR)
	uv venv --python 3.12 $(INFINITY_VENV)
	uv pip install --python $(INFINITY_VENV)/bin/python 'infinity-emb[all]'
	# Dependency fix-ups for the current infinity-emb 0.0.77 pin:
	#   1) optimum 2.x removed bettertransformer, and bettertransformer<2.0 requires
	#      transformers<4.49 which doesn't know Qwen3. Uninstall optimum entirely so
	#      infinity's soft-import block is skipped; scripts/infinity.sh passes
	#      --no-bettertransformer to keep the runtime path off the broken name.
	#   2) click 8.3+ breaks typer 0.12.x's "secondary flag" handling. Pin click<8.2.
	# Remove these once infinity-emb publishes a release compatible with the current
	# optimum / click / transformers stack.
	-uv pip uninstall --python $(INFINITY_VENV)/bin/python optimum optimum-onnx 2>/dev/null
	uv pip install --python $(INFINITY_VENV)/bin/python 'click<8.2'
	@echo ""
	@echo "Infinity installed at $(INFINITY_VENV)"
	@echo "Next: make infinity-start"

infinity-start:
	@scripts/infinity.sh start

infinity-stop:
	@scripts/infinity.sh stop

infinity-restart:
	@scripts/infinity.sh restart

infinity-status:
	@scripts/infinity.sh status

infinity-logs:
	@scripts/infinity.sh logs

infinity-smoke:
	@scripts/infinity.sh smoke
