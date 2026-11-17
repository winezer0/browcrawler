package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// EndpointReport 是报告里的一行：一个「端点 + 参数形状」（值不敏感），
// 与 requestfilter CoverageParameters 的分组语义对齐，便于直接按形状送扫描。
type EndpointReport struct {
	Method     string   `json:"method"`
	Host       string   `json:"host"`
	Path       string   `json:"path"`
	Params     []string `json:"params"`      // 形如 "query:id"、"form:user"、"path_segment:seg1"
	Statuses   []int    `json:"statuses"`    // 该形状观察到的状态码集合
	Count      int      `json:"count"`       // 该形状出现的请求数
	ExampleURL string   `json:"example_url"` // 该形状中第一条请求的完整 URL
	HasBody    bool     `json:"has_body"`    // 是否观察到请求体
	// AuthGroups 是覆盖该端点形状的认证上下文标签（Cookie/Authorization 的
	// SHA-256 前 8 位 hex，凭据原文不落盘）。多凭据爬取时，组间「独有端点」
	// 即垂直越权候选；同形状被多组覆盖则记录全部组标签。
	AuthGroups []string `json:"auth_groups"`
}

type ReportStats struct {
	Requests        int            `json:"requests"`         // 录制请求总数（含重复形状）
	Endpoints       int            `json:"endpoints"`        // 形状分组数
	Connects        int            `json:"connect_tunnels"`  // CONNECT 隧道数（未解密）
	WSSessions      int            `json:"ws_sessions"`      // WebSocket 会话记录数（created+handshake+帧+closed）
	StatusHistogram map[string]int `json:"status_histogram"` // "2xx"/"3xx"/"4xx"/"5xx"/"0" 计数
}

type CrawlReport struct {
	Completion *ScanCompletion  `json:"completion,omitempty"`
	Stats      ReportStats      `json:"stats"`
	Endpoints  []EndpointReport `json:"endpoints"`
}

// ExportReport 生成 crawl-report.json：把录制请求按「方法+主机+路径+参数形状」
// 分组汇总，作为安全检查交付的结构化索引（与 requests.txt 互补）。
func (r *Recorder) ExportReport(path string) error {
	r.mu.Lock()
	records := append([]Record(nil), r.records...)
	completion := r.reportCompletion()
	r.mu.Unlock()

	stats := ReportStats{Requests: len(records), StatusHistogram: map[string]int{}}
	groups := map[string]*EndpointReport{}
	order := []string{}
	for _, rec := range records {
		if !r.inScope(rec) {
			continue // 双层生效：形状报告默认只留域内
		}
		if rec.Method == "CONNECT" {
			stats.Connects++
			continue
		}
		stats.StatusHistogram[statusBucket(rec.Status)]++
		u, err := url.Parse(rec.URL)
		if err != nil {
			continue
		}
		key, params := shapeKey(rec)
		group, exists := groups[key]
		if !exists {
			group = &EndpointReport{
				Method: rec.Method, Host: u.Host, Path: u.Path,
				Params: params, ExampleURL: rec.URL,
			}
			groups[key] = group
			order = append(order, key)
		}
		group.Count++
		if rec.Status != 0 && !containsInt(group.Statuses, rec.Status) {
			group.Statuses = append(group.Statuses, rec.Status)
			sort.Ints(group.Statuses)
		}
		if rec.RequestBody != "" {
			group.HasBody = true
		}
		if label := authLabel(rec); !containsStr(group.AuthGroups, label) {
			group.AuthGroups = append(group.AuthGroups, label)
			sort.Strings(group.AuthGroups)
		}
	}

	report := CrawlReport{Stats: stats, Endpoints: make([]EndpointReport, 0, len(order))}
	report.Completion = completion
	stats.Endpoints = len(order)
	for _, key := range order {
		report.Endpoints = append(report.Endpoints, *groups[key])
	}
	sort.Slice(report.Endpoints, func(i, j int) bool {
		a, b := report.Endpoints[i], report.Endpoints[j]
		if a.Host != b.Host {
			return a.Host < b.Host
		}
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		return a.Method < b.Method
	})
	report.Stats = stats

	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func statusBucket(status int) string {
	switch {
	case status == 0:
		return "0"
	case status < 300:
		return "2xx"
	case status < 400:
		return "3xx"
	case status < 500:
		return "4xx"
	default:
		return "5xx"
	}
}

func containsInt(list []int, v int) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}

// shapeKey 计算端点形状 key 与参数清单：方法 + 主机 + 路径 + 参数形状（值不敏感）。
// 与 requestfilter CoverageParameters 的分组语义对齐，报告分组与
// 交付侧过滤共用，保证两份交付物的形状口径一致。
func shapeKey(rec Record) (string, []string) {
	u, err := url.Parse(rec.URL)
	if err != nil {
		return rec.Method + "\x00" + rec.URL, nil
	}
	params := []string{}
	if q := u.Query(); len(q) > 0 {
		names := make([]string, 0, len(q))
		for name := range q {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			params = append(params, "query:"+name)
		}
	}
	for _, name := range sortedKeys(rec.RequestHeaders) {
		if strings.EqualFold(name, "Content-Type") &&
			strings.Contains(strings.ToLower(rec.RequestHeaders.Get(name)), "form-urlencoded") {
			params = append(params, "form:<body>")
		}
	}
	if rec.RequestBody != "" {
		params = append(params, "body:<captured>")
	}
	sort.Strings(params)
	return rec.Method + "\x00" + u.Host + "\x00" + u.Path + "\x00" + strings.Join(params, ","), params
}

// authLabel 为单条请求派生稳定的认证上下文标签（Cookie/Authorization 的
// SHA-256 前 8 位 hex，凭据原文不落盘）；无认证头时为 anonymous。
func authLabel(rec Record) string {
	parts := make([]string, 0, 2)
	if v := rec.RequestHeaders.Get("Cookie"); v != "" {
		parts = append(parts, "cookie:"+v)
	}
	if v := rec.RequestHeaders.Get("Authorization"); v != "" {
		parts = append(parts, "auth:"+v)
	}
	if len(parts) == 0 {
		return "anonymous"
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "auth-" + hex.EncodeToString(sum[:4])
}

func containsStr(list []string, v string) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}
