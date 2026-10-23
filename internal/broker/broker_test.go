package broker

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"sbx/internal/config"
)

// fakeSecrets 是注入的假密钥源，模拟宿主机 Keychain。
type fakeSecrets map[string]string

func (f fakeSecrets) Get(name string) (string, error) { return f[name], nil }

// TestBrokerInjectsSecretAndBridges 验证核心机制：
// broker 从（假）Keychain 取出密钥注入到子进程环境，并在 HTTP 与 stdio 之间桥接。
// mock MCP server 是一个 shell 脚本：读入一行请求后，把注入的环境变量原样回填进响应。
func TestBrokerInjectsSecretAndBridges(t *testing.T) {
	// mock MCP server：读一行请求后返回带注入 token 的 JSON-RPC 响应。
	cfg := &config.Config{
		MCPServers: map[string]config.MCPServer{
			"demo": {
				Command: []string{"sh", "-c",
					`read line; printf '{"jsonrpc":"2.0","id":1,"result":{"seen":"%s"}}\n' "$MCP_TEST_TOKEN"`},
				Secrets: map[string]string{"MCP_TEST_TOKEN": "demo-key"},
			},
		},
	}

	b := New(cfg, fakeSecrets{"demo-key": "s3cr3t-value"}, log.New(io.Discard, "", 0), nil, 8772)
	defer b.Close()

	srv := httptest.NewServer(b)
	defer srv.Close()

	reqBody := `{"jsonrpc":"2.0","id":1,"method":"tools/call"}`
	resp, err := http.Post(srv.URL+"/demo", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d，响应体 = %s", resp.StatusCode, body)
	}
	// 响应里应出现注入的真实密钥值，证明密钥被正确注入到了子进程环境。
	if !bytes.Contains(body, []byte("s3cr3t-value")) {
		t.Fatalf("响应未包含注入的密钥值，实际: %s", body)
	}
}

// TestSSEContentNegotiation 验证：当客户端 Accept 含 text/event-stream 时，
// broker 以 SSE 回单条响应（Codex 的 rmcp 客户端要求此形式）。
func TestSSEContentNegotiation(t *testing.T) {
	cfg := &config.Config{
		MCPServers: map[string]config.MCPServer{
			"demo": {Command: []string{"sh", "-c",
				`read line; printf '{"jsonrpc":"2.0","id":1,"result":{"ok":true}}\n'`}},
		},
	}
	b := New(cfg, fakeSecrets{}, log.New(io.Discard, "", 0), nil, 8772)
	defer b.Close()
	srv := httptest.NewServer(b)
	defer srv.Close()

	req, _ := http.NewRequest("POST", srv.URL+"/demo", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q，期望 text/event-stream", ct)
	}
	if !bytes.Contains(body, []byte("event: message")) || !bytes.Contains(body, []byte(`"ok":true`)) {
		t.Fatalf("SSE 响应格式不符，实际: %s", body)
	}
}

// TestNotificationReturns202 验证无 id 的通知消息返回 202 且不等待响应。
func TestNotificationReturns202(t *testing.T) {
	cfg := &config.Config{
		MCPServers: map[string]config.MCPServer{
			"demo": {Command: []string{"sh", "-c", "cat >/dev/null"}},
		},
	}
	b := New(cfg, fakeSecrets{}, log.New(io.Discard, "", 0), nil, 8772)
	defer b.Close()
	srv := httptest.NewServer(b)
	defer srv.Close()

	notif := `{"jsonrpc":"2.0","method":"notifications/initialized"}`
	resp, err := http.Post(srv.URL+"/demo", "application/json", strings.NewReader(notif))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("通知应返回 202，实际 %d", resp.StatusCode)
	}
}

// TestUnknownServer404 验证未配置的 server 名返回 404。
func TestUnknownServer404(t *testing.T) {
	cfg := &config.Config{MCPServers: map[string]config.MCPServer{}}
	b := New(cfg, fakeSecrets{}, log.New(io.Discard, "", 0), nil, 8772)
	srv := httptest.NewServer(b)
	defer srv.Close()

	resp, _ := http.Post(srv.URL+"/nope", "application/json", strings.NewReader("{}"))
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("未知 server 应 404，实际 %d", resp.StatusCode)
	}
}
