// Package broker 是「凭据不落沙箱」的核心。
//
// 工作方式：
//   - broker 以 MCP Streamable-HTTP 传输对沙箱内的 agent 暴露 /<serverName> 端点。
//   - 沙箱内的 MCP 配置只写这个 HTTP 地址，不含任何密钥。
//   - 当 agent 首次访问某个 server 时，broker 在宿主机侧用 os/exec 启动「真实」
//     MCP server（stdio 传输），并从系统 Keychain 取出密钥注入到该子进程的环境变量。
//   - broker 在 HTTP(JSON-RPC) 与子进程 stdio(JSON-RPC) 之间做双向桥接。
//
// 因此真实密钥只存在于 broker 的子进程环境里，容器内的 agent 全程只看到一个本地 URL。
package broker

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"sbx/internal/audit"
	"sbx/internal/config"
)

// SecretGetter 抽象密钥来源（生产用系统 Keychain，测试可注入假实现）。
type SecretGetter interface {
	Get(name string) (string, error)
}

// Broker 把多个 MCP server 以 HTTP 形式暴露给沙箱。
type Broker struct {
	cfg         *config.Config
	store       SecretGetter
	logger      *log.Logger
	auditor     *audit.Logger
	gatewayPort int // MCP server 容器的出网经此网关端口

	mu      sync.Mutex
	servers map[string]*childServer
}

// New 构造一个 Broker。auditor 可为 nil；gatewayPort 用于容器化 MCP server 的出网路由。
func New(cfg *config.Config, store SecretGetter, logger *log.Logger, auditor *audit.Logger, gatewayPort int) *Broker {
	if logger == nil {
		logger = log.Default()
	}
	return &Broker{
		cfg:         cfg,
		store:       store,
		logger:      logger,
		auditor:     auditor,
		gatewayPort: gatewayPort,
		servers:     map[string]*childServer{},
	}
}

// ServeHTTP 路由 /<serverName> 到对应的真实 MCP server。
func (b *Broker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name := strings.Trim(r.URL.Path, "/")
	if name == "" {
		http.Error(w, "请在路径中指定 MCP server 名，如 /github", http.StatusNotFound)
		return
	}
	if _, ok := b.cfg.MCPServers[name]; !ok {
		http.Error(w, fmt.Sprintf("未知的 MCP server: %s", name), http.StatusNotFound)
		return
	}

	switch r.Method {
	case http.MethodPost:
		b.handlePost(w, r, name)
	case http.MethodGet:
		// 本 demo 不提供 server->client 的 SSE 流；按 MCP 规范返回 405 即可，
		// 兼容的 Streamable-HTTP 客户端会据此退回到纯「POST 请求/响应」模式。
		http.Error(w, "此 server 不支持 GET SSE 流", http.StatusMethodNotAllowed)
	default:
		http.Error(w, "不支持的方法", http.StatusMethodNotAllowed)
	}
}

func (b *Broker) handlePost(w http.ResponseWriter, r *http.Request, name string) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 16<<20)) // 上限 16MB
	if err != nil {
		http.Error(w, "读取请求体失败", http.StatusBadRequest)
		return
	}

	cs, err := b.getOrStart(name)
	if err != nil {
		b.logger.Printf("启动 MCP server %q 失败: %v", name, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// 判断这条 JSON-RPC 消息是「请求」（带 id，需要回复）还是「通知」（无 id）。
	var probe struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	_ = json.Unmarshal(body, &probe)
	b.auditor.MCP(name, probe.Method, string(probe.ID))

	// 透传给子进程。
	if err := cs.write(body); err != nil {
		http.Error(w, "写入 MCP server 失败: "+err.Error(), http.StatusBadGateway)
		return
	}

	// 通知类消息无需等待响应，按规范返回 202。
	if len(probe.ID) == 0 || string(probe.ID) == "null" {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	// 请求类消息：等待子进程返回对应 id 的响应。
	resp, err := cs.await(string(probe.ID), 60*time.Second)
	if err != nil {
		http.Error(w, "等待 MCP server 响应失败: "+err.Error(), http.StatusGatewayTimeout)
		return
	}

	// 内容协商：MCP Streamable-HTTP 规范允许服务端用 application/json 或 text/event-stream 回复。
	// 部分客户端（如 Codex 的 rmcp）要求 SSE；Accept 含 text/event-stream 时即用 SSE 回单条响应。
	if acceptsEventStream(r) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "event: message\ndata: %s\n\n", resp)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(resp)
}

func acceptsEventStream(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "text/event-stream")
}

// getOrStart 懒启动指定的真实 MCP server 子进程。
func (b *Broker) getOrStart(name string) (*childServer, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if cs, ok := b.servers[name]; ok && cs.alive() {
		return cs, nil
	}

	spec := b.cfg.MCPServers[name]

	// 从 Keychain 取密钥（审计只记名字，绝不记值）。
	secretEnv := map[string]string{}
	for envName, keyName := range spec.Secrets {
		val, err := b.store.Get(keyName)
		if err != nil {
			return nil, fmt.Errorf("密钥 %q 未设置，请先运行: sbx secret set %s", keyName, keyName)
		}
		secretEnv[envName] = val
		b.auditor.Secret(keyName, name)
	}

	cs, err := startChild(name, spec, secretEnv, b.gatewayPort, b.logger)
	if err != nil {
		return nil, err
	}
	b.servers[name] = cs
	mode := "宿主机进程"
	if spec.Image != "" {
		mode = "容器(" + spec.Image + ")"
	}
	b.logger.Printf("已启动 MCP server %q [%s]（注入 %d 个密钥，均来自宿主机 Keychain，不进 agent 沙箱）", name, mode, len(spec.Secrets))
	return cs, nil
}

