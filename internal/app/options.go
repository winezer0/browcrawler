package app

import (
	"strings"

	"github.com/winezer0/browcrawler/internal/config"
)

// Overrides 是 CLI flag 的结构化投影；-1 / 空串 / false 表示「未指定」。
// Apply 是纯函数，唯一职责是把覆盖值写进 Config，便于单元测试且不触碰运行时。
type Overrides struct {
	Scope                 string
	Depth                 int
	Traffic               string
	Discovered            string
	HAR                   string
	Upstream              string
	NoStealth             bool
	ObeyRobots            bool
	MergeRoots            bool
	NoForms               bool
	Requests              string
	Report                string
	RequestsFiltered      string
	SaveJS                bool
	ScopeMode             string
	NoRuntimeRoute        bool
	NoWSCapture           bool
	NoSafety              bool
	NoBasecrawler         bool
	BasecrawlerMaxURLs    int
	FeedPath              string
	DeliverOutScope       bool
	ScopeValue            string
	Resume                bool
	NavRetries            int
	MaxInteractions       int
	HostRate              float64
	AuthFile              string
	Creds                 string
	PerSession            bool
	KeepDir               bool
	SettleMS              int
	NoJSExtract           bool
	NoJSChunks            bool
	JSChunkMaxDepth       *int
	JSChunkMaxCount       *int
	JSChunkMaxBytes       *int64
	JSChunkMaxTotalBytes  *int64
	JSChunkFetchTimeoutMS *int
	Backend               string
	MaxSession            int
	MaxPages              int
	RatePerSec            float64
	RawDump               bool
	MetricsFile           string
	RootsFile             string
	Passthrough           string
	IPAllow               string
	IPDeny                string
	Concurrency           int
	ExtraSeeds            string
}

func (o Overrides) Apply(c *config.Config) {
	if s := strings.TrimSpace(o.Scope); s != "" {
		if strings.EqualFold(s, "any") {
			c.Crawl.Scope = "*"
		} else {
			c.Crawl.Scope = s
		}
	}
	if o.Depth >= 0 {
		c.Crawl.Depth = o.Depth
	}
	if o.Traffic != "" {
		c.Output.Traffic = o.Traffic
	}
	if o.Discovered != "" {
		c.Output.Discovered = o.Discovered
	}
	if o.HAR != "" {
		c.Output.HAR = o.HAR
	}
	if o.Requests != "" {
		c.Output.Requests = o.Requests
	}
	if o.Report != "" {
		c.Output.Report = o.Report
	}
	if o.RequestsFiltered != "" {
		c.Output.RequestsFiltered = o.RequestsFiltered
	}
	if o.SaveJS {
		c.Output.SaveJSDir = "out/js"
	}
	if o.ScopeMode != "" {
		c.Crawl.ScopeMode = o.ScopeMode
	}
	if o.ScopeValue != "" {
		c.Crawl.ScopeValue = o.ScopeValue
	}
	if o.NoForms {
		c.Crawl.FormSubmit = false
	}
	if o.Resume {
		c.Crawl.Resume = true
	}
	if o.NavRetries >= 0 {
		c.Crawl.NavRetries = o.NavRetries
	}
	if o.MaxInteractions > 0 {
		c.Crawl.MaxInteractions = o.MaxInteractions
	}
	if o.NoRuntimeRoute {
		c.Crawl.RuntimeRoute = false
	}
	if o.NoSafety {
		c.Crawl.Safety = false
	}
	if o.NoBasecrawler {
		c.Crawl.BasecrawlerEnabled = false
	}
	if o.BasecrawlerMaxURLs > 0 {
		c.Crawl.BasecrawlerMaxURLs = o.BasecrawlerMaxURLs
	}
	if o.FeedPath != "" {
		c.Crawl.FeedPath = o.FeedPath
	}
	if o.NoWSCapture {
		c.Crawl.WSCapture = false
	}
	if o.DeliverOutScope && c.Output.Traffic != "" {
		c.Output.OutScope = strings.TrimSuffix(c.Output.Traffic, ".jsonl") + ".outscope.txt"
	}
	if o.HostRate > 0 {
		c.Crawl.HostRatePerSec = o.HostRate
	}
	if o.AuthFile != "" {
		c.Crawl.AuthFile = o.AuthFile
	}
	if o.Creds != "" {
		c.Crawl.Credentials = o.Creds
	}
	if o.Upstream != "" {
		if strings.EqualFold(o.Upstream, "none") {
			c.UpstreamProxy = ""
		} else {
			c.UpstreamProxy = o.Upstream
		}
	}
	if o.Backend != "" {
		c.BrowserBackend = strings.ToLower(o.Backend)
	}
	if o.MaxSession > 0 {
		c.MaxSession = o.MaxSession
	}
	if o.MaxPages > 0 {
		c.Crawl.MaxPages = o.MaxPages
	}
	if o.RatePerSec > 0 {
		c.Crawl.RatePerSec = o.RatePerSec
	}
	if o.SettleMS >= 0 {
		c.Crawl.SettleMS = o.SettleMS
	}
	if o.RootsFile != "" {
		c.Mitm.PublicRootsFile = o.RootsFile
		c.Mitm.MergePublicRoots = true
	}
	if o.Passthrough != "" {
		c.Mitm.TLSPassthrough = splitList(o.Passthrough)
	}
	if o.IPAllow != "" {
		c.Policy.IPAllow = splitList(o.IPAllow)
	}
	if o.IPDeny != "" {
		c.Policy.IPDeny = splitList(o.IPDeny)
	}
	if o.MetricsFile != "" {
		c.Metrics.Enabled = true
		c.Metrics.File = o.MetricsFile
	}
	if o.NoStealth {
		c.Stealth = false
	}
	if o.ObeyRobots {
		c.ObeyRobots = true
	}
	if o.MergeRoots {
		c.Mitm.MergePublicRoots = true
	}
	if o.PerSession {
		c.Mitm.PerSessionCA = true
	}
	if o.KeepDir {
		c.KeepSessionDir = true
	}
	if o.NoJSExtract {
		c.Crawl.JSExtract = false
	}
	if o.NoJSChunks {
		c.Crawl.JSChunks.Enabled = false
	}
	if o.JSChunkMaxDepth != nil {
		c.Crawl.JSChunks.MaxDepth = *o.JSChunkMaxDepth
	}
	if o.JSChunkMaxCount != nil {
		c.Crawl.JSChunks.MaxCount = *o.JSChunkMaxCount
	}
	if o.JSChunkMaxBytes != nil {
		c.Crawl.JSChunks.MaxChunkBytes = *o.JSChunkMaxBytes
	}
	if o.JSChunkMaxTotalBytes != nil {
		c.Crawl.JSChunks.MaxTotalBytes = *o.JSChunkMaxTotalBytes
	}
	if o.JSChunkFetchTimeoutMS != nil {
		c.Crawl.JSChunks.FetchTimeoutMS = *o.JSChunkFetchTimeoutMS
	}
	if o.RawDump {
		c.Output.RawDump = true
	}
}

func splitList(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
