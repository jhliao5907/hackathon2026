package catalog

import (
	"net/url"
	"sort"
	"strings"

	"sbx/internal/config"
)

// npm registry 是容器内 npx 拉取 MCP server 包所需，始终加入白名单。
const npmRegistry = "registry.npmjs.org"

// BuildConfig 由「所选 agent + 启用的 MCP 预设 + 模型服务地址」生成一份 config.Config。
//   - allowlist = 各启用 MCP 的域名 ∪ 模型服务域名 ∪ npm registry（去重排序）
//   - mcpServers = 各启用预设 → {Image, Command, Secrets(env→keyName)}
//   - llmProxies = 该 agent 的模型代理（Target 取自 baseURL 的源，为空则用预设默认）
//
// baseURL 允许用户指向自建/第三方 OpenAI 兼容网关（如 https://llmapi.qiniu.io/v1）。
// 生成结果不含任何密钥值，仅密钥名。
func BuildConfig(agentKey string, enabledMCP []string, baseURL, model string) *config.Config {
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
		base := strings.TrimSpace(baseURL)
		if base == "" {
			base = a.BaseURL
		}
		origin, host := originAndHost(base)
		proxy := a.Proxy
		proxy.Target = origin
		// 注入所选模型名（第三方网关常只开通部分模型，需显式指定）。
		if m := strings.TrimSpace(model); m != "" {
			if proxy.ClientEnv == nil {
				proxy.ClientEnv = map[string]string{}
			}
			for _, env := range a.ModelEnvs {
				proxy.ClientEnv[env] = m
			}
		}
		cfg.LLMProxies[a.ProxyName] = proxy
		if host != "" {
			domains[host] = struct{}{}
		}
		// 固定放行域名（如 Claude Code 的启动预检端点）。
		for _, d := range a.AlwaysAllow {
			domains[d] = struct{}{}
		}
	}

	for d := range domains {
		cfg.Allowlist = append(cfg.Allowlist, d)
	}
	sort.Strings(cfg.Allowlist)
	return cfg
}

// originAndHost 把一个 base URL 解析为「源(scheme://host)」与「主机名」。
// 路径部分（如 /v1）被忽略：注入代理只需源，/v1 由 agent 的请求路径携带。
func originAndHost(raw string) (origin, host string) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return raw, ""
	}
	return u.Scheme + "://" + u.Host, u.Hostname()
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
