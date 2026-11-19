package crawl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/winezer0/basecrawler/safety"
	"github.com/winezer0/requestfilter"

	"github.com/winezer0/browcrawler/internal/config"
	"github.com/winezer0/browcrawler/internal/metrics"
	"github.com/winezer0/browcrawler/internal/policy"
	"github.com/winezer0/browcrawler/internal/storage"
)

type Stats struct {
	Completion             storage.ScanCompletion `json:"completion"`
	Pages                  int                    `json:"pages"`
	Failed                 int                    `json:"failed"`
	Discovered             int                    `json:"discovered"`
	BasecrawlerCandidates  int                    `json:"basecrawler_candidates"`
	BasecrawlerQueued      int                    `json:"basecrawler_queued"`
	FeedCandidates         int                    `json:"feed_candidates"`
	BrowserDiscovered      int                    `json:"browser_discovered"`
	BasecrawlerElapsedMS   int64                  `json:"basecrawler_elapsed_ms"`
	JSLinks                int                    `json:"js_links"`
	BundleChunksFetched    int64                  `json:"bundle_chunks_fetched"`
	BundleTruncated        int64                  `json:"bundle_truncated"`
	BundleChunkFetchErrors int64                  `json:"bundle_chunk_fetch_errors"`
	Records                int64                  `json:"traffic_records"`
	ElapsedMS              int64                  `json:"elapsed_ms"`
	Elapsed                time.Duration          `json:"-"`
}

type item struct {
	URL   string
	Depth int
}

type Crawler struct {
	cfg                *config.Config
	driver             PageDriver
	rec                *storage.Recorder
	logger             *log.Logger
	enc                *json.Encoder
	discoverySeen      map[string]bool
	completionReasons  map[string]int
	outFile            *os.File
	scope              func(string) bool
	safetyFilter       *safety.Safety
	wsMu               sync.Mutex
	wsURLs             map[string]string // CDP requestID -> ws(s) 建连 URL
	limiter            *policy.Limiter
	robots             *policy.Robots
	dedup              *DedupSession
	credentials        *Credentials
	checkpointPath     string
	resume             bool
	metrics            metrics.Recorder
	initialCandidates  []FeedSeed
	baseCandidateCount int
	feedCandidateCount int
	basecrawlerElapsed time.Duration
}

// Options 显式列出爬虫的全部依赖；策略与指标为可选（零值/Nop 即关闭）。
type Options struct {
	Cfg                   *config.Config
	Driver                PageDriver
	Rec                   *storage.Recorder
	DiscoveredPath        string
	Logger                *log.Logger
	Limiter               *policy.Limiter
	Robots                *policy.Robots
	Dedup                 *DedupSession
	Credentials           *Credentials // 真实凭据，优先于表单测试字典
	CheckpointPath        string       // 非空时每页持久化断点（seen+队列）
	Resume                bool         // true 且断点文件存在时从中断点恢复
	NavRetries            int          // 导航失败/429|503 的重试次数（含退避）
	MaxInteractions       int          // 每页交互点击上限（0 = 禁用交互探索）
	Metrics               metrics.Recorder
	InitialCandidates     []FeedSeed
	BasecrawlerCandidates int
	FeedCandidates        int
	BasecrawlerElapsed    time.Duration
}

func bundleChunkOptionsFromConfig(cfg *config.Config) bundleChunkOptions {
	chunks := cfg.Crawl.JSChunks
	return bundleChunkOptions{
		Enabled: chunks.Enabled, MaxDepth: chunks.MaxDepth, MaxCount: chunks.MaxCount,
		MaxChunkBytes: chunks.MaxChunkBytes, MaxTotalBytes: chunks.MaxTotalBytes,
		FetchTimeoutMS: chunks.FetchTimeoutMS,
	}
}

func New(o Options) (*Crawler, error) {
	if err := os.MkdirAll(filepath.Dir(o.DiscoveredPath), 0o755); err != nil {
		return nil, err
	}
	f, err := os.Create(o.DiscoveredPath)
	if err != nil {
		return nil, err
	}
	m := o.Metrics
	if m == nil {
		m = metrics.Nop()
	}
	scopeFn, err := ScopeFromCfg(o.Cfg.Crawl)
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	// 直接使用公共工具包 github.com/winezer0/basecrawler/safety，
	// 与 basecrawler 共用同一份词表与判定逻辑（本项目不再维护副本）。
	safetyFilter, err := safety.NewSafety(o.Cfg.Crawl.Safety,
		o.Cfg.Crawl.SafetyURLSkip, o.Cfg.Crawl.SafetyFormSkip)
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return &Crawler{
		cfg: o.Cfg, driver: o.Driver, rec: o.Rec, logger: o.Logger,
		outFile: f, enc: json.NewEncoder(f), scope: scopeFn, safetyFilter: safetyFilter,
		wsURLs:  map[string]string{},
		limiter: o.Limiter, robots: o.Robots, dedup: o.Dedup, credentials: o.Credentials,
		checkpointPath: o.CheckpointPath, resume: o.Resume, metrics: m,
		initialCandidates:  append([]FeedSeed(nil), o.InitialCandidates...),
		baseCandidateCount: o.BasecrawlerCandidates, feedCandidateCount: o.FeedCandidates,
		basecrawlerElapsed: o.BasecrawlerElapsed,
	}, nil
}

