// 命令 sbx 是桌面安全沙箱的终端用户入口。
//
// 终端用户只需三步：
//
//	sbx init                 # 生成示例配置
//	sbx secret set gh-token  # 把密钥存入系统 Keychain（一次即可，不回显）
//	sbx run claude           # 在隔离沙箱里启动 Claude Code
package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"sbx/internal/audit"
	"sbx/internal/broker"
	"sbx/internal/config"
	"sbx/internal/gateway"
	"sbx/internal/injector"
	"sbx/internal/sandbox"
	"sbx/internal/secrets"
	"sbx/internal/webui"
	"sbx/internal/workspace"
)

const (
	defaultConfig = "sandbox.yaml"
	brokerPort    = 8771
	gatewayPort   = 8772
	injectorPort  = 8773
	defaultImage  = "sbx-agent:latest"
	auditLog      = "sbx-audit.log"
)

func main() {
	root := &cobra.Command{
		Use:   "sbx",
		Short: "桌面级安全沙箱：隔离运行 AI agent，密钥不落沙箱",
	}
	root.AddCommand(initCmd(), secretCmd(), runCmd(), serveCmd(), uiCmd())
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "错误:", err)
		os.Exit(1)
	}
}

// ---- sbx init ----

func initCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "在当前目录生成示例 sandbox.yaml",
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := os.Stat(defaultConfig); err == nil {
				return fmt.Errorf("%s 已存在，未覆盖", defaultConfig)
			}
			if err := os.WriteFile(defaultConfig, []byte(sampleConfig), 0o644); err != nil {
				return err
			}
			fmt.Printf("已生成 %s —— 里面不含任何密钥，可直接编辑白名单与 MCP server。\n", defaultConfig)
			return nil
		},
	}
}

// ---- sbx secret ----

func secretCmd() *cobra.Command {
	c := &cobra.Command{Use: "secret", Short: "管理存放在系统 Keychain 的密钥"}

	set := &cobra.Command{
		Use:   "set <名字>",
		Short: "交互式存入一个密钥（不回显）",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			var val string
			if term.IsTerminal(int(os.Stdin.Fd())) {
				fmt.Printf("请输入密钥 %q 的值（输入不回显）: ", name)
				b, err := term.ReadPassword(int(os.Stdin.Fd()))
				fmt.Println()
				if err != nil {
					return err
				}
				val = string(b)
			} else {
				// 非交互：从 stdin 读取（便于脚本/管道导入，值不进 argv）。
				b, err := io.ReadAll(os.Stdin)
				if err != nil {
					return err
				}
				val = strings.TrimRight(string(b), "\r\n")
			}
			if err := secrets.New().Set(name, val); err != nil {
				return err
			}
			fmt.Printf("已存入 Keychain：%s（沙箱与配置文件都不会看到它的值）\n", name)
			return nil
		},
	}

	list := &cobra.Command{
		Use:   "list",
		Short: "列出当前配置引用到的密钥及其是否已设置",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(defaultConfig)
			if err != nil {
				return err
			}
			store := secrets.New()
			for _, name := range cfg.SecretNames() {
				status := "未设置"
				if store.Has(name) {
					status = "已设置"
				}
				fmt.Printf("  %-20s %s\n", name, status)
			}
			return nil
		},
	}

	rm := &cobra.Command{
		Use:   "rm <名字>",
		Short: "删除一个密钥",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return secrets.New().Delete(args[0])
		},
	}

	c.AddCommand(set, list, rm)
	return c
}

// ---- sbx run ----

