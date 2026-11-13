// Package session 按设计文档 4.1/4.3 的严格顺序装配与回收单会话资源。
//
// 结构化约定：Session 不自己 new 任何具体组件，全部通过 Options 注入
// （CA Provider、Browser 工厂、输出路径），因此可被 SessionPool 复用，
// 也可在测试里替换成假实现，且多会话之间不会共享全局文件或单例。
package session

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"browcrawler/internal/browserrunner"
	"browcrawler/internal/ca"
	"browcrawler/internal/config"
	"browcrawler/internal/crawl"
	"browcrawler/internal/proxyrunner"
	"browcrawler/internal/storage"
)

// OutputPaths 是「本会话独占」的输出路径；池化时由上层按 sessionID 生成，避免并发写同一文件。
type OutputPaths struct {
	Traffic          string
	Discovered       string
	HAR              string
	Requests         string // 可重放原始 HTTP 请求包
	Report           string // 端点形状分组报告
	RequestsFiltered string // 形状去重的精简请求包
	SaveJSDir        string // 非空时 JS 源码落盘（溯源用）
	OutScope         string // 非空时导出域外流量清单（第三方资产）
}

// BrowserFactory 由后端选择层提供：给定 CA bundle 路径，产出满足 Runner 接口的浏览器。
type BrowserFactory func(cfg *config.Config, id, dir, bundlePath string, logger *log.Logger) (browserrunner.Runner, error)

// Options 是构造会话的全部依赖，字段即契约，无隐藏全局状态。
type Options struct {
	ID          string
	Dir         string
	Cfg         *config.Config
	Logger      *log.Logger
	CA          ca.Provider
	Browser     BrowserFactory
	Output      OutputPaths
	ScopeFilter func(string) bool // URL 级作用域判定（InScope 标记 + 交付过滤）
}

type Session struct {
	ID     string
	Dir    string
	cfg    *config.Config
	logger *log.Logger
	out    OutputPaths

	CA      ca.Provider
	Rec     *storage.Recorder
	Proxy   *proxyrunner.Runner
	Browser browserrunner.Runner // 面向接口，Obscura / Chrome 可替换
	Driver  crawl.PageDriver

	once      sync.Once
	unhealthy atomic.Bool
}

// New 装配各组件（不启动任何进程）；所有失败都在 Start 之前暴露。
func New(o Options) (*Session, error) {
	if o.Cfg == nil || o.Logger == nil || o.CA == nil || o.Browser == nil {
		return nil, fmt.Errorf("session.New: Cfg/Logger/CA/Browser 均不能为空")
	}
	if err := os.MkdirAll(o.Dir, 0o755); err != nil {
		return nil, err
	}
	cert, err := o.CA.Cert()
	if err != nil {
		return nil, err
	}
	bundle, err := o.CA.BundlePath()
	if err != nil {
		return nil, err
	}
	o.Logger.Printf("[session] %s CA bundle: %s", o.ID, bundle)

	rec, err := storage.New(o.Output.Traffic, o.ID, o.Cfg.MaxBodySize, o.Cfg.Crawl.SkipStaticBody)
	if err != nil {
		return nil, fmt.Errorf("open traffic output: %w", err)
	}
	rec.SetRawDump(o.Cfg.Output.RawDump)
	if o.Output.SaveJSDir != "" {
		rec.SetSaveJS(true, o.Output.SaveJSDir)
	}
	rec.SetScopeFilter(o.ScopeFilter) // nil = 未启用，录制/导出视为全量域内
	s := &Session{
		ID: o.ID, Dir: o.Dir, cfg: o.Cfg, logger: o.Logger, out: o.Output,
		CA: o.CA, Rec: rec,
	}
	s.Proxy = proxyrunner.New(*cert, rec, o.Cfg.UpstreamProxy, o.Logger, o.Cfg.Mitm.TLSPassthrough)
	s.Browser, err = o.Browser(o.Cfg, o.ID, o.Dir, filepath.Join(o.Dir, "ca-bundle.pem"), o.Logger)
	if err != nil {
		_ = rec.Close()
		// 浏览器启动失败（如 Chrome 在无窗口站环境即退）时不留会话临时目录，
		// 否则反复失败会累积残留（I3 类验收问题）。
		_ = os.RemoveAll(o.Dir)
		return nil, fmt.Errorf("create browser runner: %w", err)
	}
	s.Driver = crawl.NewRodDriver(
		time.Duration(o.Cfg.Crawl.SettleMS)*time.Millisecond,
		o.Cfg.Timeout.Navigation(),
	)
	return s, nil
}

