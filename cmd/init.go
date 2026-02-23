package cmd

import (
	"fmt"
	"hash/fnv"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"librarian/db"
)

var (
	initPort int
	initName string
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize Librarian and deploy HelixDB schema",
	RunE:  runInit,
}

func init() {
	initCmd.Flags().IntVar(&initPort, "port", 0, "HelixDB port (default: derived from project directory)")
	initCmd.Flags().StringVar(&initName, "name", "", "Project name (default: directory basename)")
	rootCmd.AddCommand(initCmd)
}

func runInit(cmd *cobra.Command, args []string) error {
	libDir := ".librarian"

	absDir, err := filepath.Abs(".")
	if err != nil {
		return fmt.Errorf("resolving working directory: %w", err)
	}

	projectName := initName
	if projectName == "" {
		projectName = filepath.Base(absDir)
	}

	port := initPort
	if port == 0 {
		port = derivePort(absDir)
	}

	// Create .librarian directory structure
	if err := os.MkdirAll(filepath.Join(libDir, "db"), 0755); err != nil {
		return fmt.Errorf("creating .librarian directory: %w", err)
	}

	// Write helix.toml with derived project name and port
	helixToml := fmt.Sprintf(`[project]
name = "%s"
queries = "./db/"

[local.dev]
port = %d
build_mode = "dev"
`, projectName, port)

	if err := os.WriteFile(filepath.Join(libDir, "helix.toml"), []byte(helixToml), 0644); err != nil {
		return fmt.Errorf("writing helix.toml: %w", err)
	}

	// Write schema.hx from embedded files
	schemaData, err := db.Files.ReadFile("schema.hx")
	if err != nil {
		return fmt.Errorf("reading embedded schema.hx: %w", err)
	}
	if err := os.WriteFile(filepath.Join(libDir, "db", "schema.hx"), schemaData, 0644); err != nil {
		return fmt.Errorf("writing schema.hx: %w", err)
	}

	// Write queries.hx from embedded files
	queriesData, err := db.Files.ReadFile("queries.hx")
	if err != nil {
		return fmt.Errorf("reading embedded queries.hx: %w", err)
	}
	if err := os.WriteFile(filepath.Join(libDir, "db", "queries.hx"), queriesData, 0644); err != nil {
		return fmt.Errorf("writing queries.hx: %w", err)
	}

	// Write helix_host to .librarian.yaml so all commands use the correct port
	helixHost := fmt.Sprintf("http://localhost:%d", port)
	if err := writeHelixHostToConfig(helixHost); err != nil {
		return fmt.Errorf("writing .librarian.yaml: %w", err)
	}

	fmt.Printf("Created .librarian/ directory (project: %s, port: %d)\n", projectName, port)

	// Deploy to HelixDB
	fmt.Println("Deploying schema to HelixDB...")
	deployCmd := exec.Command("helix", "push", "dev")
	deployCmd.Dir = libDir
	deployCmd.Stdout = os.Stdout
	deployCmd.Stderr = os.Stderr

	if err := deployCmd.Run(); err != nil {
		fmt.Println("\nFailed to deploy schema. Ensure the helix CLI is installed:")
		fmt.Println("  curl -sSL \"https://install.helix-db.com\" | bash")
		return fmt.Errorf("deploying to HelixDB: %w", err)
	}

	fmt.Println("Schema deployed successfully!")
	return nil
}

// derivePort produces a deterministic port from a directory path,
// mapped to the range 6970–7969.
func derivePort(dir string) int {
	h := fnv.New32a()
	h.Write([]byte(dir))
	return 6970 + int(h.Sum32()%1000)
}

// writeHelixHostToConfig creates or updates .librarian.yaml, setting
// only the helix_host field while preserving any other existing fields.
func writeHelixHostToConfig(helixHost string) error {
	configPath := ".librarian.yaml"

	existing := make(map[string]interface{})
	data, err := os.ReadFile(configPath)
	if err == nil {
		yaml.Unmarshal(data, &existing)
	}

	existing["helix_host"] = helixHost

	out, err := yaml.Marshal(existing)
	if err != nil {
		return err
	}
	return os.WriteFile(configPath, out, 0644)
}
