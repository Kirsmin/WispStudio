package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadProviderOverrideAndLegacyModel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	content := `[server]
host = "0.0.0.0"
port = 9000
[storage]
data_dir = "Data" # inline comment
[openai]
base_url = "https://example.test/v1"
api_key = "sk-test"
timeout_sec = 30
[[providers]]
name = "Example"
default = true
[[providers.model_overrides]]
id = "gpt-5-test"
thinking_levels = ["off", "low", "medium", "high"]
thinking_style = "reasoning_effort"
[[models]]
id = "legacy"
name = "Legacy"
thinking_levels = ["off"]
thinking_style = "none"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Port != 9000 || len(cfg.Providers) != 1 || len(cfg.Providers[0].ModelOverrides) != 1 || len(cfg.Models) != 1 {
		t.Fatalf("unexpected config: %#v", cfg)
	}
}
