// Package sandbox 负责把 agent 跑在一个隔离的 Docker 容器里。
//
// 关键安全约束通过 docker run 参数落地：
//   - 非 root 用户运行、--cap-drop ALL、no-new-privileges；
//   - 只挂载当前工作目录为 /workspace（其余宿主机文件不可见）；
//   - 出网强制经宿主机白名单网关（HTTP_PROXY/HTTPS_PROXY）；
//   - MCP 配置只写宿主机 broker 的 URL，容器内不含任何密钥。
package sandbox

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"sbx/internal/config"
)

// hostAlias 是容器内访问宿主机的地址（Docker Desktop for Mac/Windows 可用）。
const hostAlias = "host.docker.internal"

// Options 控制一次沙箱启动。
type Options struct {
	Agent        string // claude | codex | dsh
	Image        string // agent 容器镜像名
	Workdir      string // 挂载为 /workspace 的宿主机目录
	BrokerPort   int    // 宿主机 broker 端口
	GatewayPort  int    // 宿主机出网网关端口
	InjectorPort int    // 宿主机凭据注入代理端口
	MCPServers   []string
	LLMProxies   map[string]config.LLMProxy // 模型 key 不落沙箱的代理清单
}

// agentCommand 把逻辑 agent 名映射到容器内的启动命令。
func agentCommand(agent string) ([]string, error) {
	switch agent {
	case "claude":
		return []string{"claude"}, nil
	case "codex":
		return []string{"codex"}, nil
	case "dsh":
		return []string{"dsh"}, nil
	default:
		return nil, fmt.Errorf("未知 agent: %s（支持 claude|codex|dsh）", agent)
	}
}

// BuildArgs 组装 docker run 的参数（抽出来便于单测与审阅）。
func BuildArgs(o Options) ([]string, error) {
	cmd, err := agentCommand(o.Agent)
	if err != nil {
		return nil, err
	}

	proxy := fmt.Sprintf("http://%s:%d", hostAlias, o.GatewayPort)
	brokerBase := fmt.Sprintf("http://%s:%d", hostAlias, o.BrokerPort)
	injectorBase := fmt.Sprintf("http://%s:%d", hostAlias, o.InjectorPort)

	args := []string{
		"run", "--rm", "-it",
		// —— 隔离加固 ——
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		"--user", "10001:10001", // 非 root
		// 只读 rootfs：agent 无法篡改系统文件；可写处仅限下面显式声明的 tmpfs 与挂载。
		"--read-only",
		"--tmpfs", "/tmp:rw,exec,mode=1777",
		// 家目录（claude 配置、npm 缓存）走内存、随容器销毁；mode=1777 让非 root 用户可写。
		"--tmpfs", "/home/agent:rw,exec,mode=1777",
		// 资源上限：抑制失控进程 / fork 炸弹 / 吃爆内存。
		"--memory", "2g",
		"--cpus", "2",
		"--pids-limit", "512",
		// —— 文件可见性：只给当前项目目录 ——
		"-v", o.Workdir + ":/workspace",
		"-w", "/workspace",
		// —— 让容器能访问宿主机 broker / gateway ——
		"--add-host", hostAlias + ":host-gateway",
		// —— 出网 deny-by-default：一切经白名单网关 ——
		"-e", "HTTP_PROXY=" + proxy,
		"-e", "HTTPS_PROXY=" + proxy,
		"-e", "http_proxy=" + proxy,
		"-e", "https_proxy=" + proxy,
		// broker 自身要直连，不能被代理拦截
		"-e", fmt.Sprintf("NO_PROXY=%s", hostAlias),
		"-e", fmt.Sprintf("no_proxy=%s", hostAlias),
		// —— 告知容器内启动脚本：agent 类型、broker 与 injector 的无密钥入口 ——
		"-e", "SBX_AGENT=" + o.Agent,
		"-e", "SBX_BROKER_BASE=" + brokerBase,
		"-e", "SBX_INJECTOR_BASE=" + injectorBase,
		"-e", "SBX_MCP_SERVERS=" + strings.Join(o.MCPServers, ","),
	}

	// 凭据注入代理：把每个 LLM 代理的 clientEnv 注入容器，{proxy} 替换为代理 URL。
	// 这样 agent 自身的模型 key 也不进沙箱（容器里只有占位值）。
	for name, p := range o.LLMProxies {
		proxyURL := fmt.Sprintf("http://%s:%d/%s", hostAlias, o.InjectorPort, name)
		for envName, envVal := range p.ClientEnv {
			envVal = strings.ReplaceAll(envVal, "{proxy}", proxyURL)
			args = append(args, "-e", envName+"="+envVal)
		}
	}

	args = append(args, o.Image)
	args = append(args, cmd...)
	return args, nil
}

// Run 执行 docker run 并把标准流接到当前终端。
func Run(o Options) error {
	args, err := BuildArgs(o)
	if err != nil {
		return err
	}
	c := exec.Command("docker", args...)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c.Run()
}
