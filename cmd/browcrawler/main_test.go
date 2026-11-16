package main

import (
	"testing"

	"github.com/winezer0/browcrawler/internal/config"
)

func TestJSChunkFlagsMapIntoConfigOverrides(t *testing.T) {
	cli := &cli{
		NoJSChunks:      true,
		JSChunkMaxDepth: 0, JSChunkMaxCount: 24,
		JSChunkMaxBytes: 1 << 20, JSChunkMaxTotalBytes: 4 << 20,
		JSChunkTimeoutMS: 8000,
	}
	cfg := config.Default()
	cli.toOverrides().Apply(cfg)
	if cfg.Crawl.JSChunks.Enabled || cfg.Crawl.JSChunks.MaxDepth != 0 ||
		cfg.Crawl.JSChunks.MaxCount != 24 || cfg.Crawl.JSChunks.MaxChunkBytes != 1<<20 ||
		cfg.Crawl.JSChunks.MaxTotalBytes != 4<<20 || cfg.Crawl.JSChunks.FetchTimeoutMS != 8000 {
		t.Fatalf("CLI chunk overrides were not applied: %+v", cfg.Crawl.JSChunks)
	}
}

func TestUnspecifiedJSChunkFlagsDoNotOverrideConfig(t *testing.T) {
	flags := &cli{
		JSChunkMaxDepth: -1, JSChunkMaxCount: -1,
		JSChunkMaxBytes: -1, JSChunkMaxTotalBytes: -1, JSChunkTimeoutMS: -1,
	}
	cfg := config.Default()
	cfg.Crawl.JSChunks = config.JSChunksConfig{
		Enabled: true, MaxDepth: 3, MaxCount: 24,
		MaxChunkBytes: 1 << 20, MaxTotalBytes: 4 << 20, FetchTimeoutMS: 9000,
	}
	want := cfg.Crawl.JSChunks
	flags.toOverrides().Apply(cfg)
	if cfg.Crawl.JSChunks != want {
		t.Fatalf("unspecified CLI flags changed config: got %+v want %+v", cfg.Crawl.JSChunks, want)
	}
}
