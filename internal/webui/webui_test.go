package webui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sbx/internal/config"
)

// fakeStore 是内存假密钥库。
type fakeStore struct{ m map[string]string }

func newFakeStore() *fakeStore            { return &fakeStore{m: map[string]string{}} }
func (f *fakeStore) Set(n, v string) error { f.m[n] = v; return nil }
func (f *fakeStore) Has(n string) bool     { _, ok := f.m[n]; return ok }

func newServer(t *testing.T) (*Server, string) {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "sandbox.yaml")
	return &Server{Store: newFakeStore(), CfgPath: cfgPath, ProjectDir: t.TempDir()}, cfgPath
}

func TestStateReturnsPresets(t *testing.T) {
	s, _ := newServer(t)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest("GET", "/api/state", nil))
	if rr.Code != 200 {
		t.Fatalf("状态码 %d", rr.Code)
	}
	var st stateResp
	if err := json.Unmarshal(rr.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if len(st.Agents) < 2 || len(st.MCPPresets) < 2 {
		t.Errorf("预设数量不足: agents=%d mcp=%d", len(st.Agents), len(st.MCPPresets))
	}
	if _, ok := st.AgentSecrets["codex"]; !ok {
		t.Errorf("缺少 codex 的模型密钥名")
	}
}

func TestSecretThenConfigProducesLoadableYAML(t *testing.T) {
	s, cfgPath := newServer(t)
	h := s.Handler()

	// 存一个密钥。
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("POST", "/api/secret",
		strings.NewReader(`{"name":"gh-token","value":"xxx"}`)))
	if rr.Code != 200 || !s.Store.Has("gh-token") {
		t.Fatalf("存密钥失败: code=%d", rr.Code)
	}

	// 生成配置：claude + github + filesystem。
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("POST", "/api/config",
		strings.NewReader(`{"agent":"claude","enabled":["github","filesystem"]}`)))
	if rr.Code != 200 {
		t.Fatalf("生成配置失败: code=%d body=%s", rr.Code, rr.Body)
	}

	// 写出的 yaml 应能被 config.Load 读回，且含预期内容、无密钥值。
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("生成的配置无法加载: %v", err)
	}
	if _, ok := cfg.MCPServers["github"]; !ok {
		t.Errorf("缺少 github server")
	}
	if cfg.MCPServers["github"].Secrets["GITHUB_PERSONAL_ACCESS_TOKEN"] != "gh-token" {
		t.Errorf("应只写密钥名 gh-token")
	}
	if _, ok := cfg.LLMProxies["anthropic"]; !ok {
		t.Errorf("claude 应生成 anthropic 代理")
	}
}

func TestConfigWithCustomMCPAndAllow(t *testing.T) {
	s, cfgPath := newServer(t)
	body := `{"agent":"claude","enabled":["filesystem"],
	  "customMcp":[{"name":"My Tool!","command":"npx -y some-mcp","secrets":[{"env":"API_KEY","keyName":"my-key","value":"s3cret"}]}],
	  "customAllow":["example.com","Foo.COM"]}`
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest("POST", "/api/config", strings.NewReader(body)))
	if rr.Code != 200 {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body)
	}
	// 自定义密钥应已存入。
	if !s.Store.Has("my-key") {
		t.Error("自定义 MCP 的密钥值应已存入")
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	// 名称被规整为 mytool；命令按空格拆分；默认镜像。
	sv, ok := cfg.MCPServers["mytool"]
	if !ok {
		t.Fatalf("自定义 MCP 未写入, servers=%v", cfg.MCPServers)
	}
	if sv.Image != "node:20-slim" || len(sv.Command) != 3 || sv.Secrets["API_KEY"] != "my-key" {
		t.Errorf("自定义 MCP 内容错误: %+v", sv)
	}
	// 自定义域名应在白名单（小写）。
	has := func(d string) bool {
		for _, x := range cfg.Allowlist {
			if x == d {
				return true
			}
		}
		return false
	}
	if !has("example.com") || !has("foo.com") {
		t.Errorf("自定义白名单缺失: %v", cfg.Allowlist)
	}
}

func TestLaunchReportsMissingSecrets(t *testing.T) {
	s, _ := newServer(t)
	rr := httptest.NewRecorder()
	// 未设置任何密钥，codex+github 需要 openai-key 与 gh-token。
	s.Handler().ServeHTTP(rr, httptest.NewRequest("POST", "/api/launch",
		strings.NewReader(`{"agent":"codex","enabled":["github"]}`)))
	var d struct {
		OK      bool     `json:"ok"`
		Missing []string `json:"missing"`
	}
	json.Unmarshal(rr.Body.Bytes(), &d)
	if d.OK {
		t.Fatal("缺密钥时不应 ok")
	}
	if len(d.Missing) != 2 {
		t.Errorf("应报告 2 个缺失密钥，实际: %v", d.Missing)
	}
}

func TestLaunchScript(t *testing.T) {
	got := launchScript("/Applications/My Tools/sbx", "/Users/me/proj dir", "codex")
	for _, want := range []string{
		"#!/bin/bash",
		"/opt/homebrew/bin",                       // 补全 PATH，docker 可被找到
		"cd '/Users/me/proj dir'",                 // 含空格路径被单引号包裹
		"'/Applications/My Tools/sbx' run 'codex'", // sbx 绝对路径运行
		"read -n1",                                // 结束暂停，窗口不闪退
	} {
		if !strings.Contains(got, want) {
			t.Errorf("启动脚本缺少 %q\n实际:\n%s", want, got)
		}
	}
}

func TestAuditEndpoint(t *testing.T) {
	s, _ := newServer(t)
	// 写一个审计日志文件（含合法 JSONL + 一行噪声）。
	s.AuditPath = filepath.Join(t.TempDir(), "sbx-audit.log")
	content := `{"time":"2026-10-23T19:00:00+08:00","type":"egress","decision":"deny","host":"evil.com"}
noise line
{"time":"2026-10-23T19:00:01+08:00","type":"mcp","server":"github","method":"tools/call"}
`
	os.WriteFile(s.AuditPath, []byte(content), 0o644)

	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest("GET", "/api/audit", nil))
	var d struct {
		Exists bool              `json:"exists"`
		Events []json.RawMessage `json:"events"`
	}
	json.Unmarshal(rr.Body.Bytes(), &d)
	if !d.Exists || len(d.Events) != 2 { // 噪声行被过滤
		t.Fatalf("应返回 2 条事件，实际 exists=%v n=%d", d.Exists, len(d.Events))
	}
}

func TestAuditEndpointNoFile(t *testing.T) {
	s, _ := newServer(t)
	s.AuditPath = filepath.Join(t.TempDir(), "nope.log")
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest("GET", "/api/audit", nil))
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"exists":false`) {
		t.Fatalf("无日志应返回 exists:false，实际: %s", rr.Body)
	}
}

func TestAppleScriptEscape(t *testing.T) {
	if got := appleScriptEscape(`/tmp/a"b\c`); got != `/tmp/a\"b\\c` {
		t.Errorf("转义错误: %q", got)
	}
}

func TestServesIndexHTML(t *testing.T) {
	s, _ := newServer(t)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest("GET", "/", nil))
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), "sbx 安全沙箱") {
		t.Fatalf("首页未正确返回: code=%d", rr.Code)
	}
	_ = http.StatusOK
}