func runCmd() *cobra.Command {
	var image string
	var ephemeral, autoYes bool
	c := &cobra.Command{
		Use:   "run <claude|codex|dsh>",
		Short: "在隔离沙箱里启动指定 agent",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			agent := args[0]

			cfg, err := config.Load(defaultConfig)
			if err != nil {
				return err
			}
			store := secrets.New()

			// 启动前检查：所有被引用的密钥是否已就绪。
			var missing []string
			for _, name := range cfg.SecretNames() {
				if !store.Has(name) {
					missing = append(missing, name)
				}
			}
			if len(missing) > 0 {
				return fmt.Errorf("以下密钥尚未设置，请先运行 `sbx secret set <名字>`: %v", missing)
			}

			logger := log.New(os.Stderr, "[sbx] ", log.Ltime)

			// 启动宿主机侧三服务（broker / gateway / injector）+ 审计。
			_, cleanup, err := startHostServices(cfg, store, logger)
			if err != nil {
				return err
			}
			defer cleanup()

			time.Sleep(300 * time.Millisecond) // 让各服务就绪

			// 4) 收集要暴露给容器的 MCP server 名。
			var servers []string
			for name := range cfg.MCPServers {
				servers = append(servers, name)
			}

			wd, _ := os.Getwd()

			// 一次性工作区：挂副本进容器，退出后 diff 审批才回写。
			mountDir := wd
			var snap *workspace.Snapshot
			if ephemeral {
				snap, err = workspace.Prepare(wd)
				if err != nil {
					return err
				}
				defer snap.Cleanup()
				mountDir = snap.Dir
				logger.Printf("一次性工作区已就绪：改动先落在副本，退出后需你审批才回写")
			}

			logger.Printf("启动隔离容器运行 agent=%s，工作目录=%s", agent, filepath.Base(wd))
			if err := sandbox.Run(sandbox.Options{
				Agent:        agent,
				Image:        image,
				Workdir:      mountDir,
				BrokerPort:   brokerPort,
				GatewayPort:  gatewayPort,
				InjectorPort: injectorPort,
				MCPServers:   servers,
				LLMProxies:   cfg.LLMProxies,
			}); err != nil {
				return err
			}

			// 容器退出：展示 diff 并请求审批。
			if ephemeral {
				return reviewAndApply(snap, autoYes)
			}
			return nil
		},
	}
	c.Flags().StringVar(&image, "image", defaultImage, "agent 容器镜像")
	c.Flags().BoolVar(&ephemeral, "ephemeral", true, "一次性工作区：改动经审批才回写原目录")
	c.Flags().BoolVar(&autoYes, "yes", false, "跳过 diff 审批，直接回写")
	return c
}

// reviewAndApply 展示副本改动，请求用户确认后回写原目录。
func reviewAndApply(snap *workspace.Snapshot, autoYes bool) error {
	diff, changed, err := snap.Diff()
	if err != nil {
		return err
	}
	if !changed {
		fmt.Println("\n沙箱内没有产生任何文件改动，原目录保持不变。")
		return nil
	}

	fmt.Println("\n===== 沙箱内的改动（尚未离开沙箱）=====")
	fmt.Println(diff)
	fmt.Println("=======================================")

	if !autoYes {
		fmt.Print("是否把以上改动应用到原目录？[y/N]: ")
		var ans string
		fmt.Scanln(&ans)
		if ans != "y" && ans != "Y" {
			fmt.Println("已丢弃沙箱内改动，原目录未改动。")
			return nil
		}
	}
	if err := snap.Apply(); err != nil {
		return err
	}
	fmt.Println("已把改动回写到原目录。")
	return nil
}

// startHostServices 启动宿主机侧三服务（broker / gateway / injector）与审计日志，
// 供 run 与 serve 共用。返回审计器与一个收尾函数。
func startHostServices(cfg *config.Config, store *secrets.Store, logger *log.Logger) (*audit.Logger, func(), error) {
	auditor, err := audit.NewFile(auditLog)
	if err != nil {
		return nil, nil, err
	}
	logger.Printf("审计日志写入 ./%s", auditLog)

	// 1) MCP 凭据代理（broker）。
	br := broker.New(cfg, store, logger, auditor, gatewayPort)
	go func() {
		logger.Printf("MCP broker 监听 :%d（密钥仅在宿主机注入，不进沙箱）", brokerPort)
		if err := http.ListenAndServe(gateway.Addr(brokerPort), br); err != nil {
			logger.Printf("broker 退出: %v", err)
		}
	}()

	// 2) 出网白名单网关。
	gw := gateway.New(cfg.Allowlist, logger, auditor)
	go func() {
		if err := gw.ListenAndServe(gateway.Addr(gatewayPort)); err != nil {
			logger.Printf("gateway 退出: %v", err)
		}
	}()

	// 3) 凭据注入代理（模型 key 不落沙箱）。
	if len(cfg.LLMProxies) > 0 {
		inj, err := injector.New(cfg, store, logger, auditor)
		if err != nil {
			br.Close()
			auditor.Close()
			return nil, nil, err
		}
		go func() {
			if err := inj.ListenAndServe(gateway.Addr(injectorPort)); err != nil {
				logger.Printf("injector 退出: %v", err)
			}
		}()
	}

	cleanup := func() {
		br.Close()
		auditor.Close()
	}
	return auditor, cleanup, nil
}

