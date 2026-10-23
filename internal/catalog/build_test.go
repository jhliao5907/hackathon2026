package catalog

import (
	"reflect"
	"testing"
)

func TestBuildConfigClaudeGithubFilesystem(t *testing.T) {
	cfg := BuildConfig("claude", []string{"github", "filesystem"})

	// MCP：两个 server，github 带密钥名、filesystem 无密钥。
	if len(cfg.MCPServers) != 2 {
		t.Fatalf("应有 2 个 MCP server，实际 %d", len(cfg.MCPServers))
	}
	if cfg.MCPServers["github"].Secrets["GITHUB_PERSONAL_ACCESS_TOKEN"] != "gh-token" {
		t.Errorf("github 密钥名错误: %+v", cfg.MCPServers["github"].Secrets)
	}
	if len(cfg.MCPServers["filesystem"].Secrets) != 0 {
		t.Errorf("filesystem 不应有密钥")
	}

	// 代理：claude → anthropic。
	if _, ok := cfg.LLMProxies["anthropic"]; !ok {
		t.Errorf("应含 anthropic 代理，实际: %v", cfg.LLMProxies)
	}

	// 白名单：含 github + anthropic + npm registry，去重且有序。
	want := []string{"api.anthropic.com", "api.github.com", "registry.npmjs.org"}
	if !reflect.DeepEqual(cfg.Allowlist, want) {
		t.Errorf("白名单 = %v，期望 %v", cfg.Allowlist, want)
	}
}

func TestBuildConfigCodexProxy(t *testing.T) {
	cfg := BuildConfig("codex", nil)
	p, ok := cfg.LLMProxies["openai"]
	if !ok {
		t.Fatal("codex 应生成 openai 代理")
	}
	if p.Headers["Authorization"] != "Bearer ${openai-key}" {
		t.Errorf("openai 代理 header 错误: %+v", p.Headers)
	}
}

func TestRequiredSecretNames(t *testing.T) {
	got := RequiredSecretNames("codex", []string{"github", "filesystem"})
	want := []string{"gh-token", "openai-key"} // filesystem 无密钥；排序去重
	if !reflect.DeepEqual(got, want) {
		t.Errorf("所需密钥 = %v，期望 %v", got, want)
	}
}
