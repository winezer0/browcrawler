// Package app 是应用编排层：装配 SessionPool、策略、指标与爬虫，并驱动任务。
// main 只负责解析 CLI；所有生命周期与业务顺序都收敛在这里。
package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"

	"github.com/winezer0/browcrawler/internal/browserrunner"
	"github.com/winezer0/browcrawler/internal/config"
	"github.com/winezer0/browcrawler/internal/crawl"
	"github.com/winezer0/browcrawler/internal/metrics"
	"github.com/winezer0/browcrawler/internal/policy"
	"github.com/winezer0/browcrawler/internal/pool"
	"github.com/winezer0/browcrawler/internal/session"
)

type App struct {
	resume      bool
	cfg         *config.Config
	logger      *log.Logger
	metrics     metrics.Recorder
	pool        *pool.Pool
	limiter     *policy.Limiter
	dedup       *crawl.DedupSession
	authHeader  http.Header
	creds       *crawl.Credentials
	checkpoints string
}

// Close 释放会话池、去重会话与指标；必须使用独立 ctx（主 ctx 可能已被信号取消）。
func (a *App) Close(ctx context.Context) error {
	err := a.pool.CloseAll(ctx)
	a.dedup.Close()
	if e := a.metrics.Close(); e != nil && err == nil {
		err = e
	}
	return err
}

func New(cfg *config.Config, logger *log.Logger) (*App, error) {
	var m metrics.Recorder = metrics.Nop()
	if cfg.Metrics.Enabled {
		c, err := metrics.New("browcrawler", cfg.Metrics.File)
		if err != nil {
			return nil, fmt.Errorf("init metrics: %w", err)
		}
		m = c
	}
	factory := session.BrowserFactory(browserrunner.NewFactory(cfg))
	// 双层生效：作用域判定同时用于导航（crawl 内部）与交付过滤（recorder 标记/导出）
	scopeFn, err := crawl.ScopeFromCfg(cfg.Crawl)
	if err != nil {
		_ = m.Close()
		return nil, fmt.Errorf("init scope: %w", err)
	}
	urlScope := crawl.URLOnScope(scopeFn)
	p, err := pool.New(pool.Options{
		Cfg: cfg, Logger: logger,
		SessionRoot: filepath.Join("work", "sessions"),
		Output: session.OutputPaths{
			Traffic: cfg.Output.Traffic, Discovered: cfg.Output.Discovered,
			HAR: cfg.Output.HAR, Requests: cfg.Output.Requests, Report: cfg.Output.Report,
			RequestsFiltered: cfg.Output.RequestsFiltered, SaveJSDir: cfg.Output.SaveJSDir,
			OutScope: cfg.Output.OutScope,
		},
		ScopeFilter: urlScope,
		Browser:     factory, Metrics: m,
	})
	if err != nil {
		_ = m.Close()
		return nil, err
	}
	dedup, err := crawl.NewDedupSessionWithMetrics(cfg.Crawl.URLPatternThreshold,
		cfg.Crawl.URLPatternTTLSeconds, logger, m)
	if err != nil {
		_ = p.CloseAll(context.Background())
		_ = m.Close()
		return nil, fmt.Errorf("init dedup: %w", err)
	}
	a := &App{cfg: cfg, logger: logger, metrics: m, pool: p, limiter: policy.New(cfg), dedup: dedup,
		checkpoints: filepath.Join("work", "checkpoints")}
	if cfg.Crawl.AuthFile != "" {
		headers, err := crawl.ParseAuthFile(cfg.Crawl.AuthFile)
		if err != nil {
			_ = p.CloseAll(context.Background())
			_ = m.Close()
			return nil, fmt.Errorf("load auth file: %w", err)
		}
		a.authHeader = headers
	}
	if cfg.Crawl.Credentials != "" {
		user, pass, ok := strings.Cut(cfg.Crawl.Credentials, ":")
		if !ok || user == "" {
			_ = p.CloseAll(context.Background())
			_ = m.Close()
			return nil, fmt.Errorf("credentials must be user:pass")
		}
		a.creds = &crawl.Credentials{User: user, Pass: pass}
	}
	return a, nil
}

