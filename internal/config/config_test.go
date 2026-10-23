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

func TestLoadMissingCommand(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	os.WriteFile(path, []byte("mcpServers:\n  x:\n    secrets: {}\n"), 0o644)
	if _, err := Load(path); err == nil {
		t.Fatal("缺少 command 应报错")
	}
}
