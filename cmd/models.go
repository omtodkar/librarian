package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"librarian/internal/models"
)

var (
	modelsJSON      bool
	modelsPullForce bool
)

var modelsCmd = &cobra.Command{
	Use:   "models",
	Short: "Manage local model weights (rerankers, ONNX runtime)",
	Long: `Download and inspect the model artifacts used by the in-process onnx
rerank provider. Weights and the ONNX Runtime library live in a shared cache
outside any workspace ($XDG_DATA_HOME/librarian/models, overridable with
LIBRARIAN_MODELS_DIR), so a single pull serves every project on the machine.`,
}

var modelsPullCmd = &cobra.Command{
	Use:   "pull [model-id]",
	Short: "Download a reranker model + ONNX runtime into the shared cache",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runModelsPull,
}

var modelsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List known models and whether they are pulled",
	RunE:  runModelsList,
}

var modelsPathCmd = &cobra.Command{
	Use:   "path [model-id]",
	Short: "Print the resolved cache paths for a model",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runModelsPath,
}

func init() {
	modelsPullCmd.Flags().BoolVar(&modelsJSON, "json", false, "Output as JSON")
	modelsPullCmd.Flags().BoolVar(&modelsPullForce, "force", false, "Re-download even if already cached")
	modelsListCmd.Flags().BoolVar(&modelsJSON, "json", false, "Output as JSON")
	modelsPathCmd.Flags().BoolVar(&modelsJSON, "json", false, "Output as JSON")

	modelsCmd.AddCommand(modelsPullCmd, modelsListCmd, modelsPathCmd)
	rootCmd.AddCommand(modelsCmd)
}

// resolveModelID picks the model id from the positional arg, else the
// configured onnx rerank model, else the registry default. Works without a
// workspace (cfg may be zero-valued).
func resolveModelID(args []string) string {
	if len(args) == 1 && args[0] != "" {
		return args[0]
	}
	if cfg != nil && cfg.Rerank.Provider == "onnx" && cfg.Rerank.Model != "" {
		return cfg.Rerank.Model
	}
	return models.DefaultRerankerID()
}

func runModelsPull(cmd *cobra.Command, args []string) error {
	id := resolveModelID(args)

	// In JSON mode, progress text must not pollute stdout.
	progress := io.Writer(os.Stderr)
	if modelsJSON {
		progress = io.Discard
	}

	res, err := models.Pull(cmd.Context(), id, models.PullOptions{
		Force:    modelsPullForce,
		Progress: progress,
	})
	if err != nil {
		return err
	}

	if modelsJSON {
		out, _ := json.MarshalIndent(res, "", "  ")
		fmt.Println(string(out))
	}
	return nil
}

func runModelsList(cmd *cobra.Command, args []string) error {
	type row struct {
		ID       string `json:"id"`
		Pulled   bool   `json:"pulled"`
		ModelDir string `json:"model_dir"`
	}
	var rows []row
	for _, m := range models.List() {
		dir, _ := models.ModelDir(m.ID)
		rows = append(rows, row{ID: m.ID, Pulled: models.IsPulled(m.ID), ModelDir: dir})
	}

	if modelsJSON {
		out, _ := json.MarshalIndent(rows, "", "  ")
		fmt.Println(string(out))
		return nil
	}

	fmt.Println("Models:")
	for _, r := range rows {
		state := "not pulled"
		if r.Pulled {
			state = "pulled"
		}
		fmt.Printf("  %-32s %-12s %s\n", r.ID, state, r.ModelDir)
	}
	return nil
}

func runModelsPath(cmd *cobra.Command, args []string) error {
	id := resolveModelID(args)
	if _, ok := models.Lookup(id); !ok {
		return fmt.Errorf("unknown model %q", id)
	}
	modelDir, err := models.ModelDir(id)
	if err != nil {
		return err
	}
	// ORT path may be unavailable on unsupported platforms; surface that
	// without failing the command.
	ortLib, ortErr := models.ORTLibPath(id)

	if modelsJSON {
		m := map[string]any{"id": id, "model_dir": modelDir, "pulled": models.IsPulled(id)}
		if ortErr == nil {
			m["ort_lib"] = ortLib
		} else {
			m["ort_lib_error"] = ortErr.Error()
		}
		out, _ := json.MarshalIndent(m, "", "  ")
		fmt.Println(string(out))
		return nil
	}

	fmt.Printf("model:    %s\n", id)
	fmt.Printf("dir:      %s\n", modelDir)
	if ortErr == nil {
		fmt.Printf("ort lib:  %s\n", ortLib)
	} else {
		fmt.Printf("ort lib:  (unavailable: %v)\n", ortErr)
	}
	fmt.Printf("pulled:   %v\n", models.IsPulled(id))
	return nil
}
