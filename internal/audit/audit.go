// Package audit 提供结构化审计日志（JSON Lines）。
//
// 安全沙箱必须可审计：谁在什么时候调了哪个 MCP 工具、访问了哪个域名（放行还是拒绝）、
// 用了哪个凭据。所有事件以一行一个 JSON 对象追加写入日志文件，便于 grep / 导入分析。
package audit

import (
	"encoding/json"
	"io"
	"os"
	"sync"
	"time"
)

// Event 类型常量。
const (
	TypeEgress = "egress" // 出网网关的放行/拒绝
	TypeMCP    = "mcp"     // 一次 MCP JSON-RPC 请求
	TypeLLM    = "llm"     // 一次经凭据注入代理的模型调用
	TypeSecret = "secret"  // 一次密钥读取（注入时）
)

// Event 是一条审计记录。字段按需填充，空值 omit。
type Event struct {
	Time     string `json:"time"`
	Type     string `json:"type"`
	Decision string `json:"decision,omitempty"` // allow | deny
	Host     string `json:"host,omitempty"`
	Method   string `json:"method,omitempty"`
	Server   string `json:"server,omitempty"` // MCP server 名 / LLM 代理名
	RPCID    string `json:"rpc_id,omitempty"`
	Secret   string `json:"secret,omitempty"` // keyring 密钥名（注意：只记名字，绝不记值）
	Detail   string `json:"detail,omitempty"`
}

// Logger 并发安全地把事件写成 JSONL。
type Logger struct {
	mu     sync.Mutex
	w      io.Writer
	closer io.Closer
}

// NewFile 打开（追加）一个审计日志文件。
func NewFile(path string) (*Logger, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	return &Logger{w: f, closer: f}, nil
}

// NewWriter 用任意 Writer 构造（测试用）。
func NewWriter(w io.Writer) *Logger { return &Logger{w: w} }

// Log 写入一条事件，自动补时间戳。
func (l *Logger) Log(e Event) {
	if l == nil {
		return
	}
	e.Time = time.Now().Format(time.RFC3339)
	l.mu.Lock()
	defer l.mu.Unlock()
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	l.w.Write(append(b, '\n'))
}

// Egress 记录一次出网判定。
func (l *Logger) Egress(host, method string, allowed bool) {
	d := "deny"
	if allowed {
		d = "allow"
	}
	l.Log(Event{Type: TypeEgress, Host: host, Method: method, Decision: d})
}

// MCP 记录一次 MCP 请求。
func (l *Logger) MCP(server, method, rpcID string) {
	l.Log(Event{Type: TypeMCP, Server: server, Method: method, RPCID: rpcID})
}

// LLM 记录一次经注入代理的模型调用。
func (l *Logger) LLM(proxy, detail string) {
	l.Log(Event{Type: TypeLLM, Server: proxy, Detail: detail})
}

// Secret 记录一次密钥读取（只记名字）。
func (l *Logger) Secret(name, server string) {
	l.Log(Event{Type: TypeSecret, Secret: name, Server: server})
}

// Close 关闭底层文件。
func (l *Logger) Close() error {
	if l != nil && l.closer != nil {
		return l.closer.Close()
	}
	return nil
}
