package embedding

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"librarian/internal/models"

	"github.com/sugarme/tokenizer"
	"github.com/sugarme/tokenizer/pretrained"
	ort "github.com/yalue/onnxruntime_go"
)

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// ortInit guards process-global ONNX Runtime initialization. SetSharedLibraryPath
// + InitializeEnvironment must run exactly once per process; search/context/mcp
// each construct a reranker, and the MCP server may build several. One ORT
// shared library per process is supported (a second model needing a different
// ORT build in the same process is out of scope for v1).
var (
	ortInit    sync.Once
	ortInitErr error
	ortLibPath string // the lib path the environment was initialized with
)

func initORT(libPath string) error {
	ortInit.Do(func() {
		ortLibPath = libPath
		ort.SetSharedLibraryPath(libPath)
		ortInitErr = ort.InitializeEnvironment()
	})
	if ortInitErr == nil && ortLibPath != libPath {
		// All onnx rerankers in a process must share one ORT lib. Surface a
		// clear error rather than silently using the first-initialized lib.
		return fmt.Errorf("onnx runtime already initialized with %q; cannot also use %q in the same process", ortLibPath, libPath)
	}
	return ortInitErr
}

// OnnxReranker is an in-process cross-encoder reranker backed by ONNX Runtime
// (dlopened at runtime) and a pure-Go tokenizer. It implements Reranker.
//
// Weights, tokenizer, and the ORT shared library are resolved from the shared
// models cache (internal/models); they are NOT downloaded on demand — a missing
// model yields an actionable error telling the user to run `librarian models pull`.
type OnnxReranker struct {
	modelID string
	maxLen  int
	inNames []string
	outName string
	tk      *tokenizer.Tokenizer

	mu      sync.Mutex // serializes session.Run (ORT session is not goroutine-safe)
	session *ort.DynamicAdvancedSession
}

// NewOnnxReranker constructs a reranker for the given registry model id.
// modelPath, when non-empty, overrides the model-artifact directory (the ORT
// library is still taken from the shared cache). timeoutMs is currently
// advisory only — ORT inference is not mid-call cancellable; the small candidate
// batch keeps latency bounded.
func NewOnnxReranker(modelID, modelPath string, timeoutMs int) (*OnnxReranker, error) {
	m, ok := models.Lookup(modelID)
	if !ok {
		return nil, fmt.Errorf("unknown rerank model %q; run `librarian models list` to see available models", modelID)
	}

	modelDir := modelPath
	if modelDir == "" {
		dir, err := models.ModelDir(modelID)
		if err != nil {
			return nil, err
		}
		modelDir = dir
	}
	onnxPath := filepath.Join(modelDir, "model.onnx")
	tokPath := filepath.Join(modelDir, "tokenizer.json")

	libPath, err := models.ORTLibPath(modelID)
	if err != nil {
		return nil, err
	}

	for _, p := range []string{onnxPath, tokPath, libPath} {
		if !fileExists(p) {
			return nil, fmt.Errorf("reranker model %q not found (missing %s); run `librarian models pull %s`", modelID, p, modelID)
		}
	}

	if err := initORT(libPath); err != nil {
		return nil, fmt.Errorf("initializing onnx runtime: %w", err)
	}

	tk, err := pretrained.FromFile(tokPath)
	if err != nil {
		return nil, fmt.Errorf("loading tokenizer %s: %w", tokPath, err)
	}
	// The HF tokenizer.json bakes in pad-to-max_length; disable it (we pad per
	// batch ourselves) and cap truncation to the reranker window.
	tk.WithPadding(nil)
	tk.WithTruncation(&tokenizer.TruncationParams{
		MaxLength: m.MaxSeqLen,
		Strategy:  tokenizer.LongestFirst,
		Stride:    0,
	})

	// fp16 ModernBERT crashes under ORT's extended graph fusions
	// (SimplifiedLayerNormFusion); basic optimization is required. Verified in
	// spike lib-1snh.
	opts, err := ort.NewSessionOptions()
	if err != nil {
		return nil, fmt.Errorf("onnx session options: %w", err)
	}
	defer opts.Destroy()
	if err := opts.SetGraphOptimizationLevel(ort.GraphOptimizationLevelEnableBasic); err != nil {
		return nil, fmt.Errorf("setting graph optimization level: %w", err)
	}

	session, err := ort.NewDynamicAdvancedSession(onnxPath, m.InputNames, []string{m.OutputName}, opts)
	if err != nil {
		return nil, fmt.Errorf("creating onnx session for %s: %w", modelID, err)
	}

	return &OnnxReranker{
		modelID: modelID,
		maxLen:  m.MaxSeqLen,
		inNames: m.InputNames,
		outName: m.OutputName,
		tk:      tk,
		session: session,
	}, nil
}

