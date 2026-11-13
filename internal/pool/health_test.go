package pool

import (
	"context"
	"crypto/tls"
	"io"
	"log"
	"path/filepath"
	"testing"
	"time"

	"browcrawler/internal/browserrunner"
	"browcrawler/internal/config"
	"browcrawler/internal/session"
)

type fakeCA struct{}

func (fakeCA) Cert() (*tls.Certificate, error) { return &tls.Certificate{}, nil }
func (fakeCA) BundlePath() (string, error)     { return "bundle.pem", nil }
func (fakeCA) Close() error                    { return nil }

type deadRunner struct{}

func (deadRunner) Start(context.Context, string) error { return nil }
func (deadRunner) Shutdown(context.Context) error      { return nil }
func (deadRunner) WSAddr() string                      { return "" }
func (deadRunner) Healthy() bool                       { return false }

// unhealthySession 构造一个「未启动」的会话：Proxy.Healthy() 恒 false，
// 用来验证巡检能发现并隔离回收失效会话，而无需真的启动浏览器。
func unhealthySession(t *testing.T, dir string) *session.Session {
	t.Helper()
	cfg := config.Default()
	s, err := session.New(session.Options{
		ID: "s-dead", Dir: dir, Cfg: cfg, Logger: log.New(io.Discard, "", 0),
		CA: fakeCA{},
		Browser: func(*config.Config, string, string, string, *log.Logger) (browserrunner.Runner, error) {
			return deadRunner{}, nil
		},
		Output: session.OutputPaths{Traffic: filepath.Join(dir, "t.jsonl"), Discovered: filepath.Join(dir, "d.jsonl")},
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSweepDiscardsUnhealthyIdleSession(t *testing.T) {
	p := newTestPool(t, 2)
	s := unhealthySession(t, t.TempDir())
	if s.Healthy() {
		t.Fatal("未启动的会话应判定为不健康")
	}
	e := &entry{sess: s, lastUsed: p.now()}
	p.mu.Lock()
	p.all[s.ID] = e
	p.idle = append(p.idle, e)
	p.mu.Unlock()

	// 直接调 sweep 并等待后台巡检协程也跑过一轮，保证两条路径都安全。
	p.sweep()
	time.Sleep(150 * time.Millisecond)
	p.mu.Lock()
	total, idle := len(p.all), len(p.idle)
	p.mu.Unlock()
	if total != 0 || idle != 0 {
		t.Fatalf("失效会话应被移出池: total=%d idle=%d", total, idle)
	}
}

func TestSweepKeepsHealthyUnavailableIdleSession(t *testing.T) {
	// 没有可用健康会话时，sweep 不应凭空创建或误删；这里验证空池状态稳定。
	p := newTestPool(t, 1)
	p.sweep()
	st := p.Stats()
	if st["total"] != 0 || st["idle"] != 0 {
		t.Fatalf("空池巡检后状态异常: %+v", st)
	}
}
