package catalog

import (
	"reflect"
	"testing"
)

func TestBuildConfigClaudeGithubFilesystem(t *testing.T) {
	cfg := BuildConfig("claude", []string{"github", "filesystem"}, "", "")

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

	// 白名单：含 github + 默认 anthropic 源 + 固定放行(anthropic.com/claude.com) + npm，去重有序。
	want := []string{"anthropic.com", "api.anthropic.com", "api.github.com", "claude.com", "registry.npmjs.org"}
	if !reflect.DeepEqual(cfg.Allowlist, want) {
		t.Errorf("白名单 = %v，期望 %v", cfg.Allowlist, want)
	}
}

func TestBuildConfigCodexProxy(t *testing.T) {
	cfg := BuildConfig("codex", nil, "", "")
	p, ok := cfg.LLMProxies["openai"]
	if !ok {
		t.Fatal("codex 应生成 openai 代理")
	}
	if p.Headers["Authorization"] != "Bearer ${openai-key}" {
		t.Errorf("openai 代理 header 错误: %+v", p.Headers)
	}
	// 默认 base URL（https://api.openai.com/v1）→ 代理 target 取其源。
	if p.Target != "https://api.openai.com" {
		t.Errorf("默认 target = %q，期望 https://api.openai.com", p.Target)
	}
}

func TestBuildConfigModelInjection(t *testing.T) {
	cfg := BuildConfig("claude", nil, "https://api.fenno.ai", "claude-opus-4-8")
	ce := cfg.LLMProxies["anthropic"].ClientEnv
	if ce["ANTHROPIC_MODEL"] != "claude-opus-4-8" || ce["ANTHROPIC_SMALL_FAST_MODEL"] != "claude-opus-4-8" {
		t.Errorf("模型未注入到 clientEnv: %+v", ce)
	}
	// 未改动的基础 env 仍在。
	if ce["ANTHROPIC_BASE_URL"] != "{proxy}" {
		t.Errorf("基础 clientEnv 丢失: %+v", ce)
	}
}

func TestBuildConfigCustomBaseURL(t *testing.T) {
	cfg := BuildConfig("codex", nil, "https://llmapi.qiniu.io/v1", "")
	if got := cfg.LLMProxies["openai"].Target; got != "https://llmapi.qiniu.io" {
		t.Errorf("自定义 target = %q，期望 https://llmapi.qiniu.io（取源，忽略 /v1）", got)
	}
	found := false
	for _, d := range cfg.Allowlist {
		if d == "llmapi.qiniu.io" {
			found = true
		}
	}
	if !found {
		t.Errorf("白名单应含自定义域名 llmapi.qiniu.io，实际: %v", cfg.Allowlist)
	}
}

func TestRequiredSecretNames(t *testing.T) {
	got := RequiredSecretNames("codex", []string{"github", "filesystem"})
	want := []string{"gh-token", "openai-key"} // filesystem 无密钥；排序去重
	if !reflect.DeepEqual(got, want) {
		t.Errorf("所需密钥 = %v，期望 %v", got, want)
	}
}
