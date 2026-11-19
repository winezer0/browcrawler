package config

import (
	"fmt"
	"net"
	"os"
	"strings"
)

const (
	maxJSChunkDepth      = 5
	maxJSChunkCount      = 128
	maxJSChunkBytes      = 16 << 20
	maxJSChunkTotalBytes = 64 << 20
	maxJSChunkTimeoutMS  = 120000
)

// Validate 是配置层的唯一校验入口，纯函数（只读文件系统以确认外部依赖文件存在）。
// 目标：任何「写了却不会生效」的取值都在启动前明确报错，而不是静默忽略。
func (c *Config) Validate() error {
	if err := c.validateBackend(); err != nil {
		return err
	}
	if err := c.validateScalars(); err != nil {
		return err
	}
	if err := c.validateMitm(); err != nil {
		return err
	}
	if err := c.validatePolicy(); err != nil {
		return err
	}
	return nil
}

func (c *Config) validateBackend() error {
	switch strings.ToLower(strings.TrimSpace(c.BrowserBackend)) {
	case BackendObscura, BackendChrome, BackendAuto:
		c.BrowserBackend = strings.ToLower(strings.TrimSpace(c.BrowserBackend))
	default:
		return fmt.Errorf("browser_backend=%q 不支持（可选 %s/%s/%s）",
			c.BrowserBackend, BackendObscura, BackendChrome, BackendAuto)
	}
	if c.BrowserBackend != BackendChrome && strings.TrimSpace(c.ObscuraBin) == "" {
		return fmt.Errorf("obscura_bin 不能为空（backend=%s）", c.BrowserBackend)
	}
	if c.BrowserBackend != BackendObscura && strings.TrimSpace(c.ChromeBin) == "" {
		return fmt.Errorf("chrome_bin 不能为空（backend=%s）", c.BrowserBackend)
	}
	return nil
}

func (c *Config) validateScalars() error {
	if c.MaxSession < 1 {
		return fmt.Errorf("max_session=%d 必须 >= 1", c.MaxSession)
	}
	if c.MaxBodySize <= 0 {
		return fmt.Errorf("max_body_size=%d 必须 > 0", c.MaxBodySize)
	}
	if c.Crawl.Depth < 0 {
		return fmt.Errorf("crawl.depth=%d 必须 >= 0", c.Crawl.Depth)
	}
	if c.Crawl.MaxPages < 0 {
		return fmt.Errorf("crawl.max_pages=%d 必须 >= 0", c.Crawl.MaxPages)
	}
	if c.Crawl.BasecrawlerEnabled && c.Crawl.BasecrawlerMaxURLs < 1 {
		return fmt.Errorf("crawl.basecrawler_max_urls=%d 必须 >= 1 when basecrawler_enabled", c.Crawl.BasecrawlerMaxURLs)
	}
	if c.Crawl.RatePerSec < 0 {
		return fmt.Errorf("crawl.rate_per_sec=%v 必须 >= 0", c.Crawl.RatePerSec)
	}
	if c.Crawl.SettleMS < 0 {
		return fmt.Errorf("crawl.settle_ms=%d 必须 >= 0", c.Crawl.SettleMS)
	}
	if err := c.validateJSChunks(); err != nil {
		return err
	}
	for name, ms := range map[string]int{
		"cdp_ready_ms": c.Timeout.CDPReadyMS, "navigation_ms": c.Timeout.NavigationMS,
		"shutdown_ms": c.Timeout.ShutdownMS, "task_ms": c.Timeout.TaskMS,
	} {
		if ms <= 0 {
			return fmt.Errorf("timeout.%s=%d 必须 > 0", name, ms)
		}
	}
	if c.Pool.HealthIntervalMS <= 0 {
		return fmt.Errorf("pool.health_interval_ms=%d 必须 > 0", c.Pool.HealthIntervalMS)
	}
	if c.Pool.ReuseLimit < 0 || c.Pool.MaxIdleMS < 0 {
		return fmt.Errorf("pool.reuse_limit / max_idle_ms 不能为负数")
	}
	if c.Metrics.Enabled && strings.TrimSpace(c.Metrics.File) == "" {
		return fmt.Errorf("metrics.enabled=true 时 metrics.file 不能为空")
	}
	return nil
}

