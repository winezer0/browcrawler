package policy

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/winezer0/browcrawler/internal/config"
)

func newTestLimiter(t *testing.T, mutate func(*config.Config)) *Limiter {
	t.Helper()
	c := config.Default()
	mutate(c)
	l := New(c)
	l.resolve = func(host string) ([]net.IP, error) {
		if host == "internal.test" {
			return []net.IP{net.ParseIP("10.1.2.3")}, nil
		}
		return []net.IP{net.ParseIP("93.184.216.34")}, nil
	}
	return l
}

func TestIPDenyTakesPriority(t *testing.T) {
	l := newTestLimiter(t, func(c *config.Config) {
		c.Policy.IPAllow = []string{"0.0.0.0/0"}
		c.Policy.IPDeny = []string{"10.0.0.0/8"}
	})
	if err := l.Acquire(context.Background(), "internal.test"); !errors.Is(err, ErrIPBlocked) {
		t.Fatalf("内网地址应被 deny 拒绝，实际 %v", err)
	}
	if err := l.Acquire(context.Background(), "public.test"); err != nil {
		t.Fatalf("公网地址应放行: %v", err)
	}
}

func TestIPAllowRejectsEverythingElse(t *testing.T) {
	l := newTestLimiter(t, func(c *config.Config) { c.Policy.IPAllow = []string{"127.0.0.0/8"} })
	if err := l.Acquire(context.Background(), "public.test"); !errors.Is(err, ErrIPBlocked) {
		t.Fatalf("不在白名单应被拒绝，实际 %v", err)
	}
	if err := l.Acquire(context.Background(), "127.0.0.1"); err != nil {
		t.Fatalf("白名单内应放行: %v", err)
	}
}

func TestMaxPages(t *testing.T) {
	l := newTestLimiter(t, func(c *config.Config) { c.Crawl.MaxPages = 2 })
	for i := 0; i < 2; i++ {
		if err := l.Acquire(context.Background(), "public.test"); err != nil {
			t.Fatalf("第 %d 次应放行: %v", i+1, err)
		}
	}
	if err := l.Acquire(context.Background(), "public.test"); !errors.Is(err, ErrPageLimit) {
		t.Fatalf("第 3 次应触发页面上限，实际 %v", err)
	}
	if l.Pages() != 2 {
		t.Fatalf("Pages()=%d want 2", l.Pages())
	}
}

func TestRateLimitWaits(t *testing.T) {
	l := newTestLimiter(t, func(c *config.Config) { c.Crawl.RatePerSec = 200 }) // 5ms/次
	start := time.Now()
	for i := 0; i < 4; i++ {
		if err := l.Acquire(context.Background(), "public.test"); err != nil {
			t.Fatal(err)
		}
	}
	if elapsed := time.Since(start); elapsed < 10*time.Millisecond {
		t.Fatalf("限速未生效，耗时 %v 过短", elapsed)
	}
}

func TestDisabledLimiterIsNoop(t *testing.T) {
	var l *Limiter
	if l.Enabled() {
		t.Fatal("nil limiter 不应启用")
	}
	if err := l.Acquire(context.Background(), "x"); err != nil {
		t.Fatalf("nil limiter 应直接放行: %v", err)
	}
}
