// Package catalog 是预设目录：把常见 MCP server 与各 agent 的模型代理收成一份清单，
// 让图形界面能以「勾选 + 填密钥」的方式配置，用户无需知道底层 npx 命令、docker 镜像、
// 白名单域名或 header 模板。这里是这些技术细节的单一事实源。
package catalog

import "sbx/internal/config"

// SecretField 描述一个预设需要的密钥（只记名字与展示信息，不含值）。
type SecretField struct {
	Env     string `json:"env"`     // 注入到 MCP server 的环境变量名
	KeyName string `json:"keyName"` // 对应系统 Keychain 中的密钥名
	Label   string `json:"label"`   // 界面展示用的友好名
}

// MCPPreset 是一个开箱即用的 MCP server 预设。
type MCPPreset struct {
	Key       string        `json:"key"`
	Display   string        `json:"display"`
	Desc      string        `json:"desc"`
	Image     string        `json:"image"`
	Command   []string      `json:"-"`
	Secrets   []SecretField `json:"secrets"`
	Allowlist []string      `json:"allowlist"`
}

// AgentPreset 是一个 agent 及其模型代理（让模型 key 也不落沙箱）。
type AgentPreset struct {
	Key       string          `json:"key"`
	Display   string          `json:"display"`
	ProxyName string          `json:"-"`
	Proxy     config.LLMProxy `json:"-"` // 不含 Target，Target 由 BaseURL 推导
	// BaseURL 是该 agent 默认的模型服务地址（OpenAI/Anthropic 兼容）。
	// 用户可在界面改成自建/第三方网关（如 https://llmapi.qiniu.io/v1）。
	BaseURL string `json:"baseURL"`
	// ModelEnvs 是「所选模型名」要注入到的容器环境变量；留空表示用 agent 默认模型。
	// 第三方网关常只开通部分模型，需显式指定一个它支持的模型。
	ModelEnvs []string `json:"-"`
}

// MCPPresets 返回内置的 MCP server 预设清单。
func MCPPresets() []MCPPreset {
	return []MCPPreset{
		{
			Key: "github", Display: "GitHub", Desc: "读写 GitHub 仓库 / issue / PR",
			// GitHub 官方维护的 MCP server（Go 版，stdio 模式）。
			Image:   "ghcr.io/github/github-mcp-server:latest",
			Command: []string{"stdio"},
			Secrets: []SecretField{{
				Env: "GITHUB_PERSONAL_ACCESS_TOKEN", KeyName: "gh-token", Label: "GitHub 访问令牌",
			}},
			Allowlist: []string{"api.github.com"},
		},
		{
			Key: "filesystem", Display: "文件系统", Desc: "在允许目录内读写文件（无需密钥）",
			Image:     "node:20-slim",
			Command:   []string{"npx", "-y", "@modelcontextprotocol/server-filesystem", "/workspace"},
			Secrets:   nil,
			Allowlist: nil,
		},
		{
			Key: "fetch", Display: "网页抓取", Desc: "抓取网页并转为文本（无需密钥）",
			Image:     "node:20-slim",
			Command:   []string{"npx", "-y", "@modelcontextprotocol/server-fetch"},
			Secrets:   nil,
			Allowlist: nil,
		},
	}
}

// AgentPresets 返回内置的 agent 预设清单。
func AgentPresets() []AgentPreset {
	return []AgentPreset{
		{
			Key: "claude", Display: "Claude Code", ProxyName: "anthropic",
			Proxy: config.LLMProxy{
				ClientEnv: map[string]string{"ANTHROPIC_BASE_URL": "{proxy}", "ANTHROPIC_API_KEY": "sandbox-placeholder"},
				Headers:   map[string]string{"x-api-key": "anthropic-key"},
			},
			BaseURL:   "https://api.anthropic.com",
			ModelEnvs: []string{"ANTHROPIC_MODEL", "ANTHROPIC_SMALL_FAST_MODEL"},
		},
		{
			Key: "codex", Display: "Codex", ProxyName: "openai",
			Proxy: config.LLMProxy{
				ClientEnv: map[string]string{"OPENAI_API_KEY": "sandbox-placeholder"},
				Headers:   map[string]string{"Authorization": "Bearer ${openai-key}"},
			},
			BaseURL:   "https://api.openai.com/v1",
			ModelEnvs: []string{"SBX_CODEX_MODEL"},
		},
	}
}

// MCPByKey / AgentByKey 便捷查找。
func MCPByKey(key string) (MCPPreset, bool) {
	for _, p := range MCPPresets() {
		if p.Key == key {
			return p, true
		}
	}
	return MCPPreset{}, false
}

func AgentByKey(key string) (AgentPreset, bool) {
	for _, a := range AgentPresets() {
		if a.Key == key {
			return a, true
		}
	}
	return AgentPreset{}, false
}
