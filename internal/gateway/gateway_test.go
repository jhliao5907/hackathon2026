package gateway

import (
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestAllowed(t *testing.T) {
	g := New([]string{"api.github.com", "anthropic.com"}, nil, nil)

	cases := []struct {
		host string
		want bool
	}{
		{"api.github.com", true},        // 精确匹配
		{"api.github.com:443", true},    // 带端口
		{"API.GitHub.com", true},        // 大小写不敏感
		{"api.anthropic.com", true},     // 子域名命中 anthropic.com
		{"anthropic.com", true},         // 顶级条目精确匹配
		{"evil.example.com", false},     // 不在白名单
		{"notgithub.com", false},        // 非子域、非精确
		{"github.com.evil.com", false},  // 前缀伪装不应命中
	}
	for _, c := range cases {
		if got := g.Allowed(c.host); got != c.want {
			t.Errorf("Allowed(%q) = %v, 期望 %v", c.host, got, c.want)
		}
	}
}

// TestLiveProxyAllowDeny 实时验证：经网关代理访问白名单内主机成功，白名单外被 403 拦截。
func TestLiveProxyAllowDeny(t *testing.T) {
	// 后端“被访问的服务”，监听 127.0.0.1。
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "hello-from-backend")
	}))
	defer backend.Close()
	backendHost, _, _ := net.SplitHostPort(backend.Listener.Addr().String())

	// 网关只允许后端主机名；不允许其它。
	gw := New([]string{backendHost}, log.New(io.Discard, "", 0), nil)
	gwSrv := httptest.NewServer(gw)
	defer gwSrv.Close()

	proxyURL, _ := url.Parse(gwSrv.URL)
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}

	// 1) 白名单内 -> 200
	resp, err := client.Get(backend.URL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(body) != "hello-from-backend" {
		t.Fatalf("白名单内请求应成功，得到 %d / %q", resp.StatusCode, body)
	}

	// 2) 白名单外 -> 403（用一个不在白名单的 Host，经同一代理）
	req, _ := http.NewRequest("GET", "http://evil.example.com/", nil)
	resp2, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusForbidden {
		t.Fatalf("白名单外请求应被 403 拦截，得到 %d", resp2.StatusCode)
	}
}
