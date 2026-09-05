package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLoadProviderAndOverride(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	content := `[server]
host = "0.0.0.0"
port = 9000
[storage]
data_dir = "DataX"
[openai]
timeout_sec = 88
[[providers]]
name = "P"
base_url = "https://example.test/v1"
api_key = "k"
default = true
thinking_levels = ["off", "low", "medium", "high"]
thinking_style = "reasoning_effort"
[[providers.model_overrides]]
id = "m1"
thinking_levels = ["off", "on"]
thinking_style = "enable_thinking"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Port != 9000 || cfg.Server.Host != "0.0.0.0" || cfg.Storage.DataDir != "DataX" {
		t.Fatalf("bad basic config: %#v", cfg)
	}
	if len(cfg.Providers) != 1 || len(cfg.Providers[0].ModelOverrides) != 1 {
		t.Fatalf("bad provider parse: %#v", cfg.Providers)
	}
	if !reflect.DeepEqual(cfg.Providers[0].ThinkingLevels, []string{"off", "low", "medium", "high"}) {
		t.Fatalf("bad levels: %#v", cfg.Providers[0].ThinkingLevels)
	}
}
