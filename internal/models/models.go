package models

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
)

// pulledMarker is written into a model/ORT directory after every required file
// has downloaded and passed sha256 verification. Its presence lets IsPulled be
// a single stat instead of re-hashing hundreds of MB.
const pulledMarker = ".pulled"

// FileSpec describes one downloadable artifact and how to verify/extract it.
type FileSpec struct {
	// Name is the on-disk filename the artifact is stored as.
	Name string
	// URL is the direct download URL.
	URL string
	// SHA256 is the expected lowercase hex digest of the downloaded bytes
	// (the archive bytes when Archive != "").
	SHA256 string
	// Archive, when non-empty ("tgz"|"zip"), means URL points to an archive
	// and MemberPath inside it is extracted to Name.
	Archive string
	// MemberPath is the path within the archive to extract (archives only).
	MemberPath string
}

// ORTSpec pins one ONNX Runtime release and its per-platform shared libraries,
// keyed by "<GOOS>-<GOARCH>" (e.g. "darwin-arm64", "linux-amd64").
type ORTSpec struct {
	Version string
	Libs    map[string]FileSpec
}

// Model is a registry entry: the artifacts the onnx reranker needs plus the
// ONNX I/O contract the reranker relies on.
type Model struct {
	// ID is the short registry key used in config (rerank.model).
	ID string
	// Files are the model artifacts (ONNX weights + tokenizer + config).
	Files []FileSpec
	// ORT is the ONNX Runtime bundle this model is served with.
	ORT ORTSpec
	// MaxSeqLen caps cross-encoder tokenization (the model supports 8192, but
	// reranking a handful of candidates is capped far lower for latency).
	MaxSeqLen int
	// InputNames / OutputName are the ONNX graph tensor names.
	InputNames []string
	OutputName string
}

// defaultRerankerID is the registry key for the shipped default reranker.
const defaultRerankerID = "gte-reranker-modernbert-base"

// ortV1_26 is the ONNX Runtime release the registry pins. Verified against
// github.com/yalue/onnxruntime_go v1.30.1 in spike lib-1snh. Intel macOS
// (darwin-amd64) is intentionally absent — ORT 1.26 ships no osx-x86_64 build.
var ortV1_26 = ORTSpec{
	Version: "1.26.0",
	Libs: map[string]FileSpec{
		"darwin-arm64": {
			Name:       "libonnxruntime.dylib",
			URL:        "https://github.com/microsoft/onnxruntime/releases/download/v1.26.0/onnxruntime-osx-arm64-1.26.0.tgz",
			SHA256:     "7a1280bbb1701ea514f71828765237e7896e0f2e1cd332f1f70dbd5c3e33aca3",
			Archive:    "tgz",
			MemberPath: "onnxruntime-osx-arm64-1.26.0/lib/libonnxruntime.1.26.0.dylib",
		},
		"linux-amd64": {
			Name:       "libonnxruntime.so",
			URL:        "https://github.com/microsoft/onnxruntime/releases/download/v1.26.0/onnxruntime-linux-x64-1.26.0.tgz",
			SHA256:     "1254da24fb389cf39dc0ff3451ab48301740ffbfcbaf646849df92f80ee92c57",
			Archive:    "tgz",
			MemberPath: "onnxruntime-linux-x64-1.26.0/lib/libonnxruntime.so.1.26.0",
		},
		"linux-arm64": {
			Name:       "libonnxruntime.so",
			URL:        "https://github.com/microsoft/onnxruntime/releases/download/v1.26.0/onnxruntime-linux-aarch64-1.26.0.tgz",
			SHA256:     "34ff1c2d0f12e2cf3d33a0c5f82e39792e1d581fbd6968fd7c30d173654be01a",
			Archive:    "tgz",
			MemberPath: "onnxruntime-linux-aarch64-1.26.0/lib/libonnxruntime.so.1.26.0",
		},
		"windows-amd64": {
			Name:       "onnxruntime.dll",
			URL:        "https://github.com/microsoft/onnxruntime/releases/download/v1.26.0/onnxruntime-win-x64-1.26.0.zip",
			SHA256:     "6ebe99b5564bf4d029b6e93eac9ff423682b6212eade769e9ca3f685eaf500b4",
			Archive:    "zip",
			MemberPath: "onnxruntime-win-x64-1.26.0/lib/onnxruntime.dll",
		},
	},
}

