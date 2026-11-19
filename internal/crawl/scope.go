// scope.go：爬取导航与交付物共用的作用域判定（katana 语义五档）。
// 双层生效：导航层决定是否入队/跟随；交付层（discovered/requests/report）
// 默认过滤域外；JSONL 录制层保持全量并附 InScope 标记（只读原则不破）。
package crawl

import (
	"fmt"
	"net"
	"regexp"
	"strings"

	"github.com/winezer0/basecrawler"

	"github.com/winezer0/browcrawler/internal/config"
)

// ScopeModes 是合法的作用域档位。
const (
	ScopeFQDN  = "fqdn"  // 精确主机（seed 主机）
	ScopeRDN   = "rdn"   // 根域 + 所有子域（katana 默认语义）
	ScopeDNK   = "dnk"   // 域名关键字（host 含该子串）
	ScopeRegex = "regex" // 自定义正则（匹配 host）
	ScopeAll   = "all"   // 不限域（原 -scope any）
)

// ScopeFromCfg 从配置构造作用域判定函数。
//   - cfg.ScopeMode 非空：按五档解析（fqdn/rdn 取 cfg.Scope，dnk/regex 取
//     cfg.ScopeValue）；rdn/fqdn 的 Scope 支持逗号分隔多域。
//   - cfg.ScopeMode 为空：legacy 兼容——Scope 为空或 "*" → all；"re:" 前缀
//     → regex；其余 → rdn（与旧 newScope 语义一致）。
func ScopeFromCfg(c config.CrawlConfig) (func(string) bool, error) {
	mode := strings.ToLower(strings.TrimSpace(c.ScopeMode))
	value := strings.TrimSpace(c.ScopeValue)
	legacy := strings.TrimSpace(c.Scope)

	if mode == "" {
		switch {
		case legacy == "" || legacy == "*":
			mode = ScopeAll
		case strings.HasPrefix(legacy, "re:"):
			mode, value = ScopeRegex, strings.TrimPrefix(legacy, "re:")
		default:
			mode, value = ScopeRDN, legacy
		}
	}

	switch mode {
	case ScopeAll:
		return func(string) bool { return true }, nil

	case ScopeFQDN:
		hosts, err := scopeEntries(value, legacy)
		if err != nil {
			return nil, err
		}
		return func(host string) bool {
			h := strings.ToLower(host)
			for _, d := range hosts {
				if h == d {
					return true
				}
			}
			return false
		}, nil

	case ScopeRDN:
		roots, err := scopeEntries(value, legacy)
		if err != nil {
			return nil, err
		}
		roots = mapSlice(roots, rootDomain)
		return func(host string) bool {
			h := strings.ToLower(host)
			for _, r := range roots {
				if h == r || strings.HasSuffix(h, "."+r) {
					return true
				}
			}
			return false
		}, nil

	case ScopeDNK:
		kw := strings.ToLower(value)
		if kw == "" {
			kw = strings.ToLower(legacy)
		}
		if kw == "" {
			return nil, fmt.Errorf("scope_mode=dnk 需要 scope_value 关键字")
		}
		return func(host string) bool {
			return strings.Contains(strings.ToLower(host), kw)
		}, nil

	case ScopeRegex:
		if value == "" {
			return nil, fmt.Errorf("scope_mode=regex 需要 scope_value 正则")
		}
		rx, err := regexp.Compile(value)
		if err != nil {
			return nil, fmt.Errorf("scope_value 正则非法: %w", err)
		}
		return rx.MatchString, nil

	default:
		return nil, fmt.Errorf("scope_mode 非法: %q（可选 fqdn|rdn|dnk|regex|all）", mode)
	}
}

