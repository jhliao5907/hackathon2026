package sandbox

import (
	"strings"
	"testing"

	"sbx/internal/config"
)

func TestBuildArgsHardening(t *testing.T) {
	args, err := BuildArgs(Options{
		Agent:       "claude",
		Image:       "sbx-agent:latest",
		Workdir:     "/home/u/proj",
		BrokerPort:  8771,
		GatewayPort: 8772,
		MCPServers:  []string{"github"},
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")

	// 隔离加固项必须存在。
	for _, want := range []string{
		"--cap-drop ALL",
		"no-new-privileges",
		"--user 10001:10001",
		"--read-only",
		"--tmpfs /tmp:rw,exec,mode=1777",
		"--tmpfs /home/agent:rw,exec,mode=1777",
		"--pids-limit 512",
		"--memory 2g",
		"/home/u/proj:/workspace",
		"HTTP_PROXY=http://host.docker.internal:8772",
		"SBX_BROKER_BASE=http://host.docker.internal:8771",
		"SBX_MCP_SERVERS=github",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("docker run 参数缺少 %q\n实际: %s", want, joined)
		}
	}

	// 容器内绝不应出现任何密钥相关的注入（只传 server 名和 URL）。
	if strings.Contains(strings.ToLower(joined), "token=") || strings.Contains(strings.ToLower(joined), "secret=") {
		t.Errorf("docker run 参数疑似携带了密钥: %s", joined)
	}
}

func TestBuildArgsUnknownAgent(t *testing.T) {
	if _, err := BuildArgs(Options{Agent: "bogus"}); err == nil {
		t.Fatal("未知 agent 应报错")
	}
}

func TestBuildArgsInjectsLLMProxyEnv(t *testing.T) {
	args, err := BuildArgs(Options{
		Agent:        "claude",
		Image:        "sbx-agent:latest",
		Workdir:      "/p",
		BrokerPort:   8771,
		GatewayPort:  8772,
		InjectorPort: 8773,
		LLMProxies: map[string]config.LLMProxy{
			"anthropic": {
				Target: "https://api.anthropic.com",
				ClientEnv: map[string]string{
					"ANTHROPIC_BASE_URL": "{proxy}",
					"ANTHROPIC_API_KEY":  "sandbox-placeholder",
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")

	// {proxy} 应被替换为注入代理的 URL。
	if !strings.Contains(joined, "ANTHROPIC_BASE_URL=http://host.docker.internal:8773/anthropic") {
		t.Errorf("未注入正确的 BASE_URL\n实际: %s", joined)
	}
	// 容器里只有占位 key，绝不是真实密钥。
	if !strings.Contains(joined, "ANTHROPIC_API_KEY=sandbox-placeholder") {
		t.Errorf("未注入占位 key\n实际: %s", joined)
	}
}
