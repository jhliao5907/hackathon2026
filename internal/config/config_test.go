package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadAndSecretNames(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sandbox.yaml")
	yaml := `
allowlist:
  - api.github.com
mcpServers:
  github:
    command: ["npx", "-y", "@modelcontextprotocol/server-github"]
    secrets:
      GITHUB_PERSONAL_ACCESS_TOKEN: gh-token
  other:
    command: ["node", "srv.js"]
    secrets:
      API_KEY: gh-token
`
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Allowlist) != 1 || cfg.Allowlist[0] != "api.github.com" {
		t.Errorf("白名单解析错误: %v", cfg.Allowlist)
	}
	// 两个 server 都引用 gh-token，去重后应只有一个。
	names := cfg.SecretNames()
	if len(names) != 1 || names[0] != "gh-token" {
		t.Errorf("SecretNames 去重错误: %v", names)
	}
}

func TestSecretNamesIncludesLLMProxyTemplates(t *testing.T) {
	cfg := &Config{
		LLMProxies: map[string]LLMProxy{
			"anthropic": {Target: "https://a", Headers: map[string]string{"x-api-key": "anthropic-key"}},
			"openai":    {Target: "https://o", Headers: map[string]string{"Authorization": "Bearer ${openai-key}"}},
		},
	}
	got := map[string]bool{}
	for _, n := range cfg.SecretNames() {
		got[n] = true
	}
	// 整值即密钥名 与 ${模板} 两种写法都应被识别。
	if !got["anthropic-key"] || !got["openai-key"] {
		t.Errorf("SecretNames 应含 anthropic-key 与 openai-key，实际: %v", got)
	}
}

func TestSecretRefsAndResolve(t *testing.T) {
	if got := SecretRefs("anthropic-key"); len(got) != 1 || got[0] != "anthropic-key" {
		t.Errorf("整值写法解析错误: %v", got)
	}
	if got := SecretRefs("Bearer ${k}"); len(got) != 1 || got[0] != "k" {
		t.Errorf("模板写法解析错误: %v", got)
	}
	out, err := ResolveSecretTemplate("Bearer ${k}", func(n string) (string, error) {
		return map[string]string{"k": "secret"}[n], nil
	})
	if err != nil || out != "Bearer secret" {
		t.Errorf("模板解析 = %q, err=%v", out, err)
	}
}

func TestSaveRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sandbox.yaml")
	in := &Config{
		Allowlist: []string{"api.github.com", "api.anthropic.com"},
		MCPServers: map[string]MCPServer{
			"github": {Image: "node:20-slim", Command: []string{"npx", "srv"}, Secrets: map[string]string{"TOKEN": "gh-token"}},
		},
		LLMProxies: map[string]LLMProxy{
			"anthropic": {Target: "https://api.anthropic.com", Headers: map[string]string{"x-api-key": "anthropic-key"}},
		},
	}
	if err := Save(path, in); err != nil {
		t.Fatal(err)
	}
	out, err := Load(path)
	if err != nil {
		t.Fatalf("写出的配置无法读回: %v", err)
	}
	if len(out.MCPServers) != 1 || out.MCPServers["github"].Image != "node:20-slim" {
		t.Errorf("MCP 往返不一致: %+v", out.MCPServers)
	}
	if out.MCPServers["github"].Secrets["TOKEN"] != "gh-token" {
		t.Errorf("密钥名往返丢失: %+v", out.MCPServers["github"].Secrets)
	}
	if out.LLMProxies["anthropic"].Headers["x-api-key"] != "anthropic-key" {
		t.Errorf("代理往返不一致: %+v", out.LLMProxies)
	}
}

func TestLoadMissingCommand(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	os.WriteFile(path, []byte("mcpServers:\n  x:\n    secrets: {}\n"), 0o644)
	if _, err := Load(path); err == nil {
		t.Fatal("缺少 command 应报错")
	}
}