func (c *Crawler) Close() error { return c.outFile.Close() }

func (c *Crawler) prepareInitialCandidates(seed string, st *Stats) ([]item, error) {
	queue := []item{{URL: seed, Depth: 0}}
	queued := map[string]bool{normalize(seed): true}
	for _, candidate := range c.initialCandidates {
		u, err := url.Parse(candidate.URL)
		if err != nil || u.Hostname() == "" || !c.scope(u.Hostname()) {
			continue
		}
		source := candidate.Source
		if candidate.Origin == "basecrawler" {
			source = "basecrawler:" + source
		} else if source == "" {
			source = "feed"
		}
		d := Discovery{
			URL: candidate.URL, Source: source, From: candidate.From, Depth: candidate.Depth,
			Method: candidate.Method, Kind: candidate.Kind, StatusCode: candidate.StatusCode,
			ContentType: candidate.ContentType, Confidence: candidate.Confidence,
		}
		n, err := c.writeDiscovery(d)
		if err != nil {
			return nil, err
		}
		st.Discovered += n
		if candidate.Method != "" && !strings.EqualFold(candidate.Method, "GET") {
			continue
		}
		if strings.EqualFold(candidate.Confidence, "template") || strings.EqualFold(candidate.Confidence, "clue") || !isNavigable(candidate.URL) {
			continue
		}
		depth := candidate.Depth
		if depth <= 0 {
			depth = 1
		}
		if depth > c.cfg.Crawl.Depth || c.safetyFilter.SkipURL(candidate.URL) {
			continue
		}
		key := normalize(candidate.URL)
		if key == "" || queued[key] {
			continue
		}
		queued[key] = true
		queue = append(queue, item{URL: candidate.URL, Depth: depth})
		if candidate.Origin == "basecrawler" {
			st.BasecrawlerQueued++
		}
	}
	return queue, nil
}

