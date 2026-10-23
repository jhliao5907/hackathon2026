// Package webui 提供一个仅监听本机的图形配置界面（单页 Web + JSON API）。
//
// 目标：让最终用户靠「勾选 MCP + 填密钥 + 一键启动」完成配置，无需手写 sandbox.yaml，
// 也无需知道 npx 命令 / docker 镜像 / 白名单 / header 模板等技术细节。
// 所有预设来自 internal/catalog；生成的配置经 config.Save 落盘（不含密钥值）。
package webui

import (
	"encoding/json"
	_ "embed"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"sbx/internal/catalog"
	"sbx/internal/config"
)

//go:embed index.html
var indexHTML []byte

// SecretStore 抽象密钥库（生产用系统 Keychain，测试注入假实现）。
type SecretStore interface {
	Set(name, value string) error
	Has(name string) bool
}

// Server 是本地配置界面的 HTTP 服务。
type Server struct {
	Store      SecretStore
	CfgPath    string // sandbox.yaml 路径
	ProjectDir string // 启动 agent 时的工作目录
}

// Handler 组装路由。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(indexHTML)
	})
	mux.HandleFunc("/api/state", s.handleState)
	mux.HandleFunc("/api/secret", s.handleSecret)
	mux.HandleFunc("/api/config", s.handleConfig)
	mux.HandleFunc("/api/launch", s.handleLaunch)
	return mux
}

// ---- GET /api/state：返回预设、当前选择、密钥就绪状态 ----

type stateResp struct {
	Agents       []catalog.AgentPreset `json:"agents"`
	MCPPresets   []catalog.MCPPreset   `json:"mcpPresets"`
	Selected     selected              `json:"selected"`
	Secrets      map[string]bool       `json:"secrets"`      // 密钥名 -> 是否已设置
	AgentSecrets map[string][]string   `json:"agentSecrets"` // agentKey -> 其模型代理所需密钥名
	BaseURL      map[string]string     `json:"baseURL"`      // agentKey -> 当前模型服务地址
}

type selected struct {
	Agent   string   `json:"agent"`
	Enabled []string `json:"enabled"`
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	resp := stateResp{
		Agents:       catalog.AgentPresets(),
		MCPPresets:   catalog.MCPPresets(),
		Secrets:      map[string]bool{},
		AgentSecrets: map[string][]string{},
		BaseURL:      map[string]string{},
	}
	// 各 agent 模型服务地址：默认取预设。
	for _, a := range catalog.AgentPresets() {
		resp.BaseURL[a.Key] = a.BaseURL
	}
	// 从现有 sandbox.yaml 推断当前已启用的 MCP（存在即选中）与已配置的模型服务地址。
	resp.Selected.Agent = catalog.AgentPresets()[0].Key
	if cfg, err := config.Load(s.CfgPath); err == nil {
		for _, p := range catalog.MCPPresets() {
			if _, ok := cfg.MCPServers[p.Key]; ok {
				resp.Selected.Enabled = append(resp.Selected.Enabled, p.Key)
			}
		}
		for _, a := range catalog.AgentPresets() {
			if px, ok := cfg.LLMProxies[a.ProxyName]; ok && px.Target != "" {
				resp.BaseURL[a.Key] = px.Target
			}
		}
	}
	// 汇总所有预设涉及的密钥名，标注是否已设置。
	for _, p := range catalog.MCPPresets() {
		for _, sec := range p.Secrets {
			resp.Secrets[sec.KeyName] = s.Store.Has(sec.KeyName)
		}
	}
	for _, a := range catalog.AgentPresets() {
		names := catalog.RequiredSecretNames(a.Key, nil)
		resp.AgentSecrets[a.Key] = names
		for _, name := range names {
			resp.Secrets[name] = s.Store.Has(name)
		}
	}
	writeJSON(w, resp)
}

// ---- POST /api/secret：存密钥到系统 Keychain ----

func (s *Server) handleSecret(w http.ResponseWriter, r *http.Request) {
	var body struct{ Name, Value string }
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
		http.Error(w, "参数错误", http.StatusBadRequest)
		return
	}
	if err := s.Store.Set(body.Name, body.Value); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "name": body.Name})
}

