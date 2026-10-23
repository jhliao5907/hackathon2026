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
	"os/exec"
	"runtime"

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
	runCmd := fmt.Sprintf("sbx run %s", body.Agent)
	if len(missing) > 0 {
		writeJSON(w, map[string]any{"ok": false, "missing": missing, "command": runCmd})
		return
	}
	// 尝试拉起一个终端运行 agent（TUI 需要终端）；失败则让前端显示命令。
	launched := s.openTerminal(runCmd)
	writeJSON(w, map[string]any{"ok": true, "launched": launched, "command": runCmd})
}

// openTerminal 在新终端窗口运行命令（目前支持 macOS），返回是否成功拉起。
func (s *Server) openTerminal(runCmd string) bool {
	if runtime.GOOS != "darwin" {
		return false
	}
	script := fmt.Sprintf("tell application \"Terminal\" to do script \"cd %q && %s\"", s.ProjectDir, runCmd)
	return exec.Command("osascript", "-e", script).Start() == nil
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
