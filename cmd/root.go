package cmd

import (
	"os"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"librarian/internal/config"
	"librarian/internal/indexer"
	"librarian/internal/indexer/handlers/office"
	"librarian/internal/indexer/handlers/pdf"
	"librarian/internal/workspace"
)

var cfg *config.Config

var rootCmd = &cobra.Command{
	Use:   "librarian",
	Short: "Semantic documentation search, project-local workspace",
	Long:  "Librarian indexes project documentation and code into a searchable vector + graph store, exposed via CLI and an optional MCP server.",
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func init() {
	cobra.OnInitialize(initConfig)
	rootCmd.PersistentFlags().String("config", "", "explicit config file path (default: discovered via .librarian/)")
	rootCmd.PersistentFlags().String("db-path", "", "override SQLite database path")
	viper.BindPFlag("db_path", rootCmd.PersistentFlags().Lookup("db-path"))
}

func initConfig() {
	cfgFile, _ := rootCmd.PersistentFlags().GetString("config")

	// Workspace discovery — walk up from CWD looking for .librarian/. Not finding one
	// isn't fatal; 'librarian init' runs before a workspace exists.
	var ws *workspace.Workspace
	if cwd, err := os.Getwd(); err == nil {
		ws, _ = workspace.Find(cwd)
	}

	switch {
	case cfgFile != "":
		viper.SetConfigFile(cfgFile)
	case ws != nil:
		viper.SetConfigFile(ws.ConfigPath())
	}

	viper.SetEnvPrefix("LIBRARIAN")
	viper.AutomaticEnv()
	viper.ReadInConfig() // silently ignore if config file not found

	cfg = config.Load()

	// Normalize DB path to the discovered workspace unless the user explicitly
	// overrode it via flag or config. Makes commands work from any subdirectory.
	if ws != nil && !rootCmd.PersistentFlags().Changed("db-path") && !viper.IsSet("db_path") {
		cfg.DBPath = ws.DBPath()
	}

	// Propagate Office-handler config so the DOCX/XLSX/PPTX handlers pick up
	// user-specified row/col caps and speaker-notes preferences. The office
	// package auto-registers its handlers at init with DefaultConfig; we
	// replace each registration with a fresh handler whose Config reflects
	// the loaded workspace config. Registry.Register is last-writer-wins by
	// extension, so overwriting is safe. Runs inside cobra.OnInitialize so
	// every subcommand (index, mcp serve, report, …) sees the user config.
	officeCfg := office.Config{
		XLSXMaxRows:         cfg.Office.XLSXMaxRows,
		XLSXMaxCols:         cfg.Office.XLSXMaxCols,
		IncludeSpeakerNotes: cfg.Office.IncludeSpeakerNotes,
	}
	indexer.RegisterDefault(office.NewDocx(officeCfg))
	indexer.RegisterDefault(office.NewXlsx(officeCfg))
	indexer.RegisterDefault(office.NewPptx(officeCfg))

	// PDF handler: same pattern — init-time default gets overwritten with
	// user-configured MaxPages cap.
	indexer.RegisterDefault(pdf.NewPDF(pdf.Config{MaxPages: cfg.PDF.MaxPages}))
}
