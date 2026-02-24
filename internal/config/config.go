package config

import "github.com/spf13/viper"

type Config struct {
	DocsDir          string          `mapstructure:"docs_dir"`
	DBPath           string          `mapstructure:"db_path"`
	Embedding        EmbeddingConfig `mapstructure:"embedding"`
	Chunking         ChunkingConfig  `mapstructure:"chunking"`
	CodeFilePatterns []string        `mapstructure:"code_file_patterns"`
	ExcludePatterns  []string        `mapstructure:"exclude_patterns"`
}

type EmbeddingConfig struct {
	Provider string `mapstructure:"provider"`
	Model    string `mapstructure:"model"`
	APIKey   string `mapstructure:"api_key"`
	BaseURL  string `mapstructure:"base_url"`
}

type ChunkingConfig struct {
	MaxTokens    int `mapstructure:"max_tokens"`
	OverlapLines int `mapstructure:"overlap_lines"`
	MinTokens    int `mapstructure:"min_tokens"`
}

func Load() *Config {
	cfg := &Config{
		DocsDir: "docs",
		DBPath:  ".librarian/librarian.db",
		Embedding: EmbeddingConfig{
			Provider: "gemini",
		},
		Chunking: ChunkingConfig{
			MaxTokens:    512,
			OverlapLines: 3,
			MinTokens:    50,
		},
		CodeFilePatterns: []string{"*.go", "*.ts", "*.py", "*.rs", "*.java", "*.rb"},
		ExcludePatterns:  []string{"node_modules/**", ".git/**", "vendor/**"},
	}

	viper.Unmarshal(cfg)

	if dbPath := viper.GetString("db_path"); dbPath != "" {
		cfg.DBPath = dbPath
	}

	// Fall back to default if no db_path was set via config, env, or flag
	if cfg.DBPath == "" {
		cfg.DBPath = ".librarian/librarian.db"
	}

	return cfg
}