// Output 返回本会话独占的输出路径，供爬虫层使用。
func (s *Session) Output() OutputPaths { return s.out }

// Start 顺序：goproxy → Browser(带 proxyAddr) → CDP 就绪 → rod 连接。
func (s *Session) Start(ctx context.Context) error {
	if err := s.Proxy.Start(ctx); err != nil {
		return fmt.Errorf("start proxy: %w", err)
	}
	if err := s.Browser.Start(ctx, s.Proxy.ProxyAddr()); err != nil {
		return fmt.Errorf("start browser: %w", err)
	}
	if err := s.Driver.Connect(ctx, s.Browser.WSAddr()); err != nil {
		return fmt.Errorf("connect page driver: %w", err)
	}
	s.logger.Printf("[session] %s READY proxy=%s cdp=%s", s.ID, s.Proxy.ProxyAddr(), s.Browser.WSAddr())
	return nil
}

// Healthy 供后台巡检使用；一旦被池标记为失效，立即返回 false。
func (s *Session) Healthy() bool {
	return !s.unhealthy.Load() && s.Proxy.Healthy() && s.Browser.Healthy()
}

// Shutdown 逆序回收：rod → Browser → goproxy → 输出/CA → 临时目录。幂等。
func (s *Session) Shutdown(ctx context.Context) (err error) {
	s.once.Do(func() {
		s.logger.Printf("[session] %s shutting down", s.ID)
		if s.Driver != nil {
			// Browser.close 后 rod 会返回 EOF，属预期行为。
			if e := s.Driver.Close(); e != nil && !errors.Is(e, io.EOF) && !errors.Is(e, context.Canceled) {
				s.logger.Printf("[session] driver close: %v", e)
			}
		}
		if s.Browser != nil {
			if e := s.Browser.Shutdown(ctx); e != nil && err == nil {
				err = e
			}
		}
		if s.Proxy != nil {
			if e := s.Proxy.Shutdown(ctx); e != nil && err == nil {
				err = e
			}
		}
		if s.Rec != nil {
			if s.out.HAR != "" {
				if e := s.Rec.ExportHAR(s.out.HAR); e != nil {
					s.logger.Printf("[session] HAR export: %v", e)
				}
			}
			if s.out.Requests != "" {
				if e := s.Rec.ExportRequests(s.out.Requests); e != nil {
					s.logger.Printf("[session] requests export: %v", e)
				}
			}
			if s.out.Report != "" {
				if e := s.Rec.ExportReport(s.out.Report); e != nil {
					s.logger.Printf("[session] report export: %v", e)
				}
			}
			if s.out.OutScope != "" {
				if e := s.Rec.ExportOutScope(s.out.OutScope); e != nil {
					s.logger.Printf("[session] outscope export: %v", e)
				}
			}
			if s.out.RequestsFiltered != "" {
				if e := s.Rec.ExportRequestsFiltered(s.out.RequestsFiltered); e != nil {
					s.logger.Printf("[session] requests-filtered export: %v", e)
				}
			}
			if e := s.Rec.Close(); e != nil && err == nil {
				err = e
			}
		}
		if s.CA != nil {
			if e := s.CA.Close(); e != nil && err == nil {
				err = e
			}
		}
		if s.cfg.KeepSessionDir {
			s.logger.Printf("[session] temp dir kept: %s", s.Dir)
		} else if e := os.RemoveAll(s.Dir); e != nil {
			s.logger.Printf("[session] cleanup %s: %v", s.Dir, e)
		} else {
			s.logger.Printf("[session] temp dir removed: %s", s.Dir)
		}
	})
	return err
}

// MarkUnhealthy 供池在进程意外退出时强制标记，此后 Healthy 恒为 false。
func (s *Session) MarkUnhealthy() {
	if s.unhealthy.CompareAndSwap(false, true) {
		s.logger.Printf("[session] %s marked unhealthy", s.ID)
	}
}
