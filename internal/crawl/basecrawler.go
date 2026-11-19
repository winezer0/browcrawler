package crawl

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/winezer0/basecrawler"

	"github.com/winezer0/browcrawler/internal/config"
)

// DiscoverBasecrawler runs the bounded, read-only static pass and returns its
// source edges for the browser crawler to record and visit. Both passes share
// the same proxy, scope, auth headers, depth, and per-task deadline.
func DiscoverBasecrawler(
	ctx context.Context,
	seed string,
	cfg config.CrawlConfig,
	obeyRobots bool,
	maxBodySize int64,
	requestTimeout time.Duration,
	proxy string,
	headers http.Header,
) ([]FeedSeed, error) {
	if cfg.BasecrawlerMaxURLs < 1 {
		return nil, fmt.Errorf("basecrawler_max_urls must be positive")
	}
	staticRate, err := basecrawlerRateLimit(cfg)
	if err != nil {
		return nil, err
	}
	scope, scopeRegex, err := BasecrawlerScope(cfg)
	if err != nil {
		return nil, err
	}

	options := basecrawler.DefaultOptions()
	options.MaxDepth = cfg.Depth
	options.MaxURLs = cfg.BasecrawlerMaxURLs
	options.Concurrency = 2
	options.PerHostConcurrency = 2
	options.StableHostQuota = true
	options.RateLimit = staticRate
	options.Scope = scope
	options.ScopeRegex = scopeRegex
	options.FollowRobots = obeyRobots
	options.FollowRobotsDisallow = false
	options.FollowSitemap = true
	options.FollowLLMS = true
	options.FollowFormActions = false
	options.AllowUnsafeSend = false
	options.ExtractJS = true
	options.ExtractFromStyle = true
	options.ExtractJSON = true
	options.ExtractChunkGraph = true
	options.RouteExec = false
	options.RouteExpand = false
	options.FuzzPaths = false
	options.ProbeOpenAPI = false
	options.ExtractCurlBlocks = false
	options.Deobfuscate = true
	options.DeobfForce = false
	options.DeobfExec = false
	options.NoiseFilter = true
	options.Retries = 0
	options.Proxy = proxy
	options.Insecure = proxy != "" // local MITM proxy presents this session's generated CA
	if maxBodySize > 0 {
		options.MaxBodySize = maxBodySize
	}
	if requestTimeout > 0 {
		options.Timeout = requestTimeout
	}
	if headers != nil {
		options.Headers = headers.Clone()
	}

	var mu sync.Mutex
	var ordered []string
	byKey := make(map[string]*FeedSeed)
	add := func(result basecrawler.Result) {
		key := feedKey(result.URL, result.Method, result.Source, result.Referer, result.Kind)
		if key == "" {
			return
		}
		if existing := byKey[key]; existing != nil {
			if result.StatusCode > 0 {
				existing.StatusCode = result.StatusCode
				existing.ContentType = result.ContentType
			}
			return
		}
		ordered = append(ordered, key)
		byKey[key] = &FeedSeed{
			URL: result.URL, Method: strings.ToUpper(result.Method), Kind: result.Kind,
			Source: result.Source, From: result.Referer, Depth: result.Depth,
			StatusCode: result.StatusCode, ContentType: result.ContentType,
			Confidence: result.Confidence, Origin: "basecrawler",
		}
	}
	updateStatus := func(result basecrawler.Result) {
		target := normalize(result.URL)
		if target == "" || result.StatusCode == 0 {
			return
		}
		mu.Lock()
		for _, item := range byKey {
			if normalize(item.URL) == target {
				item.StatusCode = result.StatusCode
				item.ContentType = result.ContentType
			}
		}
		mu.Unlock()
	}
	options.OnDiscovery = func(result basecrawler.Result) {
		mu.Lock()
		add(result)
		mu.Unlock()
	}
	options.OnResult = updateStatus

	staticCrawler, err := basecrawler.New(options)
	if err != nil {
		return nil, fmt.Errorf("create basecrawler: %w", err)
	}
	_, crawlErr := staticCrawler.Crawl(ctx, seed)
	closeErr := staticCrawler.Close()
	if crawlErr == nil {
		crawlErr = closeErr
	}

	mu.Lock()
	defer mu.Unlock()
	seeds := make([]FeedSeed, 0, len(ordered))
	seedKey := normalize(seed)
	for _, key := range ordered {
		candidate := byKey[key]
		if normalize(candidate.URL) == seedKey {
			continue
		}
		seeds = append(seeds, *candidate)
	}
	return seeds, crawlErr
}

func feedKey(rawURL, method, source, referer, kind string) string {
	if normalize(rawURL) == "" {
		return ""
	}
	return strings.Join([]string{normalize(rawURL), strings.ToUpper(method), source, normalize(referer), kind}, "\x00")
}

func basecrawlerRateLimit(cfg config.CrawlConfig) (int, error) {
	limit := 0.0
	for _, rate := range []float64{cfg.RatePerSec, cfg.HostRatePerSec} {
		if rate <= 0 {
			continue
		}
		if limit == 0 || rate < limit {
			limit = rate
		}
	}
	if limit == 0 {
		return 0, nil
	}
	if limit < 1 {
		return 0, fmt.Errorf("basecrawler currently supports integer requests/second; configured rate %.3g is below 1", limit)
	}
	return int(math.Floor(limit)), nil
}
