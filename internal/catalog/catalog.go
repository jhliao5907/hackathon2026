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
	// AlwaysAllow 是无论模型地址是什么都固定放行的出网域名。
	// 例：Claude Code 启动时会对 api.anthropic.com 做连通性/区域预检（不走 base URL），
	// 不放行会导致启动即报 ERR_SOCKET_CLOSED。
	AlwaysAllow []string `json:"alwaysAllow"`
}

// MCPPresets 返回内置的 MCP server 预设清单。
func MCPPresets() []MCPPreset {
	npm := func(pkg string, extra ...string) []string {
		return append([]string{"npx", "-y", pkg}, extra...)
	}
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
			Key: "gitlab", Display: "GitLab", Desc: "读写 GitLab 项目 / issue / MR",
			Image:   "node:20-slim",
			Command: npm("@modelcontextprotocol/server-gitlab"),
			Secrets: []SecretField{{
				Env: "GITLAB_PERSONAL_ACCESS_TOKEN", KeyName: "gitlab-token", Label: "GitLab 访问令牌",
			}},
			Allowlist: []string{"gitlab.com"},
		},
		{
			Key: "brave", Display: "Brave 搜索", Desc: "用 Brave Search 做网页搜索",
			Image:   "node:20-slim",
			Command: npm("@modelcontextprotocol/server-brave-search"),
			Secrets: []SecretField{{
				Env: "BRAVE_API_KEY", KeyName: "brave-key", Label: "Brave Search API Key",
			}},
			Allowlist: []string{"api.search.brave.com"},
		},
		{
			Key: "slack", Display: "Slack", Desc: "读写 Slack 频道与消息",
			Image:   "node:20-slim",
			Command: npm("@modelcontextprotocol/server-slack"),
			Secrets: []SecretField{
				{Env: "SLACK_BOT_TOKEN", KeyName: "slack-bot-token", Label: "Slack Bot Token"},
				{Env: "SLACK_TEAM_ID", KeyName: "slack-team-id", Label: "Slack Team ID"},
			},
			Allowlist: []string{"slack.com"},
		},
		{
			Key: "filesystem", Display: "文件系统", Desc: "在允许目录内读写文件（无需密钥）",
			Image:   "node:20-slim",
			Command: npm("@modelcontextprotocol/server-filesystem", "/workspace"),
		},
		{
			Key: "memory", Display: "记忆", Desc: "基于知识图谱的长期记忆（无需密钥）",
			Image:   "node:20-slim",
			Command: npm("@modelcontextprotocol/server-memory"),
		},
		{
			Key: "sequential-thinking", Display: "顺序思考", Desc: "结构化分步推理工具（无需密钥）",
			Image:   "node:20-slim",
			Command: npm("@modelcontextprotocol/server-sequential-thinking"),
		},
	}
}

// AgentPresets 返回内置的 agent 预设清单。
func AgentPresets() []AgentPreset {
	return []AgentPreset{
		{
			Key: "claude", Display: "Claude Code", ProxyName: "anthropic",
			Proxy: config.LLMProxy{
				ClientEnv: map[string]string{
					"ANTHROPIC_BASE_URL":                     "{proxy}",
					"ANTHROPIC_API_KEY":                      "sandbox-placeholder",
					"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1", // 减少 statsig/遥测等旁路流量
				},
				Headers: map[string]string{"x-api-key": "anthropic-key"},
			},
			BaseURL:     "https://api.anthropic.com",
			ModelEnvs:   []string{"ANTHROPIC_MODEL", "ANTHROPIC_SMALL_FAST_MODEL"},
			// Claude Code 启动连通性/区域预检需要（直连，不走 base URL）。
			// 放行整个 anthropic.com / claude.com（网关按子域名匹配，覆盖 api./platform./statsig. 等）。
			AlwaysAllow: []string{"anthropic.com", "claude.com"},
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