// BasecrawlerScope translates browcrawler's host-scope rules into the URL
// matcher understood by basecrawler. Keeping the same rule here prevents the
// static pass from fetching hosts that the browser pass would reject.
func BasecrawlerScope(c config.CrawlConfig) (basecrawler.Scope, *regexp.Regexp, error) {
	if _, err := ScopeFromCfg(c); err != nil {
		return 0, nil, err
	}
	mode := strings.ToLower(strings.TrimSpace(c.ScopeMode))
	value := strings.TrimSpace(c.ScopeValue)
	legacy := strings.TrimSpace(c.Scope)
	if mode == "" {
		switch {
		case legacy == "" || legacy == "*":
			mode = ScopeAll
		case strings.HasPrefix(legacy, "re:"):
			mode, value = ScopeRegex, strings.TrimPrefix(legacy, "re:")
		default:
			mode = ScopeRDN
		}
	}
	if mode == ScopeAll {
		return basecrawler.ScopeAny, nil, nil
	}

	var hostPattern string
	switch mode {
	case ScopeFQDN:
		hosts, err := scopeEntries(value, legacy)
		if err != nil {
			return 0, nil, err
		}
		patterns := make([]string, 0, len(hosts))
		for _, host := range hosts {
			patterns = append(patterns, baseHostPattern(host))
		}
		hostPattern = "(?:" + strings.Join(patterns, "|") + ")"
	case ScopeRDN:
		roots, err := scopeEntries(value, legacy)
		if err != nil {
			return 0, nil, err
		}
		patterns := make([]string, 0, len(roots))
		for _, root := range roots {
			patterns = append(patterns, `(?:[^./:]+\.)*`+baseHostPattern(rootDomain(root)))
		}
		hostPattern = "(?:" + strings.Join(patterns, "|") + ")"
	case ScopeDNK:
		if value == "" {
			value = legacy
		}
		if value == "" {
			return 0, nil, fmt.Errorf("scope_mode=dnk 需要 scope_value 关键字")
		}
		hostPattern = `[^/?#:]*` + regexp.QuoteMeta(strings.ToLower(value)) + `[^/?#:]*`
	case ScopeRegex:
		if value == "" {
			return 0, nil, fmt.Errorf("scope_mode=regex 需要 scope_value 正则")
		}
		// ScopeFromCfg validates this expression against hostnames. Remove only
		// outer anchors so it can be embedded between the URL authority markers.
		hostPattern = strings.TrimPrefix(value, "^")
		hostPattern = strings.TrimSuffix(hostPattern, "$")
	default:
		return 0, nil, fmt.Errorf("scope_mode 非法: %q", mode)
	}

	pattern := `(?i)^https?://(?:[^/?#@]+@)?(?:` + hostPattern + `)(?::[0-9]+)?(?:[/?#]|$)`
	if mode == ScopeRegex {
		pattern = `^https?://(?:[^/?#@]+@)?(?:` + hostPattern + `)(?::[0-9]+)?(?:[/?#]|$)`
	}
	rx, err := regexp.Compile(pattern)
	if err != nil {
		return 0, nil, fmt.Errorf("compile basecrawler scope: %w", err)
	}
	return basecrawler.ScopeRegex, rx, nil
}

func baseHostPattern(host string) string {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if ip := net.ParseIP(host); ip != nil && strings.Contains(host, ":") {
		return `\[` + regexp.QuoteMeta(host) + `\]`
	}
	return regexp.QuoteMeta(host)
}

// scopeEntries 解析作用域主机列表：优先 ScopeValue，空则回退 legacy Scope
// （逗号分隔）。fqdn/rdn 模式下二者皆空时由调用方保证 EnsureScope 已填 seed 主机。
func scopeEntries(value, legacy string) ([]string, error) {
	s := value
	if s == "" {
		s = legacy
	}
	if s == "" || s == "*" {
		return nil, fmt.Errorf("scope_mode 需要 scope_value 或显式 -scope 主机列表")
	}
	var out []string
	for _, d := range strings.Split(s, ",") {
		if d = strings.ToLower(strings.TrimSpace(d)); d != "" {
			out = append(out, d)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("scope 主机列表为空")
	}
	return out, nil
}

// rootDomain 取根域（最后两段标签；多级 TLD 如 co.uk 需显式传根域，不做 PSL）。
func rootDomain(host string) string {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	parts := strings.Split(host, ".")
	if len(parts) <= 2 {
		return host
	}
	return parts[len(parts)-2] + "." + parts[len(parts)-1]
}

func mapSlice(in []string, f func(string) string) []string {
	out := make([]string, len(in))
	for i, v := range in {
		out[i] = f(v)
	}
	return out
}
