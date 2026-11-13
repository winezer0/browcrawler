// Package proxyrunner 一对一会话绑定的 goproxy MITM 代理（只读录制）。
package proxyrunner

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/elazarl/goproxy"

	"browcrawler/internal/storage"
)

type Runner struct {
	ca          tls.Certificate
	rec         *storage.Recorder
	upstream    string
	passthrough map[string]bool
	ln          net.Listener
	srv         *http.Server
	addr        string
	closed      atomic.Bool
	log         *log.Logger
}

// New 只做初始化；端口在 Start 里通过 net.Listen 自动分配（避免冲突）。
// passthrough 是「不解密、直接隧道」的域名列表（用于证书固定 / mTLS 目标）。
// upstreamProxyAuthHeader 解析上游代理 URL 的 userinfo（user:pass@host），
// 返回 Proxy-Authorization 头值；无 userinfo 返回空。
func upstreamProxyAuthHeader(upstream string) string {
	u, err := url.Parse(upstream)
	if err != nil || u.User == nil {
		return ""
	}
	pass, _ := u.User.Password()
	creds := u.User.Username()
	if pass != "" {
		creds += ":" + pass
	}
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(creds))
}

func New(ca tls.Certificate, rec *storage.Recorder, upstream string, logger *log.Logger, passthrough []string) *Runner {
	set := make(map[string]bool, len(passthrough))
	for _, h := range passthrough {
		if h = strings.ToLower(strings.TrimSpace(h)); h != "" {
			set[h] = true
		}
	}
	return &Runner{ca: ca, rec: rec, upstream: upstream, log: logger, passthrough: set}
}

// shouldPassthrough 支持精确域名与 *.example.com 通配后缀。
func (p *Runner) shouldPassthrough(host string) bool {
	h := strings.ToLower(host)
	if i := strings.LastIndex(h, ":"); i > 0 && !strings.Contains(h[i:], "]") {
		h = h[:i] // 去掉端口
	}
	if p.passthrough[h] {
		return true
	}
	for d := range p.passthrough {
		if strings.HasPrefix(d, "*.") && strings.HasSuffix(h, d[1:]) {
			return true
		}
	}
	return false
}

func (p *Runner) Start(_ context.Context) error {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	p.ln = ln
	p.addr = "http://" + ln.Addr().String()

	proxy := goproxy.NewProxyHttpServer()
	proxy.Verbose = false
	proxy.Logger = log.New(io.Discard, "", 0)
	proxy.AllowHTTP2 = false

	// 上游：外网可走本地 10808 代理，内网/回环直连（保证本地测试站点可达）。
	var upstreamURL *url.URL
	if p.upstream != "" {
		upstreamURL, err = url.Parse(p.upstream)
		if err != nil {
			_ = ln.Close()
			return err
		}
	}
	proxy.Tr = &http.Transport{
		Proxy: func(req *http.Request) (*url.URL, error) {
			if upstreamURL == nil || isLocal(req.URL.Hostname()) {
				return nil, nil
			}
			return upstreamURL, nil
		},
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		DisableCompression:    true,
		ForceAttemptHTTP2:     false,
		MaxIdleConns:          64,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       30 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	}
	// 隧道直通分支：仅 passthrough 域名会走这里，内网直连、外网经上游代理 CONNECT。
	// upstream 带 user:pass@host 时注入 Proxy-Authorization（goproxy v1.7.2 的
	// NewConnectDialToProxy 不处理 userinfo，带认证上游会 407）。
	proxyAuthHeader := upstreamProxyAuthHeader(p.upstream)
	proxy.ConnectDialWithReq = func(req *http.Request, network, addr string) (net.Conn, error) {
		host := addr
		if h, _, err := net.SplitHostPort(addr); err == nil {
			host = h
		}
		if upstreamURL == nil || isLocal(host) {
			return (&net.Dialer{Timeout: 20 * time.Second}).Dial(network, addr)
		}
		dial := proxy.NewConnectDialToProxyWithHandler(p.upstream, func(r *http.Request) {
			if proxyAuthHeader != "" {
				r.Header.Set("Proxy-Authorization", proxyAuthHeader)
			}
		})
		return dial(network, addr)
	}

	baseTLS := goproxy.TLSConfigFromCA(&p.ca)
	mitm := &goproxy.ConnectAction{
		Action: goproxy.ConnectMitm,
		TLSConfig: func(host string, ctx *goproxy.ProxyCtx) (*tls.Config, error) {
			cfg, err := baseTLS(host, ctx)
			if err != nil {
				return nil, err
			}
			cfg.NextProtos = []string{"http/1.1"}
			return cfg, nil
		},
	}
	tunnel := &goproxy.ConnectAction{Action: goproxy.ConnectAccept}
	proxy.OnRequest().HandleConnect(goproxy.FuncHttpsHandler(
		func(host string, ctx *goproxy.ProxyCtx) (*goproxy.ConnectAction, string) {
			if p.shouldPassthrough(host) {
				p.log.Printf("[proxy] TLS passthrough: %s", host)
				p.rec.RecordConnect(host)
				return tunnel, host
			}
			return mitm, host
		}))

	proxy.OnRequest().DoFunc(func(req *http.Request, ctx *goproxy.ProxyCtx) (*http.Request, *http.Response) {
		ctx.UserData = p.rec.Begin(req)
		return req, nil
	})
	proxy.OnResponse().DoFunc(func(resp *http.Response, ctx *goproxy.ProxyCtx) *http.Response {
		if resp == nil {
			return nil
		}
		rec, _ := ctx.UserData.(*storage.Record)
		if rec == nil {
			rec = p.rec.Begin(ctx.Req)
		}
		p.rec.Attach(rec, resp)
		return resp
	})

	p.srv = &http.Server{Handler: proxy}
	go func() {
		if err := p.srv.Serve(ln); err != nil && err != http.ErrServerClosed && !p.closed.Load() {
			p.log.Printf("[proxy] serve error: %v", err)
		}
	}()
	p.log.Printf("[proxy] listening %s upstream=%q", p.addr, p.upstream)
	return nil
}

func (p *Runner) ProxyAddr() string { return p.addr }

func (p *Runner) Healthy() bool {
	if p.closed.Load() || p.addr == "" {
		return false
	}
	c, err := net.DialTimeout("tcp", strings.TrimPrefix(p.addr, "http://"), 500*time.Millisecond)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// Shutdown 优雅关闭 http.Server，等待在途请求收尾。
func (p *Runner) Shutdown(ctx context.Context) error {
	p.closed.Store(true)
	if p.srv == nil {
		return nil
	}
	err := p.srv.Shutdown(ctx)
	if p.ln != nil {
		_ = p.ln.Close()
	}
	return err
}

func isLocal(host string) bool {
	h := strings.ToLower(host)
	if h == "localhost" || strings.HasSuffix(h, ".local") || strings.HasSuffix(h, ".internal") {
		return true
	}
	ip := net.ParseIP(h)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast()
}
