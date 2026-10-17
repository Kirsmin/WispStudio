package config

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestEnsureTokenKeepsExisting 已有 Token 时原样返回且不改动文件。
func TestEnsureTokenKeepsExisting(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	text := "[server]\nPort = 8787\nToken = \"fixed-token\"\n"
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatalf("写配置失败: %v", err)
	}
	token, err := EnsureToken(path)
	if err != nil {
		t.Fatalf("EnsureToken 失败: %v", err)
	}
	if token != "fixed-token" {
		t.Fatalf("token = %q, 期望 fixed-token", token)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取配置失败: %v", err)
	}
	if string(data) != text {
		t.Fatalf("已有 Token 时不应改写文件:\n%s", data)
	}
}

// TestEnsureTokenGeneratesAndPersists 空 Token 时生成随机令牌并写回，
// 写回结果可解析且二次调用返回同一令牌。
func TestEnsureTokenGeneratesAndPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	text := "[server]\nPort = 8787\nToken = \"\"\n# 注释保留检查\n"
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatalf("写配置失败: %v", err)
	}
	token, err := EnsureToken(path)
	if err != nil {
		t.Fatalf("EnsureToken 失败: %v", err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(token) {
		t.Fatalf("token 不是 32 字节十六进制: %q", token)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取配置失败: %v", err)
	}
	if !strings.Contains(string(data), `Token = "`+token+`"`) {
		t.Fatalf("令牌未写回:\n%s", data)
	}
	if !strings.Contains(string(data), "# 注释保留检查") {
		t.Fatalf("写回丢失注释:\n%s", data)
	}
	cfg, err := Parse(data)
	if err != nil {
		t.Fatalf("写回后解析失败: %v", err)
	}
	if cfg.Server.Token != token {
		t.Fatalf("解析令牌 = %q, 期望 %q", cfg.Server.Token, token)
	}
	again, err := EnsureToken(path)
	if err != nil {
		t.Fatalf("二次 EnsureToken 失败: %v", err)
	}
	if again != token {
		t.Fatalf("二次令牌 = %q, 期望 %q", again, token)
	}
}

// TestEnsureTokenAddsMissingSection 缺少 [server] 段时追加一段并写回。
func TestEnsureTokenAddsMissingSection(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	if err := os.WriteFile(path, []byte("[[Providers]]\nProviderID = \"fake\"\nBaseURL = \"http://127.0.0.1:1\"\nModels = [\"m\"]\n"), 0o600); err != nil {
		t.Fatalf("写配置失败: %v", err)
	}
	token, err := EnsureToken(path)
	if err != nil {
		t.Fatalf("EnsureToken 失败: %v", err)
	}
	cfg, err := LoadFile(path)
	if err != nil {
		t.Fatalf("重新加载失败: %v", err)
	}
	if cfg.Server.Token != token {
		t.Fatalf("解析令牌 = %q, 期望 %q", cfg.Server.Token, token)
	}
}
