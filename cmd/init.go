package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"librarian/internal/store"
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize Librarian database",
	RunE:  runInit,
}

func init() {
	rootCmd.AddCommand(initCmd)
}

func runInit(cmd *cobra.Command, args []string) error {
	s, err := store.Open(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("initializing database: %w", err)
	}
	defer s.Close()

	fmt.Printf("Initialized Librarian database at %s\n", cfg.DBPath)
	return nil
}
