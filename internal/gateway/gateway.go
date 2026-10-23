// Package gateway 实现一个出网白名单正向代理。
//
// 沙箱容器通过 HTTP_PROXY/HTTPS_PROXY 指向本网关，容器自身没有直连公网的能力。
// 网关对每个请求的目标域名做白名单校验：命中才放行，否则拒绝并记录日志。
// 这就是 deny-by-default 的出网控制。
package gateway

import (
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"sbx/internal/audit"
)

// Gateway 是一个带域名白名单的正向代理。
type Gateway struct {
	allow   map[string]bool
	logger  *log.Logger
	auditor *audit.Logger
}

// New 用白名单域名列表构造网关。域名大小写不敏感。auditor 可为 nil。
func New(allowlist []string, logger *log.Logger, auditor *audit.Logger) *Gateway {
	allow := make(map[string]bool, len(allowlist))
	for _, d := range allowlist {
		allow[strings.ToLower(strings.TrimSpace(d))] = true
	}
	if logger == nil {
		logger = log.Default()
	}
	return &Gateway{allow: allow, logger: logger, auditor: auditor}
}

// Allowed 判断某个 host（可能带端口）是否在白名单内。
// 规则：精确匹配域名，或匹配为白名单项的子域名。
func (g *Gateway) Allowed(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if g.allow[host] {
		return true
	}
	// 允许白名单项的子域名：api.github.com 命中 github.com 条目。
	for d := range g.allow {
		if strings.HasSuffix(host, "."+d) {
			return true
		}
	}
	return false
}

// ServeHTTP 同时处理普通 HTTP 代理请求和 HTTPS 的 CONNECT 隧道。
func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		g.handleConnect(w, r)
		return
	}
	g.handleHTTP(w, r)
}

func (g *Gateway) handleConnect(w http.ResponseWriter, r *http.Request) {
	if !g.Allowed(r.Host) {
		g.deny(r.Host, "CONNECT")
		http.Error(w, "出网被沙箱网关拒绝：域名不在白名单内", http.StatusForbidden)
		return
	}
	g.auditor.Egress(r.Host, "CONNECT", true)
	g.logger.Printf("ALLOW CONNECT %s", r.Host)

	dst, err := net.DialTimeout("tcp", r.Host, 10*time.Second)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "不支持连接劫持", http.StatusInternalServerError)
		dst.Close()
		return
	}
	src, _, err := hj.Hijack()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		dst.Close()
		return
	}
	src.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))

	// 双向透明转发。
	go func() { io.Copy(dst, src); dst.Close() }()
	go func() { io.Copy(src, dst); src.Close() }()
}

func (g *Gateway) handleHTTP(w http.ResponseWriter, r *http.Request) {
	if !g.Allowed(r.Host) {
		g.deny(r.Host, r.Method)
		http.Error(w, "出网被沙箱网关拒绝：域名不在白名单内", http.StatusForbidden)
		return
	}
	g.auditor.Egress(r.Host, r.Method, true)
	g.logger.Printf("ALLOW %s %s", r.Method, r.Host)

	r.RequestURI = ""
	resp, err := http.DefaultTransport.RoundTrip(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

func (g *Gateway) deny(host, method string) {
	g.auditor.Egress(host, method, false)
	g.logger.Printf("DENY %s %s （不在白名单内）", method, host)
}

// ListenAndServe 在指定地址启动网关。
func (g *Gateway) ListenAndServe(addr string) error {
	srv := &http.Server{Addr: addr, Handler: g}
	g.logger.Printf("出网网关监听 %s（deny-by-default，白名单 %d 项）", addr, len(g.allow))
	return srv.ListenAndServe()
}

// Addr 返回便于拼接的监听地址字符串。
func Addr(port int) string { return fmt.Sprintf(":%d", port) }
