package policy

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Robots 在爬虫层实现 robots.txt 约束。
//
// 为什么自研：Obscura v0.2.6 的 `--obey-robots` 帮助文本明确写着
// "Global: applies to fetch and scrape"，不含 serve（CDP）模式；实测经 rod 导航时
// 该参数完全不生效（从未请求 /robots.txt，Disallow 路径照样被访问）。
// 因此不能把授权边界托付给后端，改由本包在导航前判定，并走会话 goproxy 以便录制/走上游。
type Robots struct {
	client *http.Client
	mu     sync.Mutex
	cache  map[string]robotRules
}

type robotRule struct {
	path  string
	allow bool
}

type robotRules struct {
	rules []robotRule
	err   error
}

// NewRobots 用会话代理地址构造（robots.txt 请求同样经过 goproxy，可被录制且能走上游代理）。
func NewRobots(proxyAddr string) *Robots {
	transport := &http.Transport{}
	if proxyAddr != "" {
		if u, err := url.Parse(proxyAddr); err == nil {
			transport.Proxy = http.ProxyURL(u)
		}
	}
	return &Robots{
		client: &http.Client{Transport: transport, Timeout: 8 * time.Second},
		cache:  map[string]robotRules{},
	}
}

// Allowed 判断目标 URL 是否允许导航。网络/解析失败时 fail-open（放行）并记录原因，
// 避免单个站点的 robots.txt 不可达就整个任务停摆；决策始终可由日志追溯。
func (r *Robots) Allowed(ctx context.Context, raw string) (bool, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return true, nil
	}
	origin := u.Scheme + "://" + u.Host
	rules := r.forOrigin(ctx, origin)
	if rules.err != nil {
		return true, rules.err
	}
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	if u.RawQuery != "" {
		path += "?" + u.RawQuery
	}
	bestLen, allowed := -1, true
	for _, rule := range rules.rules {
		if !matchPath(path, rule.path) {
			continue
		}
		if len(rule.path) >= bestLen {
			bestLen, allowed = len(rule.path), rule.allow
		}
	}
	return allowed, nil
}

func (r *Robots) forOrigin(ctx context.Context, origin string) robotRules {
	r.mu.Lock()
	if v, ok := r.cache[origin]; ok {
		r.mu.Unlock()
		return v
	}
	r.mu.Unlock()

	res := robotRules{}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, origin+"/robots.txt", nil)
	if err != nil {
		res.err = err
	} else {
		resp, err := r.client.Do(req)
		if err != nil {
			res.err = fmt.Errorf("fetch robots.txt: %w", err)
		} else {
			defer resp.Body.Close()
			if resp.StatusCode >= 400 {
				// 4xx/5xx 按约定视为「无限制」。
				res.rules = nil
			} else {
				res.rules, res.err = parseRobots(resp)
			}
		}
	}
	r.mu.Lock()
	r.cache[origin] = res
	r.mu.Unlock()
	return res
}

func parseRobots(resp *http.Response) ([]robotRule, error) {
	scanner := bufio.NewScanner(resp.Body)
	var out []robotRule
	applicable := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key, value = strings.ToLower(strings.TrimSpace(key)), strings.TrimSpace(value)
		switch key {
		case "user-agent":
			if value == "*" || strings.Contains(strings.ToLower(value), "browcrawler") {
				applicable = true
			} else if applicable {
				// 已结束通用组、进入其他 UA 组：停止收集（简化但保守）。
				applicable = false
			}
		case "disallow", "allow":
			if !applicable || value == "" {
				continue
			}
			out = append(out, robotRule{path: value, allow: key == "allow"})
		}
	}
	if err := scanner.Err(); err != nil {
		return out, err
	}
	return out, nil
}

func matchPath(path, rule string) bool {
	if rule == "/" {
		return true
	}
	// 支持常见通配符 * 与结尾 $，其余按前缀匹配。
	anchor := strings.HasSuffix(rule, "$")
	rule = strings.TrimSuffix(rule, "$")
	if strings.Contains(rule, "*") {
		parts := strings.Split(rule, "*")
		pos := 0
		for i, p := range parts {
			if p == "" {
				continue
			}
			idx := strings.Index(path[pos:], p)
			if idx < 0 {
				return false
			}
			pos += idx + len(p)
			if i == 0 && idx != 0 {
				return false
			}
		}
		return !anchor || pos == len(path)
	}
	if anchor {
		return path == rule
	}
	return strings.HasPrefix(path, rule)
}