func (a *App) Stats() map[string]int { return a.pool.Stats() }

// EnsureScope 落实授权边界：未显式指定 scope 时锁定 seed 主机。
func EnsureScope(cfg *config.Config, seed string) error {
	if strings.TrimSpace(cfg.Crawl.Scope) != "" {
		return nil
	}
	u, err := url.Parse(seed)
	if err != nil || u.Hostname() == "" {
		return fmt.Errorf("invalid seed URL %q: %v", seed, err)
	}
	cfg.Crawl.Scope = u.Hostname()
	return nil
}

// Run 并发执行多个 seed；并发度受 len(seeds) 与 max_session 双重限制。
func (a *App) Run(ctx context.Context, seeds []string, concurrency int) error {
	if len(seeds) == 0 {
		return fmt.Errorf("no seed URLs")
	}
	if concurrency < 1 {
		concurrency = 1
	}
	if concurrency > len(seeds) {
		concurrency = len(seeds)
	}
	if concurrency > a.cfg.MaxSession {
		concurrency = a.cfg.MaxSession
	}
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	errCh := make(chan error, len(seeds))
	for i, seed := range seeds {
		wg.Add(1)
		go func(i int, seed string) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				errCh <- ctx.Err()
				return
			}
			defer func() { <-sem }()
			if err := a.runOne(ctx, seed, i, len(seeds)); err != nil {
				errCh <- fmt.Errorf("task[%d] %s: %w", i, seed, err)
			}
		}(i, seed)
	}
	wg.Wait()
	close(errCh)
	var errs []error
	for e := range errCh {
		if e != nil && !errors.Is(e, context.Canceled) {
			errs = append(errs, e)
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// newRobots 只在显式开启 obey_robots 时构造；robots.txt 请求走本会话 goproxy。
func newRobots(cfg *config.Config, sess *session.Session) *policy.Robots {
	if !cfg.ObeyRobots {
		return nil
	}
	return policy.NewRobots(sess.Proxy.ProxyAddr())
}

func (a *App) runOne(ctx context.Context, seed string, idx, total int) error {
	sess, err := a.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire session: %w", err)
	}
	defer a.pool.Release(sess)
	out := sess.Output()
	prefix := ""
	if total > 1 {
		prefix = fmt.Sprintf("[task %d/%d] ", idx+1, total)
		a.logger.Printf("%sacquired session %s", prefix, sess.ID)
	}

	// 认证态注入：属于会话级能力，Run 之前完成（goproxy 录制到的请求即带认证头）
	if len(a.authHeader) > 0 {
		if applier, ok := sess.Driver.(crawl.AuthApplier); ok {
			if err := applier.ApplyAuth(ctx, a.authHeader, seed); err != nil {
				return fmt.Errorf("apply auth: %w", err)
			}
		}
	}
	c, err := crawl.New(crawl.Options{
		Cfg: a.cfg, Driver: sess.Driver, Rec: sess.Rec,
		DiscoveredPath: out.Discovered, Logger: a.logger,
		Limiter: a.limiter, Metrics: a.metrics, Robots: newRobots(a.cfg, sess),
		Dedup: a.dedup, Credentials: a.creds,
		CheckpointPath: crawl.CheckpointPathFor(a.checkpoints, seed),
		Resume:         a.cfg.Crawl.Resume,
	})
	if err != nil {
		return fmt.Errorf("crawler init: %w", err)
	}
	defer c.Close()

	taskCtx, cancel := context.WithTimeout(ctx, a.cfg.Timeout.Task())
	defer cancel()
	st, runErr := c.Run(taskCtx, seed)
	if runErr != nil {
		a.logger.Printf("%s[result] interrupted: %v", prefix, runErr)
	}
	a.logger.Printf("%s[result] %s", prefix, st)
	a.logger.Printf("%s[completion] %s complete=%t reasons=%v", prefix, st.Completion.Status, st.Completion.Complete, st.Completion.Reasons)
	a.logger.Printf("%s[result] traffic=%s discovered=%s", prefix, out.Traffic, out.Discovered)
	if out.HAR != "" {
		a.logger.Printf("%s[result] har=%s", prefix, out.HAR)
	}
	return nil
}
