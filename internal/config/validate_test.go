package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultIsValid(t *testing.T) {
	if err := Default().Validate(); err != nil {
		t.Fatalf("默认配置必须可直接使用: %v", err)
	}
}

func TestDefaultJSChunkBudgets(t *testing.T) {
	chunks := Default().Crawl.JSChunks
	if !chunks.Enabled || chunks.MaxDepth != 2 || chunks.MaxCount != 16 ||
		chunks.MaxChunkBytes != 2<<20 || chunks.MaxTotalBytes != 8<<20 || chunks.FetchTimeoutMS != 15000 {
		t.Fatalf("unexpected JS chunk defaults: %+v", chunks)
	}
}

func TestDefaultBasecrawlerBudget(t *testing.T) {
	c := Default().Crawl
	if !c.BasecrawlerEnabled || c.BasecrawlerMaxURLs != 200 {
		t.Fatalf("unexpected basecrawler defaults: enabled=%t max_urls=%d", c.BasecrawlerEnabled, c.BasecrawlerMaxURLs)
	}
}

func TestValidateBackend(t *testing.T) {
	for _, b := range []string{BackendObscura, BackendChrome, BackendAuto} {
		c := Default()
		c.BrowserBackend = b
		if err := c.Validate(); err != nil {
			t.Errorf("backend=%s 应合法: %v", b, err)
		}
	}
	for _, b := range []string{"", "firefox", "OBSCURA_X", "webkit"} {
		c := Default()
		c.BrowserBackend = b
		if err := c.Validate(); err == nil {
			t.Errorf("backend=%q 应被拒绝", b)
		}
	}
}

func TestValidateNoSilentPhantoms(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Config)
	}{
		{"max_session=0", func(c *Config) { c.MaxSession = 0 }},
		{"max_body_size=0", func(c *Config) { c.MaxBodySize = 0 }},
		{"negative depth", func(c *Config) { c.Crawl.Depth = -1 }},
		{"zero enabled basecrawler budget", func(c *Config) { c.Crawl.BasecrawlerMaxURLs = 0 }},
		{"negative js chunk depth", func(c *Config) { c.Crawl.JSChunks.MaxDepth = -1 }},
		{"js chunk depth above hard limit", func(c *Config) { c.Crawl.JSChunks.MaxDepth = maxJSChunkDepth + 1 }},
		{"negative js chunk count", func(c *Config) { c.Crawl.JSChunks.MaxCount = -1 }},
		{"zero enabled js chunk count", func(c *Config) { c.Crawl.JSChunks.MaxCount = 0 }},
		{"js chunk bytes above hard limit", func(c *Config) { c.Crawl.JSChunks.MaxChunkBytes = maxJSChunkBytes + 1 }},
		{"js total smaller than per chunk", func(c *Config) { c.Crawl.JSChunks.MaxTotalBytes = c.Crawl.JSChunks.MaxChunkBytes - 1 }},
		{"negative js chunk timeout", func(c *Config) { c.Crawl.JSChunks.FetchTimeoutMS = -1 }},
		{"js chunk timeout above hard limit", func(c *Config) { c.Crawl.JSChunks.FetchTimeoutMS = maxJSChunkTimeoutMS + 1 }},
		{"zero enabled js chunk timeout", func(c *Config) { c.Crawl.JSChunks.FetchTimeoutMS = 0 }},
		{"js chunk timeout reaches browser evaluation timeout", func(c *Config) { c.Crawl.JSChunks.FetchTimeoutMS = c.Timeout.NavigationMS }},
		{"negative rate", func(c *Config) { c.Crawl.RatePerSec = -1 }},
		{"zero navigation timeout", func(c *Config) { c.Timeout.NavigationMS = 0 }},
		{"zero health interval", func(c *Config) { c.Pool.HealthIntervalMS = 0 }},
		{"bad CIDR", func(c *Config) { c.Policy.IPAllow = []string{"999.1.1.1/8"} }},
		{"merge roots without file", func(c *Config) { c.Mitm.MergePublicRoots = true }},
		{"metrics without file", func(c *Config) { c.Metrics.Enabled = true; c.Metrics.File = "" }},
		{"passthrough with slash", func(c *Config) { c.Mitm.TLSPassthrough = []string{"a.com/x"} }},
		{"passthrough without public roots", func(c *Config) { c.Mitm.TLSPassthrough = []string{"a.com"} }},
	}
	for _, tc := range cases {
		c := Default()
		tc.mutate(c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s 应被明确拒绝，不能静默忽略", tc.name)
		}
	}
	disabled := Default()
	disabled.Crawl.BasecrawlerEnabled = false
	disabled.Crawl.BasecrawlerMaxURLs = 0
	if err := disabled.Validate(); err != nil {
		t.Fatalf("disabled basecrawler may omit its URL budget: %v", err)
	}
}

func TestLoadJSChunkConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := `crawl:
  js_chunks:
    enabled: false
    max_depth: 3
    max_count: 24
    max_chunk_bytes: 1048576
    max_total_bytes: 4194304
    fetch_timeout_ms: 9000
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	want := JSChunksConfig{
		Enabled: false, MaxDepth: 3, MaxCount: 24,
		MaxChunkBytes: 1 << 20, MaxTotalBytes: 4 << 20, FetchTimeoutMS: 9000,
	}
	if cfg.Crawl.JSChunks != want {
		t.Fatalf("loaded JS chunk config = %+v, want %+v", cfg.Crawl.JSChunks, want)
	}
}

func TestExampleConfigUsesValidatedJSChunkDefaults(t *testing.T) {
	cfg, err := Load(filepath.Join("..", "..", "config.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	want := JSChunksConfig{
		Enabled: true, MaxDepth: 2, MaxCount: 16,
		MaxChunkBytes: 2 << 20, MaxTotalBytes: 8 << 20, FetchTimeoutMS: 15000,
	}
	if cfg.Crawl.JSChunks != want {
		t.Fatalf("example JS chunk config = %+v, want %+v", cfg.Crawl.JSChunks, want)
	}
}

func TestMergeRootsRequiresRealFile(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "roots.pem")
	if err := os.WriteFile(good, []byte("placeholder"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := Default()
	c.Mitm.MergePublicRoots = true
	c.Mitm.PublicRootsFile = good
	if err := c.Validate(); err != nil {
		t.Fatalf("存在文件时校验应通过: %v", err)
	}
	c.Mitm.TLSPassthrough = []string{"pinned.example.com"}
	if err := c.Validate(); err != nil {
		t.Fatalf("passthrough + 公共根应合法: %v", err)
	}
	chrome := Default()
	chrome.BrowserBackend = BackendChrome
	chrome.Mitm.TLSPassthrough = []string{"pinned.example.com"}
	if err := chrome.Validate(); err != nil {
		t.Fatalf("Chrome 后端跳过证书校验，无需公共根: %v", err)
	}
	c.Mitm.PublicRootsFile = filepath.Join(dir, "missing.pem")
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "public_roots_file") {
		t.Fatalf("缺失文件应明确报错，实际: %v", err)
	}
}

func TestParseCIDRBareIP(t *testing.T) {
	for _, s := range []string{"127.0.0.1", "10.0.0.0/8", "::1", "2001:db8::/32"} {
		n, err := parseCIDR(s)
		if err != nil || n == nil {
			t.Errorf("parseCIDR(%q) 失败: %v", s, err)
		}
	}
}