// Close 关闭所有子进程。
func (b *Broker) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, cs := range b.servers {
		cs.close()
	}
}

// ---- 子进程：JSON-RPC over stdio 的桥接 ----

type childServer struct {
	name   string
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	logger *log.Logger

	writeMu sync.Mutex

	pendMu  sync.Mutex
	pending map[string]chan json.RawMessage
	done    chan struct{}
}

// dockerRunCmd 把 MCP server 封装成一个受限容器的 `docker run -i`。
// 关键：密钥用 `-e NAME`（不带值）透传——值只存在 docker CLI 进程的环境里，
// 不会出现在 docker run 的 argv 中，避免被宿主机 `ps` 看到。
func dockerRunCmd(spec config.MCPServer, secretEnv map[string]string, gatewayPort int) *exec.Cmd {
	proxy := fmt.Sprintf("http://host.docker.internal:%d", gatewayPort)
	args := []string{
		"run", "-i", "--rm",
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		"--add-host", "host.docker.internal:host-gateway",
		// MCP server 容器的出网同样经白名单网关。
		"-e", "HTTP_PROXY=" + proxy,
		"-e", "HTTPS_PROXY=" + proxy,
		"-e", "NO_PROXY=host.docker.internal",
	}
	env := os.Environ()
	for k, v := range secretEnv {
		args = append(args, "-e", k) // 只给名字，值走下面的 env
		env = append(env, k+"="+v)
	}
	args = append(args, spec.Image)
	args = append(args, spec.Command...)

	c := exec.Command("docker", args...)
	c.Env = env
	return c
}

func startChild(name string, spec config.MCPServer, secretEnv map[string]string, gatewayPort int, logger *log.Logger) (*childServer, error) {
	if len(spec.Command) == 0 {
		return nil, fmt.Errorf("server %q 未配置 command", name)
	}

	var cmd *exec.Cmd
	if spec.Image != "" {
		// 容器化：MCP server 跑在受限容器里，密钥注入到该容器（不进 agent 沙箱）。
		cmd = dockerRunCmd(spec, secretEnv, gatewayPort)
	} else {
		// 宿主机进程模式（测试/简单场景）。
		cmd = exec.Command(spec.Command[0], spec.Command[1:]...)
		env := os.Environ()
		for k, v := range secretEnv {
			env = append(env, k+"="+v)
		}
		cmd.Env = env
	}
	// 子进程 stderr 跟随 logger 的输出（run 时写文件、serve 时到终端），
	// 避免 MCP server 的启动横幅污染前台 agent 的 TUI。
	cmd.Stderr = logger.Writer()

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	cs := &childServer{
		name:    name,
		cmd:     cmd,
		stdin:   stdin,
		logger:  logger,
		pending: map[string]chan json.RawMessage{},
		done:    make(chan struct{}),
	}
	go cs.readLoop(stdout)
	return cs, nil
}

// readLoop 持续读取子进程 stdout 的按行 JSON-RPC 消息，
// 把带 id 的「响应」分发给对应的等待者，其余（通知）记录后丢弃。
func (cs *childServer) readLoop(stdout io.Reader) {
	defer close(cs.done)
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 16<<20) // 支持最大 16MB 的单行消息
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		msg := make([]byte, len(line))
		copy(msg, line)

		var probe struct {
			ID json.RawMessage `json:"id"`
		}
		_ = json.Unmarshal(msg, &probe)
		if len(probe.ID) == 0 || string(probe.ID) == "null" {
			// server->client 通知（progress/log 等），本 demo 直接丢弃。
			continue
		}
		cs.pendMu.Lock()
		ch, ok := cs.pending[string(probe.ID)]
		if ok {
			delete(cs.pending, string(probe.ID))
		}
		cs.pendMu.Unlock()
		if ok {
			ch <- msg
		}
	}
}

func (cs *childServer) write(msg []byte) error {
	cs.writeMu.Lock()
	defer cs.writeMu.Unlock()
	if _, err := cs.stdin.Write(append(bytes.TrimRight(msg, "\n"), '\n')); err != nil {
		return err
	}
	return nil
}

func (cs *childServer) await(id string, timeout time.Duration) (json.RawMessage, error) {
	ch := make(chan json.RawMessage, 1)
	cs.pendMu.Lock()
	cs.pending[id] = ch
	cs.pendMu.Unlock()

	select {
	case resp := <-ch:
		return resp, nil
	case <-time.After(timeout):
		cs.pendMu.Lock()
		delete(cs.pending, id)
		cs.pendMu.Unlock()
		return nil, fmt.Errorf("超时")
	case <-cs.done:
		return nil, fmt.Errorf("MCP server 已退出")
	}
}

func (cs *childServer) alive() bool {
	select {
	case <-cs.done:
		return false
	default:
		return true
	}
}

func (cs *childServer) close() {
	cs.stdin.Close()
	if cs.cmd.Process != nil {
		_ = cs.cmd.Process.Kill()
	}
}
