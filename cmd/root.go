package cmd

import (
	"os"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"librarian/internal/config"
)

var cfg *config.Config

var rootCmd = &cobra.Command{
	Use:   "librarian",
	Short: "Semantic documentation search via MCP",
	Long:  "Librarian indexes project documentation into a searchable vector + graph database and exposes it via MCP.",
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func init() {
	cobra.OnInitialize(initConfig)
	rootCmd.PersistentFlags().String("config", "", "config file (default is .librarian.yaml)")
	rootCmd.PersistentFlags().String("helix-host", "", "HelixDB host URL")
	viper.BindPFlag("helix_host", rootCmd.PersistentFlags().Lookup("helix-host"))
}

func initConfig() {
	cfgFile, _ := rootCmd.PersistentFlags().GetString("config")
	if cfgFile != "" {
		viper.SetConfigFile(cfgFile)
	} else {
		viper.SetConfigName(".librarian")
		viper.SetConfigType("yaml")
		viper.AddConfigPath(".")
	}

	viper.SetEnvPrefix("LIBRARIAN")
	viper.AutomaticEnv()
	viper.ReadInConfig() // silently ignore if config file not found

	cfg = config.Load()
}
