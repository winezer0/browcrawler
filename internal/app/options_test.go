package app

import (
	"strings"
	"testing"

	"github.com/winezer0/browcrawler/internal/config"
)

func TestOverridesApply(t *testing.T) {
	c := config.Default()
	chunkDepth, chunkCount := 3, 24
	chunkTimeout := 8000
	chunkBytes, totalChunkBytes := int64(1<<20), int64(4<<20)
	Overrides{
		Scope: "any", Depth: 3, Traffic: "t.jsonl", Discovered: "d.jsonl", HAR: "h.har",
		Upstream: "none", Backend: "AUTO", MaxSession: 4, MaxPages: 7, RatePerSec: 2.5,
		SettleMS: 0, NoStealth: true, MergeRoots: true, PerSession: true, KeepDir: true,
		NoJSExtract: true, RawDump: true, MetricsFile: "m.jsonl", RootsFile: "roots.pem",
		Passthrough: "a.com, *.b.com", IPAllow: "127.0.0.0/8", IPDeny: "10.0.0.0/8",
		NoJSChunks: true, JSChunkMaxDepth: &chunkDepth, JSChunkMaxCount: &chunkCount,
		JSChunkMaxBytes: &chunkBytes, JSChunkMaxTotalBytes: &totalChunkBytes,
		JSChunkFetchTimeoutMS: &chunkTimeout,
	}.Apply(c)

	checks := map[string]bool{
		"scope any":       c.Crawl.Scope == "*",
		"depth":           c.Crawl.Depth == 3,
		"traffic":         c.Output.Traffic == "t.jsonl",
		"discovered":      c.Output.Discovered == "d.jsonl",
		"har":             c.Output.HAR == "h.har",
		"upstream none":   c.UpstreamProxy == "",
		"backend lower":   c.BrowserBackend == config.BackendAuto,
		"max session":     c.MaxSession == 4,
		"max pages":       c.Crawl.MaxPages == 7,
		"rate":            c.Crawl.RatePerSec == 2.5,
		"settle":          c.Crawl.SettleMS == 0,
		"no stealth":      !c.Stealth,
		"merge roots":     c.Mitm.MergePublicRoots,
		"per session ca":  c.Mitm.PerSessionCA,
		"keep dir":        c.KeepSessionDir,
		"no js-extract":   !c.Crawl.JSExtract,
		"no js chunks":    !c.Crawl.JSChunks.Enabled,
		"chunk depth":     c.Crawl.JSChunks.MaxDepth == 3,
		"chunk count":     c.Crawl.JSChunks.MaxCount == 24,
		"chunk bytes":     c.Crawl.JSChunks.MaxChunkBytes == 1<<20,
		"chunk total":     c.Crawl.JSChunks.MaxTotalBytes == 4<<20,
		"chunk timeout":   c.Crawl.JSChunks.FetchTimeoutMS == 8000,
		"raw dump":        c.Output.RawDump,
		"metrics enabled": c.Metrics.Enabled && c.Metrics.File == "m.jsonl",
		"roots file":      c.Mitm.PublicRootsFile == "roots.pem",
		"passthrough":     len(c.Mitm.TLSPassthrough) == 2 && c.Mitm.TLSPassthrough[1] == "*.b.com",
		"ip allow":        len(c.Policy.IPAllow) == 1,
		"ip deny":         len(c.Policy.IPDeny) == 1,
	}
	for name, ok := range checks {
		if !ok {
			t.Errorf("覆盖项 %s 未生效", name)
		}
	}
}

func TestZeroOverridesKeepDefaults(t *testing.T) {
	c := config.Default()
	want := *c
	Overrides{Depth: -1, SettleMS: -1}.Apply(c)
	if c.Crawl.Depth != want.Crawl.Depth || c.Crawl.SettleMS != want.Crawl.SettleMS ||
		c.BrowserBackend != want.BrowserBackend || c.MaxSession != want.MaxSession ||
		c.Crawl.JSChunks != want.Crawl.JSChunks {
		t.Fatal("未指定的 flag 不得改变默认配置")
	}
}

func TestEnsureScope(t *testing.T) {
	c := config.Default()
	if err := EnsureScope(c, "https://example.com/a"); err != nil {
		t.Fatal(err)
	}
	if c.Crawl.Scope != "example.com" {
		t.Fatalf("scope=%q want example.com", c.Crawl.Scope)
	}
	c.Crawl.Scope = "*"
	if err := EnsureScope(c, "https://example.com/a"); err != nil || c.Crawl.Scope != "*" {
		t.Fatalf("显式 scope 不应被覆盖: %v / %q", err, c.Crawl.Scope)
	}
	if err := EnsureScope(config.Default(), "not-a-url"); err == nil {
		t.Fatal("非法 seed 应报错")
	}
	if !strings.Contains(splitList(" a , ,b ")[1], "b") {
		t.Fatal("splitList 应去空并 trim")
	}
}