// Model returns the registry model id.
func (r *OnnxReranker) Model() string { return r.modelID }

// Close releases the ONNX session.
func (r *OnnxReranker) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.session != nil {
		r.session.Destroy()
		r.session = nil
	}
	return nil
}

// Rerank scores each (query, document) pair with the cross-encoder and returns
// one relevance logit per document, in input order.
func (r *OnnxReranker) Rerank(query string, documents []string) ([]float64, error) {
	if len(documents) == 0 {
		return nil, nil
	}

	// Tokenize each pair, tracking the longest sequence to pad the batch to.
	batch := len(documents)
	idsRows := make([][]int64, batch)
	maskRows := make([][]int64, batch)
	maxLen := 0
	for i, doc := range documents {
		enc, err := r.tk.EncodePair(query, doc, true)
		if err != nil {
			return nil, fmt.Errorf("tokenizing pair %d: %w", i, err)
		}
		rawIDs := enc.GetIds()
		rawMask := enc.GetAttentionMask()
		ids := make([]int64, len(rawIDs))
		mask := make([]int64, len(rawMask))
		for j := range rawIDs {
			ids[j] = int64(rawIDs[j])
			mask[j] = int64(rawMask[j])
		}
		idsRows[i] = ids
		maskRows[i] = mask
		if len(ids) > maxLen {
			maxLen = len(ids)
		}
	}

	// Flatten into [batch, maxLen] tensors, right-padding with id 0 / mask 0.
	// Masked positions are ignored by attention, so the pad id value is moot.
	flatIDs := make([]int64, batch*maxLen)
	flatMask := make([]int64, batch*maxLen)
	for i := 0; i < batch; i++ {
		copy(flatIDs[i*maxLen:], idsRows[i])
		copy(flatMask[i*maxLen:], maskRows[i])
	}

	shape := ort.NewShape(int64(batch), int64(maxLen))
	idsT, err := ort.NewTensor(shape, flatIDs)
	if err != nil {
		return nil, fmt.Errorf("creating input_ids tensor: %w", err)
	}
	defer idsT.Destroy()
	maskT, err := ort.NewTensor(shape, flatMask)
	if err != nil {
		return nil, fmt.Errorf("creating attention_mask tensor: %w", err)
	}
	defer maskT.Destroy()

	// fp16 model emits float32 logits at the graph boundary (verified spike
	// lib-1snh). Output shape is [batch, 1].
	outT, err := ort.NewEmptyTensor[float32](ort.NewShape(int64(batch), 1))
	if err != nil {
		return nil, fmt.Errorf("creating logits tensor: %w", err)
	}
	defer outT.Destroy()

	r.mu.Lock()
	err = r.session.Run([]ort.Value{idsT, maskT}, []ort.Value{outT})
	r.mu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("onnx inference: %w", err)
	}

	data := outT.GetData()
	if len(data) != batch {
		return nil, fmt.Errorf("onnx reranker returned %d scores for %d documents", len(data), batch)
	}
	scores := make([]float64, batch)
	for i, v := range data {
		scores[i] = float64(v)
	}
	return scores, nil
}
