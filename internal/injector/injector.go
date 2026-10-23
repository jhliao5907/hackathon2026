// Package injector 实现「凭据注入反向代理」，让 agent 自身的模型 key 也不进沙箱。
//
// 工作方式（以 Anthropic 为例）：
//   - 容器内 agent 配置 ANTHROPIC_BASE_URL=http://host:8773/anthropic，并带一个占位 key；
//   - agent 的模型请求因此发到本代理的 /anthropic 路径；
//   - 代理从宿主机 Keychain 取真实 key，覆盖写入 x-api-key 头，再转发到真实 api.anthropic.com；
//   - 容器内始终只有占位 key，真实 key 只存在于宿主机代理进程。
//
// 相比 TLS 中间人方案，这里不需要在容器里安装 CA 证书，简单且稳定。
package injector

import (
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"sbx/internal/audit"
	"sbx/internal/config"
)

// SecretGetter 抽象密钥来源（生产用系统 Keychain，测试可注入假实现）。
type SecretGetter interface {
	Get(name string) (string, error)
}

// Injector 把多个上游以「本地无密钥 URL」暴露给沙箱。
type Injector struct {
	store   SecretGetter
	logger  *log.Logger
	auditor *audit.Logger
	proxies map[string]*entry
}

type entry struct {
	name    string
	target  *url.URL
	headers map[string]string // header 名 -> keyring 密钥名
	rp      *httputil.ReverseProxy
}

// New 根据配置构造代理。
func New(cfg *config.Config, store SecretGetter, logger *log.Logger, auditor *audit.Logger) (*Injector, error) {
	if logger == nil {
		logger = log.Default()
	}
	in := &Injector{store: store, logger: logger, auditor: auditor, proxies: map[string]*entry{}}

	for name, p := range cfg.LLMProxies {
		tgt, err := url.Parse(p.Target)
		if err != nil {
			return nil, fmt.Errorf("llmProxy %q 的 target 非法: %w", name, err)
		}
		e := &entry{name: name, target: tgt, headers: p.Headers}
		e.rp = in.buildReverseProxy(e)
		in.proxies[name] = e
	}
	return in, nil
}

// buildReverseProxy 构造一个会注入凭据的反向代理（用现代的 Rewrite API）。
func (in *Injector) buildReverseProxy(e *entry) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(e.target) // scheme/host 指向上游

			// 剥掉 /<name> 路径前缀。
			prefix := "/" + e.name
			pr.Out.URL.Path = strings.TrimPrefix(pr.Out.URL.Path, prefix)
			if pr.Out.URL.Path == "" {
				pr.Out.URL.Path = "/"
			}
			// 反代到真实上游时，Host 必须是上游主机名。
			pr.Out.Host = e.target.Host

			// 从 Keychain 取密钥，覆盖写入对应 header（覆盖容器里的占位值）。
			for headerName, keyName := range e.headers {
				val, err := in.store.Get(keyName)
				if err != nil {
					in.logger.Printf("注入 header %q 失败：密钥 %q 未就绪: %v", headerName, keyName, err)
					continue
				}
				pr.Out.Header.Set(headerName, val)
				in.auditor.Secret(keyName, e.name)
			}
			in.auditor.LLM(e.name, pr.Out.Method+" "+pr.Out.URL.Path)
		},
	}
}

// ServeHTTP 按 /<name> 前缀路由到对应上游。
func (in *Injector) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name := firstSegment(r.URL.Path)
	e, ok := in.proxies[name]
	if !ok {
		http.Error(w, "未知的 LLM 代理: "+name, http.StatusNotFound)
		return
	}
	e.rp.ServeHTTP(w, r)
}

func firstSegment(p string) string {
	p = strings.TrimPrefix(p, "/")
	if i := strings.IndexByte(p, '/'); i >= 0 {
		return p[:i]
	}
	return p
}

// ListenAndServe 启动注入代理。
func (in *Injector) ListenAndServe(addr string) error {
	in.logger.Printf("凭据注入代理监听 %s（模型 key 只在宿主机注入，不进沙箱）", addr)
	return (&http.Server{Addr: addr, Handler: in}).ListenAndServe()
}