// ---- POST /api/config：按选择生成并写入 sandbox.yaml ----

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Agent   string   `json:"agent"`
		Enabled []string `json:"enabled"`
		BaseURL string   `json:"baseUrl"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "参数错误", http.StatusBadRequest)
		return
	}
	cfg := catalog.BuildConfig(body.Agent, body.Enabled, body.BaseURL)
	if err := config.Save(s.CfgPath, cfg); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "allowlist": cfg.Allowlist})
}

// ---- POST /api/launch：校验密钥就绪，拉起终端运行 sbx run ----

func (s *Server) handleLaunch(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Agent   string   `json:"agent"`
		Enabled []string `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "参数错误", http.StatusBadRequest)
		return
	}
	// 校验所需密钥是否都已就绪。
	var missing []string
	for _, name := range catalog.RequiredSecretNames(body.Agent, body.Enabled) {
		if !s.Store.Has(name) {
			missing = append(missing, name)
		}
	}
	exe, _ := os.Executable()
	if exe == "" {
		exe = "sbx"
	}
	// 展示用命令（绝对路径，避免 sbx 不在 PATH 时复制粘贴跑不起来）。
	runCmd := fmt.Sprintf("cd %s && %s run %s", s.ProjectDir, exe, body.Agent)
	if len(missing) > 0 {
		writeJSON(w, map[string]any{"ok": false, "missing": missing, "command": runCmd})
		return
	}
	// 拉起一个终端窗口运行 agent（TUI 需要终端）；真正检查是否成功，失败则让前端显示命令。
	launched, err := s.openTerminal(exe, body.Agent)
	writeJSON(w, map[string]any{"ok": true, "launched": launched, "command": runCmd, "error": errStr(err)})
}

// launchScript 生成一个可执行的 .command 脚本内容：进入项目目录并运行 agent。
// 用 sbx 的绝对路径，避免依赖 PATH；用单引号包裹避免路径含空格/特殊字符时出错。
func launchScript(exe, dir, agent string) string {
	return "#!/bin/bash\n" +
		"cd " + shellSingleQuote(dir) + " || exit 1\n" +
		"exec " + shellSingleQuote(exe) + " run " + shellSingleQuote(agent) + "\n"
}

// openTerminal 把启动脚本写成 .command 文件并用 `open` 在终端里打开（macOS）。
// 优先用 iTerm（若已安装），否则回退到默认处理器（通常是 Terminal.app）。
// 比 osascript 拼字符串稳妥，且无需自动化(Automation)授权。
func (s *Server) openTerminal(exe, agent string) (bool, error) {
	if runtime.GOOS != "darwin" {
		return false, nil
	}
	path := filepath.Join(os.TempDir(), "sbx-launch-"+agent+".command")
	if err := os.WriteFile(path, []byte(launchScript(exe, s.ProjectDir, agent)), 0o755); err != nil {
		return false, err
	}
	// 优先 iTerm（通过其 AppleScript API 执行；open -a 不会运行 .command）。
	if itermInstalled() {
		if err := runInITerm(path); err == nil {
			return true, nil
		}
	}
	// 回退默认终端（通常是 Terminal.app，会直接执行 .command）。
	if err := exec.Command("open", path).Run(); err != nil {
		return false, err
	}
	return true, nil
}

// itermInstalled 通过 LaunchServices 判断 iTerm 是否安装。
func itermInstalled() bool {
	return exec.Command("osascript", "-e", `id of app "iTerm"`).Run() == nil
}

// runInITerm 让 iTerm 新开窗口运行给定可执行脚本。
func runInITerm(path string) error {
	script := `tell application "iTerm"
  activate
  create window with default profile command "` + appleScriptEscape(path) + `"
end tell`
	return exec.Command("osascript", "-e", script).Run()
}

// appleScriptEscape 转义 AppleScript 双引号字符串里的特殊字符。
func appleScriptEscape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return s
}

// shellSingleQuote 用单引号安全包裹一个 shell 参数。
func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func errStr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