func (c *Config) validateJSChunks() error {
	chunks := c.Crawl.JSChunks
	if chunks.MaxDepth < 0 || chunks.MaxDepth > maxJSChunkDepth {
		return fmt.Errorf("crawl.js_chunks.max_depth=%d 必须在 0..%d", chunks.MaxDepth, maxJSChunkDepth)
	}
	if chunks.MaxCount < 0 || chunks.MaxCount > maxJSChunkCount {
		return fmt.Errorf("crawl.js_chunks.max_count=%d 必须在 0..%d", chunks.MaxCount, maxJSChunkCount)
	}
	if chunks.MaxChunkBytes < 0 || chunks.MaxChunkBytes > maxJSChunkBytes {
		return fmt.Errorf("crawl.js_chunks.max_chunk_bytes=%d 必须在 0..%d", chunks.MaxChunkBytes, maxJSChunkBytes)
	}
	if chunks.MaxTotalBytes < 0 || chunks.MaxTotalBytes > maxJSChunkTotalBytes {
		return fmt.Errorf("crawl.js_chunks.max_total_bytes=%d 必须在 0..%d", chunks.MaxTotalBytes, maxJSChunkTotalBytes)
	}
	if chunks.FetchTimeoutMS < 0 || chunks.FetchTimeoutMS > maxJSChunkTimeoutMS {
		return fmt.Errorf("crawl.js_chunks.fetch_timeout_ms=%d 必须在 0..%d", chunks.FetchTimeoutMS, maxJSChunkTimeoutMS)
	}
	if chunks.Enabled {
		if chunks.MaxCount == 0 || chunks.MaxChunkBytes == 0 || chunks.MaxTotalBytes == 0 || chunks.FetchTimeoutMS == 0 {
			return fmt.Errorf("crawl.js_chunks 启用时 max_count、max_chunk_bytes、max_total_bytes、fetch_timeout_ms 必须大于 0")
		}
		if c.Timeout.NavigationMS > 0 && chunks.FetchTimeoutMS >= c.Timeout.NavigationMS {
			return fmt.Errorf("crawl.js_chunks.fetch_timeout_ms 必须小于 timeout.navigation_ms（浏览器求值超时）")
		}
		if chunks.MaxChunkBytes > chunks.MaxTotalBytes {
			return fmt.Errorf("crawl.js_chunks.max_chunk_bytes 不能大于 max_total_bytes")
		}
	}
	return nil
}

// PolicyEnabled 表示是否启用了任意需要注入策略的能力，供编排层判断。
func (c *Config) PolicyEnabled() bool {
	return c.Crawl.MaxPages > 0 || c.Crawl.RatePerSec > 0 ||
		len(c.Policy.IPAllow) > 0 || len(c.Policy.IPDeny) > 0 || len(c.Mitm.TLSPassthrough) > 0
}

func (c *Config) validateMitm() error {
	if strings.TrimSpace(c.Mitm.CACrt) == "" || strings.TrimSpace(c.Mitm.CAKey) == "" {
		return fmt.Errorf("mitm.ca_crt / ca_key 不能为空")
	}
	if c.Mitm.MergePublicRoots {
		f := strings.TrimSpace(c.Mitm.PublicRootsFile)
		if f == "" {
			return fmt.Errorf("mitm.merge_public_roots=true 必须显式指定 mitm.public_roots_file（PEM 格式公共根合集）；" +
				"Windows 系统证书库无法可靠导出完整证书，不能静默降级为空合集")
		}
		if _, err := os.Stat(f); err != nil {
			return fmt.Errorf("mitm.public_roots_file=%q 不可读: %w", f, err)
		}
	}
	switch strings.ToLower(strings.TrimSpace(c.Crawl.ScopeMode)) {
	case "", "fqdn", "rdn", "dnk", "regex", "all":
	default:
		return fmt.Errorf("crawl.scope_mode 非法: %q（可选 fqdn|rdn|dnk|regex|all）", c.Crawl.ScopeMode)
	}
	if strings.EqualFold(strings.TrimSpace(c.Crawl.ScopeMode), "regex") &&
		strings.TrimSpace(c.Crawl.ScopeValue) == "" {
		return fmt.Errorf("crawl.scope_mode=regex 需要 scope_value")
	}
	if strings.EqualFold(strings.TrimSpace(c.Crawl.ScopeMode), "dnk") &&
		strings.TrimSpace(c.Crawl.ScopeValue) == "" && strings.TrimSpace(c.Crawl.Scope) == "" {
		return fmt.Errorf("crawl.scope_mode=dnk 需要 scope_value 或 -scope 主机")
	}
	for _, h := range c.Mitm.TLSPassthrough {
		if h = strings.TrimSpace(h); h == "" || strings.ContainsAny(h, " \t/") {
			return fmt.Errorf("mitm.tls_passthrough 含非法域名 %q", h)
		}
	}
	// 实测（2026-11-08）：passthrough 隧道里是真实站点证书，而 stealth 通道的
	// SSL_CERT_FILE 只有自签 CA，单独使用会导航失败；Chrome 后端用
	// --ignore-certificate-errors 跳过校验，不受此限制。
	if len(c.Mitm.TLSPassthrough) > 0 && !c.Mitm.MergePublicRoots && c.BrowserBackend != BackendChrome {
		return fmt.Errorf("mitm.tls_passthrough 需同时开启 merge_public_roots 并指定 mitm.public_roots_file" +
			"（隧道内为真实证书，否则 stealth 信任库只有自签 CA 会校验失败）")
	}
	return nil
}

func (c *Config) validatePolicy() error {
	for _, list := range []struct {
		name  string
		items []string
	}{{"policy.ip_allow", c.Policy.IPAllow}, {"policy.ip_deny", c.Policy.IPDeny}} {
		for _, s := range list.items {
			if _, err := parseCIDR(s); err != nil {
				return fmt.Errorf("%s 含非法 CIDR/IP %q: %w", list.name, s, err)
			}
		}
	}
	return nil
}

// parseCIDR 兼容「裸 IP」写法，统一转成 *net.IPNet。
func parseCIDR(s string) (*net.IPNet, error) {
	s = strings.TrimSpace(s)
	if ip := net.ParseIP(s); ip != nil {
		bits := 128
		if ip.To4() != nil {
			bits = 32
		}
		return &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)}, nil
	}
	_, n, err := net.ParseCIDR(s)
	return n, err
}
