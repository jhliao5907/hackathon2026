package injector

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"sbx/internal/config"
)

type fakeSecrets map[string]string

func (f fakeSecrets) Get(name string) (string, error) { return f[name], nil }

// TestInjectsHeaderAndStripsPrefix 验证：代理从（假）Keychain 注入 header，
// 剥掉 /<name> 前缀，并把请求转发到上游；容器侧的占位值被覆盖。
func TestInjectsHeaderAndStripsPrefix(t *testing.T) {
	var gotKey, gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("x-api-key")
		gotPath = r.URL.Path
		io.WriteString(w, "ok")
	}))
	defer upstream.Close()

	cfg := &config.Config{
		LLMProxies: map[string]config.LLMProxy{
			"anthropic": {
				Target:  upstream.URL,
				Headers: map[string]string{"x-api-key": "anthropic-key"},
			},
		},
	}
	in, err := New(cfg, fakeSecrets{"anthropic-key": "real-secret"}, log.New(io.Discard, "", 0), nil)
	if err != nil {
		t.Fatal(err)
	}
	proxySrv := httptest.NewServer(in)
	defer proxySrv.Close()

	// 模拟容器内 agent：带占位 key，访问 /anthropic/v1/messages。
	req, _ := http.NewRequest("POST", proxySrv.URL+"/anthropic/v1/messages", nil)
	req.Header.Set("x-api-key", "placeholder-in-sandbox")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if gotKey != "real-secret" {
		t.Errorf("上游收到的 key = %q，期望被注入为 real-secret（占位值应被覆盖）", gotKey)
	}
	if gotPath != "/v1/messages" {
		t.Errorf("上游收到的 path = %q，期望剥掉 /anthropic 前缀", gotPath)
	}
}

func TestUnknownProxy404(t *testing.T) {
	cfg := &config.Config{LLMProxies: map[string]config.LLMProxy{}}
	in, _ := New(cfg, fakeSecrets{}, log.New(io.Discard, "", 0), nil)
	srv := httptest.NewServer(in)
	defer srv.Close()

	resp, _ := http.Get(srv.URL + "/nope/x")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("未知代理应 404，实际 %d", resp.StatusCode)
	}
}

func TestBadTarget(t *testing.T) {
	cfg := &config.Config{LLMProxies: map[string]config.LLMProxy{
		"x": {Target: "http://[::bad"},
	}}
	if _, err := New(cfg, fakeSecrets{}, nil, nil); err == nil {
		t.Fatal("非法 target 应报错")
	}
	_ = url.URL{}
}