// ---- sbx ui ----

func uiCmd() *cobra.Command {
	var port int
	var noOpen bool
	c := &cobra.Command{
		Use:   "ui",
		Short: "打开图形配置界面（本机浏览器，勾选即配置、一键启动）",
		RunE: func(cmd *cobra.Command, args []string) error {
			wd, _ := os.Getwd()
			srv := &webui.Server{
				Store:      secrets.New(),
				CfgPath:    defaultConfig,
				ProjectDir: wd,
			}
			addr := fmt.Sprintf("127.0.0.1:%d", port)
			url := "http://" + addr
			fmt.Printf("配置界面已启动：%s （Ctrl-C 退出）\n", url)
			if !noOpen {
				openBrowser(url)
			}
			return http.ListenAndServe(addr, srv.Handler())
		},
	}
	c.Flags().IntVar(&port, "port", 8780, "监听端口（仅 127.0.0.1）")
	c.Flags().BoolVar(&noOpen, "no-open", false, "不自动打开浏览器")
	return c
}

// openBrowser 用系统默认方式打开 URL（失败则忽略，用户可手动访问）。
func openBrowser(url string) {
	var c *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		c = exec.Command("open", url)
	case "windows":
		c = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		c = exec.Command("xdg-open", url)
	}
	_ = c.Start()
}

// ---- sbx serve ----

func serveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "只启动宿主机侧服务（broker/gateway/injector），便于联调",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(defaultConfig)
			if err != nil {
				return err
			}
			logger := log.New(os.Stderr, "[sbx] ", log.Ltime)
			_, cleanup, err := startHostServices(cfg, secrets.New(), logger)
			if err != nil {
				return err
			}
			defer cleanup()
			logger.Printf("服务就绪：broker :%d  gateway :%d  injector :%d（Ctrl-C 退出）", brokerPort, gatewayPort, injectorPort)
			select {} // 阻塞
		},
	}
}

const sampleConfig = `# 桌面安全沙箱配置（不含任何敏感值）
allowlist:
  - api.anthropic.com
  - api.openai.com
  - api.github.com
  - registry.npmjs.org

# MCP server：对 Claude Code 与 Codex 通用，均注册为指向 broker 的无密钥 HTTP 地址。
# image 非空时，MCP server 跑在受限容器里，密钥由宿主机 broker 注入到该容器。
mcpServers:
  github:
    image: "node:20-slim"
    command: ["npx", "-y", "@modelcontextprotocol/server-github"]
    secrets:
      GITHUB_PERSONAL_ACCESS_TOKEN: gh-token

# 凭据注入代理：让 agent 自身的模型 key 也不进沙箱（容器只放占位 key，真实 key 由代理注入）。
llmProxies:
  anthropic:                                     # Claude Code
    target: "https://api.anthropic.com"
    clientEnv:
      ANTHROPIC_BASE_URL: "{proxy}"
      ANTHROPIC_API_KEY: "sandbox-placeholder"
    headers:
      x-api-key: anthropic-key
  openai:                                        # Codex
    target: "https://api.openai.com"
    clientEnv:
      OPENAI_API_KEY: "sandbox-placeholder"
    headers:
      Authorization: "Bearer ${openai-key}"
`
