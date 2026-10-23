// Package config 解析 sandbox.yaml。
//
// 设计要点：配置文件里永远不出现任何敏感值（api key / password）。
//   - mcpServers.*.secrets 只声明「环境变量名 -> keyring 中的密钥名」；
//   - llmProxies.*.headers 只声明「HTTP 头名 -> keyring 中的密钥名」。
// 真实密钥由宿主机侧（broker / injector）从 keyring 取出并注入，绝不写入沙箱或配置。
package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Config 是 sandbox.yaml 的根结构。
type Config struct {
	// Allowlist 是出网白名单域名列表，gateway 用它做 deny-by-default 放行。
	Allowlist []string `yaml:"allowlist"`
	// MCPServers 是沙箱内可用的 MCP server 清单，键为逻辑名（如 github）。
	MCPServers map[string]MCPServer `yaml:"mcpServers"`
	// LLMProxies 是凭据注入反向代理清单，键为逻辑名（如 anthropic）。
	// 让 agent 自身的模型 key 也不进沙箱。
	LLMProxies map[string]LLMProxy `yaml:"llmProxies"`
}

// MCPServer 描述一个真实 MCP server 的启动方式与需要注入的密钥。
type MCPServer struct {
	// Image 非空时，MCP server 跑在该镜像的受限容器里（推荐，隔离更强）；
	// 为空时退回宿主机进程运行（主要供测试与简单场景）。
	Image string `yaml:"image"`
	// Command 是启动真实 MCP server 的命令（stdio 传输）。
	Command []string `yaml:"command"`
	// Secrets 把 server 所需环境变量名映射到 keyring 中的密钥名。
	// 例：GITHUB_PERSONAL_ACCESS_TOKEN -> gh-token（值是密钥名，不是密钥本身）。
	Secrets map[string]string `yaml:"secrets"`
}

// LLMProxy 描述一个凭据注入反向代理：容器把模型请求发到本代理，
// 代理从 keyring 取密钥注入 header 后转发到真实上游。
type LLMProxy struct {
	// Target 是真实上游，如 https://api.anthropic.com。
	Target string `yaml:"target"`
	// ClientEnv 是注入到容器的环境变量；值里的占位符 {proxy} 会被替换为本代理的 URL。
	// 例：ANTHROPIC_BASE_URL: "{proxy}"。
	ClientEnv map[string]string `yaml:"clientEnv"`
	// Headers 把要注入到上游请求的 HTTP 头名映射到 keyring 中的密钥名。
	// 例：x-api-key -> anthropic-key（值是密钥名）。
	Headers map[string]string `yaml:"headers"`
}

// Load 从磁盘读取并校验配置。
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取配置 %s 失败: %w", path, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("解析 YAML 失败: %w", err)
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) validate() error {
	for name, s := range c.MCPServers {
		if len(s.Command) == 0 {
			return fmt.Errorf("mcpServer %q 缺少 command", name)
		}
	}
	for name, p := range c.LLMProxies {
		if p.Target == "" {
			return fmt.Errorf("llmProxy %q 缺少 target", name)
		}
	}
	return nil
}

// SecretNames 返回配置中引用到的所有 keyring 密钥名（去重），
// 涵盖 MCP server 与 LLM 代理，供 CLI 在启动前提示哪些密钥尚未设置。
func (c *Config) SecretNames() []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(keyName string) {
		if _, ok := seen[keyName]; ok {
			return
		}
		seen[keyName] = struct{}{}
		out = append(out, keyName)
	}
	for _, s := range c.MCPServers {
		for _, keyName := range s.Secrets {
			add(keyName)
		}
	}
	for _, p := range c.LLMProxies {
		for _, keyName := range p.Headers {
			add(keyName)
		}
	}
	return out
}