// Run 以 seed 为根做 BFS，深度上限来自配置。
// 统计用命名返回值 + defer 收尾，保证 Ctrl+C / 超时中断时也能拿到真实的记录数与耗时。
func (c *Crawler) Run(ctx context.Context, seed string) (st Stats, runErr error) {
	t0 := time.Now()
	c.completionReasons = map[string]int{}
	defer func() {
		if st.Failed > 0 {
			c.noteCompletion("request_failure", st.Failed)
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(runErr, context.DeadlineExceeded) {
			c.noteCompletion("timeout", 1)
		} else if errors.Is(ctx.Err(), context.Canceled) || errors.Is(runErr, context.Canceled) {
			c.noteCompletion("canceled", 1)
		} else if runErr != nil {
			c.noteCompletion("fatal_error", 1)
		}
		st.Completion = storage.NewScanCompletion(c.completionReasons)
		c.rec.SetCompletion(st.Completion)
		st.Records, _ = c.rec.Stats()
		st.Elapsed = time.Since(t0)
		st.ElapsedMS = st.Elapsed.Milliseconds()
		st.BasecrawlerCandidates = c.baseCandidateCount
		st.FeedCandidates = c.feedCandidateCount
		st.BasecrawlerElapsedMS = c.basecrawlerElapsed.Milliseconds()
		c.metrics.Inc("crawl.pages_ok", int64(st.Pages))
		c.metrics.Inc("crawl.pages_failed", int64(st.Failed))
		c.metrics.Inc("crawl.discovered", int64(st.Discovered))
		c.metrics.Inc("crawl.traffic_records", st.Records)
		c.metrics.Inc("crawl.elapsed_ms", st.ElapsedMS)
		c.metrics.Inc("crawl.basecrawler_candidates", int64(st.BasecrawlerCandidates))
		c.metrics.Inc("crawl.basecrawler_queued", int64(st.BasecrawlerQueued))
		c.metrics.Inc("crawl.feed_candidates", int64(st.FeedCandidates))
		c.metrics.Inc("crawl.browser_discovered", int64(st.BrowserDiscovered))
		c.metrics.Inc("crawl.basecrawler_elapsed_ms", st.BasecrawlerElapsedMS)
	}()
	queue, err := c.prepareInitialCandidates(seed, &st)
	if err != nil {
		return st, err
	}
	seen := map[string]bool{}
	if c.cfg.Crawl.RuntimeRoute {
		InjectRuntimeRouteHook(ctx, c.driver)
	}
	if c.cfg.Crawl.WSCapture {
		if wc, ok := c.driver.(WSCapturer); ok {
			if err := wc.EnableWSCapture(ctx, c.onWSEvent); err != nil {
				c.logger.Printf("[crawl] ws capture disabled: %v", err)
			}
		}
	}
	if c.resume && c.checkpointPath != "" {
		if cp, err := LoadCheckpoint(c.checkpointPath, seed); err != nil {
			c.logger.Printf("[crawl] checkpoint load failed: %v", err)
		} else if cp != nil {
			restoreCheckpoint(cp, seen, &queue)
			c.logger.Printf("[crawl] resumed from checkpoint: seen=%d queue=%d", len(cp.Seen), len(cp.Rows))
			c.metrics.Inc("crawl.resumed", 1)
		}
	}
	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return st, err
		}
		it := queue[0]
		queue = queue[1:]
		key := normalize(it.URL)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		u, err := url.Parse(it.URL)
		if err != nil || !c.scope(u.Hostname()) {
			c.logger.Printf("[crawl] out of scope: %s", it.URL)
			continue
		}
		if c.robots != nil {
			allowed, err := c.robots.Allowed(ctx, it.URL)
			if err != nil {
				c.logger.Printf("[crawl] robots.txt 不可达，按放行处理: %s: %v", it.URL, err)
			}
			if !allowed {
				c.logger.Printf("[crawl] robots.txt disallow: %s", it.URL)
				c.metrics.Inc("crawl.robots_blocked", 1)
				continue
			}
		}
		if err := c.limiter.Acquire(ctx, u.Hostname()); err != nil {
			if errors.Is(err, policy.ErrPageLimit) {
				c.noteCompletion("page_limit", 1)
				c.logger.Printf("[crawl] max_pages=%d reached, stopping normally", c.cfg.Crawl.MaxPages)
				return st, nil
			}
			if ctx.Err() != nil {
				return st, ctx.Err()
			}
			c.logger.Printf("[crawl] policy blocked: %s: %v", it.URL, err)
			c.noteCompletion("policy_limit", 1)
			c.metrics.Inc("crawl.policy_blocked", 1)
			continue
		}
		c.rec.SetTaskID(it.URL)
		c.logger.Printf("[crawl] depth=%d %s", it.Depth, it.URL)

		// 导航 + 重试循环：网络错误与 429/503 按 NavRetries 重试（指数退避+jitter）；
		// 每次尝试独立 Reserve，失败 Release 以便重试，成功且非限流才 Commit。
		navigated := false
		var lastErr error
		for attempt := 0; attempt <= c.cfg.Crawl.NavRetries; attempt++ {
			if attempt > 0 {
				backoff := time.Duration(500*(1<<uint(attempt-1))) * time.Millisecond
				backoff += time.Duration(rand.Int63n(int64(backoff/4) + 1))
				c.logger.Printf("[crawl] retry %d/%d in %v: %s", attempt, c.cfg.Crawl.NavRetries, backoff, it.URL)
				c.metrics.Inc("crawl.retries", 1)
				select {
				case <-ctx.Done():
					return st, ctx.Err()
				case <-time.After(backoff):
				}
			}
			var dedupReservation *requestfilter.Reservation
			if c.dedup != nil {
				var allowed bool
				allowed, dedupReservation = c.dedup.Reserve(it.URL)
				if !allowed {
					c.logger.Printf("[crawl] dedup filtered: %s", it.URL)
					c.metrics.Inc("crawl.dedup_filtered", 1)
					break // 已被形状/泛化覆盖：静默跳过
				}
			}
			err := c.driver.Navigate(ctx, it.URL)
			if err == nil {
				if status := c.rec.LastStatusFor(it.URL); (status == 429 || status == 503) && attempt < c.cfg.Crawl.NavRetries {
					c.dedup.Release(dedupReservation)
					c.logger.Printf("[crawl] http %d, will retry: %s", status, it.URL)
					lastErr = fmt.Errorf("http %d", status)
					continue
				}
				c.dedup.Commit(dedupReservation)
				navigated = true
				break
			}
			c.dedup.Release(dedupReservation)
			lastErr = err
			if ctx.Err() != nil {
				return st, ctx.Err() // 中断不是页面失败，立即收尾
			}
		}
		if !navigated {
			if lastErr != nil {
				st.Failed++
				c.logger.Printf("[crawl] navigate failed: %s: %v", it.URL, lastErr)
			}
			continue
		}
		st.Pages++
		doc, err := c.driver.HTML(ctx)
		if err != nil {
			st.Failed++
			c.logger.Printf("[crawl] html failed: %s: %v", it.URL, err)
			continue
		}
		links := ExtractLinks(it.URL, doc)
		var jsLinks []Discovery
		if c.cfg.Crawl.RuntimeRoute {
			n := c.collectRuntimeRoutes(ctx, it, seen, &queue)
			st.Discovered += n
			st.BrowserDiscovered += n
		}
		if c.cfg.Crawl.JSExtract {
			analysis := extractJSWithChunks(ctx, c.driver, c.rec.TakeJS(), it.URL, bundleChunkOptionsFromConfig(c.cfg))
			jsLinks = analysis.Discoveries // preserve static evidence even for browser-known routes
			st.BundleChunksFetched += analysis.FetchedChunks
			st.BundleTruncated += analysis.TruncatedBundles + analysis.TruncatedChunkBodies
			st.BundleChunkFetchErrors += analysis.ChunkFetchErrors
			c.noteCompletion("bundle_scan_limit", int(analysis.TruncatedBundles))
			c.noteCompletion("chunk_body_limit", int(analysis.TruncatedChunkBodies))
			c.noteCompletion("chunk_fetch_failure", int(analysis.ChunkFetchErrors))
			c.noteCompletion("chunk_count_limit", int(analysis.ChunkCountLimited))
			c.noteCompletion("chunk_depth_limit", int(analysis.ChunkDepthLimited))
			c.noteCompletion("chunk_bytes_limit", int(analysis.ChunkBytesLimited))
			if analysis.FetchedChunks > 0 {
				c.metrics.Inc("crawl.bundle_chunks_fetched", analysis.FetchedChunks)
			}
			if analysis.TruncatedBundles > 0 {
				c.metrics.Inc("crawl.bundle_parse_truncated", analysis.TruncatedBundles)
			}
			if analysis.TruncatedChunkBodies > 0 {
				c.metrics.Inc("crawl.bundle_chunk_truncated", analysis.TruncatedChunkBodies)
			}
			if analysis.ChunkFetchErrors > 0 {
				c.metrics.Inc("crawl.bundle_chunk_fetch_errors", analysis.ChunkFetchErrors)
			}
			st.JSLinks += len(jsLinks)
		}
		for _, d := range append(links, jsLinks...) {
			// 双层生效·输出层：discovered 默认只显示域内（域外引用仍在
			// traffic.jsonl 全量录制并带 InScope 标记）。
			du, derr := url.Parse(d.URL)
			if derr != nil || !c.scope(du.Hostname()) {
				continue
			}
			d.Depth = it.Depth + 1
			if d.From == "" {
				d.From = it.URL
			}
			n, err := c.writeDiscovery(d)
			if err != nil {
				return st, err
			}
			st.Discovered += n
			st.BrowserDiscovered += n
		}
		var formRecords, postLinks []Discovery
		if c.cfg.Crawl.MaxInteractions > 0 {
			if trigger, ok := c.driver.(InteractionTrigger); ok {
				n, err := trigger.TriggerInteractions(ctx, c.cfg.Crawl.MaxInteractions)
				if err != nil {
					c.logger.Printf("[crawl] interactions failed: %s: %v", it.URL, err)
					c.noteCompletion("analysis_failure", 1)
				}
				c.metrics.Inc("crawl.interactions", int64(n))
				// 交互后的 DOM 变化（onclick 写入的链接）并入 postLinks 提取。
				if doc2, e2 := c.driver.HTML(ctx); e2 == nil {
					for _, d := range ExtractLinks(it.URL, doc2) {
						if normalize(d.URL) != normalize(it.URL) {
							postLinks = append(postLinks, d)
						}
					}
				}
				// 交互触发的 SPA 路由跳转（pushState hook）在此刻已入 hook 数组
				if c.cfg.Crawl.RuntimeRoute {
					n := c.collectRuntimeRoutes(ctx, it, seen, &queue)
					st.Discovered += n
					st.BrowserDiscovered += n
				}
			}
		}
		if c.cfg.Crawl.FormSubmit {
			if fs, ok := c.driver.(FormSubmitter); ok {
				var formLinks []Discovery
				formRecords, formLinks = SubmitPageForms(ctx, fs, it.URL,
					time.Duration(c.cfg.Crawl.SettleMS)*time.Millisecond, c.credentials, c.safetyFilter, c.logger)
				postLinks = append(postLinks, formLinks...)
				c.metrics.Inc("crawl.forms_submitted", int64(len(formRecords)))
			}
		}
		for _, d := range append(formRecords, postLinks...) {
			// 双层生效·输出层：表单记录同样只留域内（提交产生的导航本身
			// 受导航层 scope 约束，此处过滤的是跨域 action 端点的展示）。
			du, derr := url.Parse(d.URL)
			if derr != nil || !c.scope(du.Hostname()) {
				continue
			}
			d.Depth = it.Depth + 1
			n, err := c.writeDiscovery(d)
			if err != nil {
				return st, err
			}
			st.Discovered += n
			st.BrowserDiscovered += n
		}
		if it.Depth >= c.cfg.Crawl.Depth {
			for _, d := range append(links, postLinks...) {
				u, err := url.Parse(d.URL)
				if err == nil && c.scope(u.Hostname()) && isNavigable(d.URL) && !seen[normalize(d.URL)] {
					c.noteCompletion("depth_boundary_unvisited", 1)
				}
			}
			continue
		}
		for _, d := range append(links, postLinks...) {
			if !isNavigable(d.URL) {
				continue
			}
			du, err := url.Parse(d.URL)
			if err != nil || !c.scope(du.Hostname()) {
				continue
			}
			if seen[normalize(d.URL)] {
				continue
			}
			// safety：危险链接（登出类）记录为发现但不真实导航
			if c.safetyFilter.SkipURL(d.URL) {
				d.Depth = it.Depth + 1
				d.Source = "url-skipped"
				n, err := c.writeDiscovery(d)
				if err != nil {
					return st, err
				}
				st.Discovered += n
				st.BrowserDiscovered += n
				c.metrics.Inc("crawl.safety_url_skipped", 1)
				continue
			}
			queue = append(queue, item{URL: d.URL, Depth: it.Depth + 1})
		}
		if c.checkpointPath != "" {
			if err := SaveCheckpoint(c.checkpointPath, seed, seen, queue); err != nil {
				c.logger.Printf("[crawl] checkpoint save failed: %v", err)
			}
		}
	}
	return st, nil
}

