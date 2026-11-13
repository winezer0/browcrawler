package browserrunner

import (
	"context"
	"fmt"
	"log"
	"sync"

	"github.com/winezer0/browcrawler/internal/config"
)

// Factory 是 Session 与具体渲染后端之间的唯一接缝：给定会话资源，产出 Runner。
// 与 session.BrowserFactory 底层签名一致，由编排层做一次显式类型转换即可注入。
type Factory func(cfg *config.Config, id, dir, bundlePath string, logger *log.Logger) (Runner, error)

// NewFactory 按 browser_backend 选择后端；auto 返回带自动降级的组合 Runner。
func NewFactory(cfg *config.Config) Factory {
	switch cfg.BrowserBackend {
	case config.BackendChrome:
		return func(c *config.Config, id, dir, _ string, l *log.Logger) (Runner, error) {
			return NewChrome(c, id, dir, l), nil
		}
	case config.BackendObscura:
		return func(c *config.Config, id, dir, bundle string, l *log.Logger) (Runner, error) {
			return NewObscura(c, id, dir, bundle, l), nil
		}
	default: // auto
		return func(c *config.Config, id, dir, bundle string, l *log.Logger) (Runner, error) {
			return &fallbackRunner{
				primary:   NewObscura(c, id, dir, bundle, l),
				secondary: NewChrome(c, id, dir, l),
				logger:    l,
			}, nil
		}
	}
}

// fallbackRunner 用组合而非修改原实现来提供自动降级：
// 主后端 Start 失败 → 回收主后端 → 启动备用后端 → 后续全部委托给 active。
type fallbackRunner struct {
	mu        sync.Mutex
	primary   Runner
	secondary Runner
	active    Runner
	logger    *log.Logger
}

func (f *fallbackRunner) Start(ctx context.Context, proxyAddr string) error {
	var primaryErr error
	if err := f.primary.Start(ctx, proxyAddr); err == nil {
		f.mu.Lock()
		f.active = f.primary
		f.mu.Unlock()
		return nil
	} else {
		f.logger.Printf("[backend] primary renderer failed (%v), falling back to Chrome", err)
		_ = f.primary.Shutdown(ctx) // 幂等，清理可能残留的进程
		primaryErr = err
	}
	if err := f.secondary.Start(ctx, proxyAddr); err != nil {
		return fmt.Errorf("primary renderer: %w; chrome fallback: %w", primaryErr, err)
	}
	f.mu.Lock()
	f.active = f.secondary
	f.mu.Unlock()
	return nil
}

func (f *fallbackRunner) current() Runner {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.active
}

func (f *fallbackRunner) WSAddr() string {
	if r := f.current(); r != nil {
		return r.WSAddr()
	}
	return ""
}

func (f *fallbackRunner) Healthy() bool {
	if r := f.current(); r != nil {
		return r.Healthy()
	}
	return false
}

func (f *fallbackRunner) Shutdown(ctx context.Context) error {
	if r := f.current(); r != nil {
		return r.Shutdown(ctx)
	}
	return nil
}
