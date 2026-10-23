package catalog

import (
	"sort"

	"sbx/internal/config"
)

// npm registry 是容器内 npx 拉取 MCP server 包所需，始终加入白名单。
const npmRegistry = "registry.npmjs.org"

// BuildConfig 由「所选 agent + 启用的 MCP 预设」生成一份 config.Config。
//   - allowlist = 各启用 MCP 的域名 ∪ 该 agent 代理域名 ∪ npm registry（去重排序）
//   - mcpServers = 各启用预设 → {Image, Command, Secrets(env→keyName)}
//   - llmProxies = 该 agent 的模型代理（claude=anthropic / codex=openai）
//
// 生成结果不含任何密钥值，仅密钥名。
func BuildConfig(agentKey string, enabledMCP []string) *config.Config {
	cfg := &config.Config{
		MCPServers: map[string]config.MCPServer{},
		LLMProxies: map[string]config.LLMProxy{},
	}
	domains := map[string]struct{}{npmRegistry: {}}

	for _, key := range enabledMCP {
		p, ok := MCPByKey(key)
		if !ok {
			continue
		}
		secrets := map[string]string{}
		for _, s := range p.Secrets {
			secrets[s.Env] = s.KeyName
		}
		cfg.MCPServers[p.Key] = config.MCPServer{
			Image:   p.Image,
			Command: p.Command,
			Secrets: secrets,
		}
		for _, d := range p.Allowlist {
			domains[d] = struct{}{}
		}
	}

	if a, ok := AgentByKey(agentKey); ok {
		cfg.LLMProxies[a.ProxyName] = a.Proxy
		for _, d := range a.Allowlist {
			domains[d] = struct{}{}
		}
	}

	for d := range domains {
		cfg.Allowlist = append(cfg.Allowlist, d)
	}
	sort.Strings(cfg.Allowlist)
	return cfg
}

// RequiredSecretNames 返回「启用的 MCP + 该 agent 代理」所需的全部密钥名（去重排序）。
// 供界面提示哪些密钥尚未设置。
func RequiredSecretNames(agentKey string, enabledMCP []string) []string {
	seen := map[string]struct{}{}
	for _, key := range enabledMCP {
		if p, ok := MCPByKey(key); ok {
			for _, s := range p.Secrets {
				seen[s.KeyName] = struct{}{}
			}
		}
	}
	if a, ok := AgentByKey(agentKey); ok {
		for _, headerVal := range a.Proxy.Headers {
			for _, name := range config.SecretRefs(headerVal) {
				seen[name] = struct{}{}
			}
		}
	}
	var out []string
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
