// Package policy 集中实现授权边界与速率约束，避免把策略散落在 BFS 循环里。
// 设计文档 §6 / §11.3 #3：域名 scope 之外再加 IP 层 allow/deny，防止 DNS 指向内网时越界。
package policy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/winezer0/browcrawler/internal/config"
)

var (
	ErrPageLimit = errors.New("max_pages reached")
	ErrIPBlocked = errors.New("host blocked by IP policy")
)

// Limiter 是并发安全的请求预算控制器；零值代表不限制。
type Limiter struct {
	maxPages     int
	interval     time.Duration // 全局限速
	hostInterval time.Duration // 每主机限速（与全局叠加，取更长等待）
	ipAllow      []*net.IPNet
	ipDeny       []*net.IPNet
	resolve      func(string) ([]net.IP, error)

	mu       sync.Mutex
	pages    int
	nextAt   time.Time
	hostLast map[string]time.Time
	nowFunc  func() time.Time
}

// New 从配置构造 Limiter；resolve 可注入以便测试（默认 net.LookupIP）。
func New(cfg *config.Config) *Limiter {
	l := &Limiter{
		maxPages: cfg.Crawl.MaxPages,
		resolve:  net.LookupIP,
		nowFunc:  time.Now,
	}
	if cfg.Crawl.RatePerSec > 0 {
		l.interval = time.Duration(float64(time.Second) / cfg.Crawl.RatePerSec)
	}
	if cfg.Crawl.HostRatePerSec > 0 {
		l.hostInterval = time.Duration(float64(time.Second) / cfg.Crawl.HostRatePerSec)
		l.hostLast = map[string]time.Time{}
	}
	l.ipAllow = parseNets(cfg.Policy.IPAllow)
	l.ipDeny = parseNets(cfg.Policy.IPDeny)
	return l
}

// Enabled 表示是否真的挂了任何策略，编排层据此决定是否注入。
func (l *Limiter) Enabled() bool {
	return l != nil && (l.maxPages > 0 || l.interval > 0 || l.hostInterval > 0 ||
		len(l.ipAllow) > 0 || len(l.ipDeny) > 0)
}

// Acquire 在每次导航前调用：先做 IP 边界检查，再扣页面预算并等待限速窗口。
func (l *Limiter) Acquire(ctx context.Context, host string) error {
	if l == nil || !l.Enabled() {
		return nil
	}
	if err := l.checkIPErr(host); err != nil {
		return err
	}
	l.mu.Lock()
	if l.maxPages > 0 && l.pages >= l.maxPages {
		l.mu.Unlock()
		return ErrPageLimit
	}
	l.pages++
	var wait time.Duration
	if l.interval > 0 {
		now := l.nowFunc()
		if now.Before(l.nextAt) {
			wait = l.nextAt.Sub(now)
		} else {
			l.nextAt = now
		}
		l.nextAt = l.nextAt.Add(l.interval)
	}
	if l.hostInterval > 0 {
		now := l.nowFunc()
		next := now
		if last, seen := l.hostLast[host]; seen && now.Sub(last) < l.hostInterval {
			if hostWait := l.hostInterval - now.Sub(last); hostWait > wait {
				wait = hostWait
			}
			next = last.Add(l.hostInterval)
		}
		l.hostLast[host] = next
	}
	l.mu.Unlock()
	if wait > 0 {
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (l *Limiter) checkIPErr(host string) error {
	if len(l.ipAllow) == 0 && len(l.ipDeny) == 0 {
		return nil
	}
	ips := []net.IP(nil)
	if ip := net.ParseIP(strings.Trim(host, "[]")); ip != nil {
		ips = append(ips, ip)
	} else {
		var err error
		ips, err = l.resolve(host)
		if err != nil || len(ips) == 0 {
			// 解析失败时不放行：允许列表存在时尤其不能猜测。
			if len(l.ipAllow) > 0 {
				return fmt.Errorf("%w: DNS resolve %q failed: %v", ErrIPBlocked, host, err)
			}
			return nil
		}
	}
	for _, ip := range ips {
		if inAny(ip, l.ipDeny) {
			return fmt.Errorf("%w: %s -> %s (deny)", ErrIPBlocked, host, ip)
		}
	}
	if len(l.ipAllow) > 0 {
		for _, ip := range ips {
			if inAny(ip, l.ipAllow) {
				return nil
			}
		}
		return fmt.Errorf("%w: %s -> %v (not in allow list)", ErrIPBlocked, host, ips)
	}
	return nil
}

func inAny(ip net.IP, nets []*net.IPNet) bool {
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

func parseNets(items []string) []*net.IPNet {
	out := make([]*net.IPNet, 0, len(items))
	for _, s := range items {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if ip := net.ParseIP(s); ip != nil {
			bits := 128
			if ip.To4() != nil {
				bits = 32
			}
			out = append(out, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
			continue
		}
		if _, n, err := net.ParseCIDR(s); err == nil {
			out = append(out, n)
		}
	}
	return out
}

func (l *Limiter) Pages() int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.pages
}