// URLOnScope 把主机级 scope 判定包装为 URL 级（供交付层过滤复用）。
func URLOnScope(scope func(string) bool) func(string) bool {
	return func(raw string) bool {
		u, err := url.Parse(raw)
		if err != nil {
			return false
		}
		return scope(u.Hostname())
	}
}

func normalize(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	u.Fragment = ""
	u.Host = strings.ToLower(u.Host)
	if u.Scheme == "http" && strings.HasSuffix(u.Host, ":80") {
		u.Host = strings.TrimSuffix(u.Host, ":80")
	} else if u.Scheme == "https" && strings.HasSuffix(u.Host, ":443") {
		u.Host = strings.TrimSuffix(u.Host, ":443")
	}
	return u.String()
}

var staticExt = []string{
	".png", ".jpg", ".jpeg", ".gif", ".webp", ".svg", ".ico", ".bmp",
	".css", ".js", ".mjs", ".map", ".woff", ".woff2", ".ttf", ".otf", ".eot",
	".mp4", ".webm", ".mp3", ".wav", ".avi", ".pdf", ".zip", ".gz", ".exe",
}

func isNavigable(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	p := strings.ToLower(u.Path)
	for _, ext := range staticExt {
		if strings.HasSuffix(p, ext) {
			return false
		}
	}
	return true
}

func (s Stats) String() string {
	return fmt.Sprintf("pages=%d failed=%d discovered=%d basecrawler_candidates=%d basecrawler_queued=%d feed_candidates=%d browser_discovered=%d basecrawler_elapsed=%dms js_links=%d chunks=%d bundle_truncated=%d bundle_chunk_fetch_errors=%d traffic=%d elapsed=%dms",
		s.Pages, s.Failed, s.Discovered, s.BasecrawlerCandidates, s.BasecrawlerQueued,
		s.FeedCandidates, s.BrowserDiscovered, s.BasecrawlerElapsedMS, s.JSLinks,
		s.BundleChunksFetched, s.BundleTruncated, s.BundleChunkFetchErrors, s.Records, s.ElapsedMS)
}

func (c *Crawler) noteCompletion(code string, count int) {
	if count <= 0 {
		return
	}
	if c.completionReasons == nil {
		c.completionReasons = map[string]int{}
	}
	c.completionReasons[code] += count
}
