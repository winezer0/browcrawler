package session

import (
	"context"
	"crypto/tls"
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"

	"github.com/winezer0/browcrawler/internal/browserrunner"
	"github.com/winezer0/browcrawler/internal/config"
)

type fakeCA struct{}

func (fakeCA) Cert() (*tls.Certificate, error) { return &tls.Certificate{}, nil }
func (fakeCA) BundlePath() (string, error)     { return "bundle.pem", nil }
func (fakeCA) Close() error                    { return nil }

// fakeRunner 是可注入的浏览器假实现（Start 失败可配置）。
type fakeRunner struct{}

func (fakeRunner) Start(_ context.Context, _ string) error { return nil }
func (fakeRunner) Shutdown(_ context.Context) error        { return nil }
func (fakeRunner) WSAddr() string                          { return "" }
func (fakeRunner) Healthy() bool                           { return false }

// failingRunner 模拟浏览器启动即失败（如 Chrome 在无窗口站环境）。
type failingRunner struct{ err error }

func (f failingRunner) Start(_ context.Context, _ string) error { return f.err }
func (failingRunner) Shutdown(_ context.Context) error          { return nil }
func (failingRunner) WSAddr() string                            { return "" }
func (failingRunner) Healthy() bool                             { return false }

func newOptions(t *testing.T, dir string, browser BrowserFactory) Options {
	t.Helper()
	cfg := config.Default()
	return Options{
		ID: "s-test-001", Dir: dir, Cfg: cfg, Logger: log.New(io.Discard, "", 0),
		CA: fakeCA{}, Browser: browser,
		Output: OutputPaths{Traffic: filepath.Join(dir, "t.jsonl"), Discovered: filepath.Join(dir, "d.jsonl")},
	}
}

// 装配正确性：ID/输出路径映射/rec 文件创建。
func TestNewAssemblesSession(t *testing.T) {
	dir := t.TempDir()
	o := newOptions(t, dir, func(*config.Config, string, string, string, *log.Logger) (browserrunner.Runner, error) {
		return fakeRunner{}, nil
	})
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Shutdown(context.Background())
	if s.ID != "s-test-001" || s.Dir != dir {
		t.Fatalf("装配错误: id=%s dir=%s", s.ID, s.Dir)
	}
	out := s.Output()
	if out.Traffic != o.Output.Traffic || out.Discovered != o.Output.Discovered {
		t.Fatalf("Output 映射错误: %+v", out)
	}
	if _, err := os.Stat(o.Output.Traffic); err != nil {
		t.Fatalf("traffic 文件未创建: %v", err)
	}
	// 未 Start 时 Healthy 必须 false（Proxy/Browser 均未就绪）
	if s.Healthy() {
		t.Fatal("未 Start 不应 Healthy")
	}
}

// 回归（本轮修复）：浏览器启动失败时 New 返回错误且会话临时目录被清理，
// 否则反复失败会累积残留目录（I3 类问题）。
func TestNewBrowserFailureCleansDir(t *testing.T) {
	dir := t.TempDir()
	o := newOptions(t, dir, func(*config.Config, string, string, string, *log.Logger) (browserrunner.Runner, error) {
		return failingRunner{err: os.ErrPermission}, os.ErrPermission
	})
	if _, err := New(o); err == nil {
		t.Fatal("浏览器启动失败应返回错误")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("失败路径应清理会话目录: stat err = %v", err)
	}
}

// 缺少必填依赖时 New 必须报错（不 panic）。
func TestNewRejectsNilDependencies(t *testing.T) {
	dir := t.TempDir()
	base := newOptions(t, dir, func(*config.Config, string, string, string, *log.Logger) (browserrunner.Runner, error) {
		return fakeRunner{}, nil
	})
	if _, err := New(Options{}); err == nil {
		t.Fatal("空 Options 应报错")
	}
	noCA := base
	noCA.CA = nil
	if _, err := New(noCA); err == nil {
		t.Fatal("CA 为空应报错")
	}
	noBrowser := base
	noBrowser.Browser = nil
	if _, err := New(noBrowser); err == nil {
		t.Fatal("Browser 为空应报错")
	}
}

// Shutdown 幂等：once 保护，导出与回收只执行一次。
func TestShutdownIdempotent(t *testing.T) {
	// 会话目录会被 Shutdown 清理（RemoveAll），交付物目录必须独立。
	dir := t.TempDir()
	outDir := t.TempDir()
	o := newOptions(t, dir, func(*config.Config, string, string, string, *log.Logger) (browserrunner.Runner, error) {
		return fakeRunner{}, nil
	})
	o.Output.HAR = filepath.Join(outDir, "t.har")
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := s.Shutdown(ctx); err != nil {
		t.Fatalf("first shutdown: %v", err)
	}
	if err := s.Shutdown(ctx); err != nil {
		t.Fatalf("second shutdown: %v", err)
	}
	if _, err := os.Stat(o.Output.HAR); err != nil {
		t.Fatalf("HAR 未导出: %v", err)
	}
	// 会话目录本身应被清理（含 traffic/discovered）
	if _, err := os.Stat(filepath.Join(dir, "t.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("会话目录应被清理: %v", err)
	}
}
