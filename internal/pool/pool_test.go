package pool

import (
	"context"
	"io"
	"log"
	"testing"

	"browcrawler/internal/browserrunner"
	"browcrawler/internal/config"
	"browcrawler/internal/session"
)

func newTestPool(t *testing.T, max int) *Pool {
	t.Helper()
	cfg := config.Default()
	cfg.MaxSession = max
	cfg.Pool.HealthIntervalMS = 50
	p, err := New(Options{
		Cfg: cfg, Logger: log.New(io.Discard, "", 0),
		Output: session.OutputPaths{
			Traffic: "out/traffic.jsonl", Discovered: "out/discovered.jsonl", HAR: "out/traffic.har",
		},
		Browser: func(*config.Config, string, string, string, *log.Logger) (browserrunner.Runner, error) {
			return nil, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.CloseAll(context.Background()) })
	return p
}

func TestPathsSingleVsMultiSession(t *testing.T) {
	single := newTestPool(t, 1).paths("s-1")
	if single.Traffic != "out/traffic.jsonl" {
		t.Fatalf("单会话应保持原路径，实际 %q", single.Traffic)
	}
	multi := newTestPool(t, 3).paths("s-abc")
	if multi.Traffic != "out/traffic-s-abc.jsonl" || multi.Discovered != "out/discovered-s-abc.jsonl" ||
		multi.HAR != "out/traffic-s-abc.har" {
		t.Fatalf("多会话路径必须按 sessionID 隔离: %+v", multi)
	}
}

func TestWithSuffixEmpty(t *testing.T) {
	if withSuffix("", "x") != "" {
		t.Fatal("空路径不应产生后缀")
	}
}

func TestCloseAllIsIdempotent(t *testing.T) {
	p := newTestPool(t, 2)
	if err := p.CloseAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := p.CloseAll(context.Background()); err != nil {
		t.Fatalf("二次关闭应幂等: %v", err)
	}
	st := p.Stats()
	if st["total"] != 0 || st["max"] != 2 {
		t.Fatalf("关闭后状态异常: %+v", st)
	}
}