// hfBase builds a HuggingFace resolve URL for a file in a model repo.
func hfBase(repo, path string) string {
	return "https://huggingface.co/" + repo + "/resolve/main/" + path
}

// registry holds every known model. Values verified in spike lib-gh5h.
var registry = map[string]Model{
	defaultRerankerID: {
		ID: defaultRerankerID,
		Files: []FileSpec{
			{
				Name:   "model.onnx",
				URL:    hfBase("Alibaba-NLP/gte-reranker-modernbert-base", "onnx/model_fp16.onnx"),
				SHA256: "18acede2913b05211687cfd627d8b3d85bbb91ad36b44252263322760eb68d7b",
			},
			{
				Name:   "tokenizer.json",
				URL:    hfBase("Alibaba-NLP/gte-reranker-modernbert-base", "tokenizer.json"),
				SHA256: "2aea6ff4701d063e7e029b6be695a1659f2caaa2ae4fb0e8b18285818271becd",
			},
			{
				Name:   "tokenizer_config.json",
				URL:    hfBase("Alibaba-NLP/gte-reranker-modernbert-base", "tokenizer_config.json"),
				SHA256: "626c86908d7c711f93b0feffd8657b782cc2727b391f9be190240e8cafb626d5",
			},
			{
				Name:   "config.json",
				URL:    hfBase("Alibaba-NLP/gte-reranker-modernbert-base", "config.json"),
				SHA256: "c9316ff715158502dad782f35454eee18de984160618dc30afe7508feb46b7ce",
			},
		},
		ORT:        ortV1_26,
		MaxSeqLen:  512,
		InputNames: []string{"input_ids", "attention_mask"},
		OutputName: "logits",
	},
}

// DefaultRerankerID returns the registry key of the shipped default reranker.
func DefaultRerankerID() string { return defaultRerankerID }

// Lookup returns the registry entry for id.
func Lookup(id string) (Model, bool) {
	m, ok := registry[id]
	return m, ok
}

// List returns all registered models sorted by ID.
func List() []Model {
	out := make([]Model, 0, len(registry))
	for _, m := range registry {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// platformKey is the "<GOOS>-<GOARCH>" key used to select an ORT lib.
func platformKey() string { return runtime.GOOS + "-" + runtime.GOARCH }

// ModelDir returns the directory holding a model's artifacts:
// <DataDir>/<id>.
func ModelDir(id string) (string, error) {
	base, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, id), nil
}

// ModelFilePath returns the on-disk path of a named artifact for a model.
func ModelFilePath(id, name string) (string, error) {
	dir, err := ModelDir(id)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
}

// ortDir returns the directory holding the ORT lib for a model's pinned
// version and the current platform: <DataDir>/onnxruntime/<ver>/<os>-<arch>.
func ortDir(m Model) (string, error) {
	base, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "onnxruntime", m.ORT.Version, platformKey()), nil
}

// ORTLibPath returns the resolved shared-library path for a model on the
// current platform, or an error when the platform has no pinned ORT build
// (e.g. Intel macOS, for which ORT 1.26 ships no archive).
func ORTLibPath(id string) (string, error) {
	m, ok := Lookup(id)
	if !ok {
		return "", fmt.Errorf("unknown model %q", id)
	}
	spec, ok := m.ORT.Libs[platformKey()]
	if !ok {
		return "", fmt.Errorf("no ONNX Runtime build for platform %q (model %q); the in-process onnx reranker is unsupported here — use the openai/Infinity rerank provider instead", platformKey(), id)
	}
	dir, err := ortDir(m)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, spec.Name), nil
}

// IsPulled reports whether all of a model's artifacts AND its platform ORT lib
// have been fully downloaded (both .pulled sentinels present).
func IsPulled(id string) bool {
	m, ok := Lookup(id)
	if !ok {
		return false
	}
	if _, ok := m.ORT.Libs[platformKey()]; !ok {
		return false // platform has no ORT build → can never be "pulled" here
	}
	mdir, err := ModelDir(id)
	if err != nil {
		return false
	}
	odir, err := ortDir(m)
	if err != nil {
		return false
	}
	return fileExists(filepath.Join(mdir, pulledMarker)) &&
		fileExists(filepath.Join(odir, pulledMarker))
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
