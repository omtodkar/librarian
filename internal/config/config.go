package config

import "github.com/spf13/viper"

type Config struct {
	DocsDir          string          `mapstructure:"docs_dir"`
	HelixHost        string          `mapstructure:"helix_host"`
	Embedding        EmbeddingConfig `mapstructure:"embedding"`
	Chunking         ChunkingConfig  `mapstructure:"chunking"`
	CodeFilePatterns []string        `mapstructure:"code_file_patterns"`
	ExcludePatterns  []string        `mapstructure:"exclude_patterns"`
}

type EmbeddingConfig struct {
	Provider string `mapstructure:"provider"`
	Model    string `mapstructure:"model"`
	APIKey   string `mapstructure:"api_key"`
}

type ChunkingConfig struct {
	MaxTokens    int `mapstructure:"max_tokens"`
	OverlapLines int `mapstructure:"overlap_lines"`
	MinTokens    int `mapstructure:"min_tokens"`
}

func Load() *Config {
	cfg := &Config{
		DocsDir:   "docs",
		HelixHost: "http://localhost:6969",
		Embedding: EmbeddingConfig{
			Provider: "helix",
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

	if host := viper.GetString("helix_host"); host != "" {
		cfg.HelixHost = host
	}

	return cfg
}
