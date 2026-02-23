package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"

	"librarian/db"
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize Librarian and deploy HelixDB schema",
	RunE:  runInit,
}

func init() {
	rootCmd.AddCommand(initCmd)
}

func runInit(cmd *cobra.Command, args []string) error {
	libDir := ".librarian"

	// Create .librarian directory structure
	if err := os.MkdirAll(filepath.Join(libDir, "db"), 0755); err != nil {
		return fmt.Errorf("creating .librarian directory: %w", err)
	}

	// Write helix.toml
	helixToml := `[project]
name = "librarian"
queries = "./db/"

[local.dev]
port = 6969
build_mode = "dev"
`
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

	fmt.Println("Created .librarian/ directory with HelixDB configuration")

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
