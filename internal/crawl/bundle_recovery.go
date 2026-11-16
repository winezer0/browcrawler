package crawl

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"strings"

	"github.com/winezer0/browcrawler/internal/storage"
)

const (
	maxBundleChunkDepth     = 2
	maxBundleChunkFetches   = 16
	maxBundleChunkBytes     = 2 << 20
	maxBundleChunkTotalSize = 8 << 20
)

type bundleChunkCandidate struct {
	URL   string
	Depth int
}

type jsExtractionResult struct {
	Discoveries          []Discovery
	Chunks               []bundleChunkCandidate
	TruncatedBundles     int64
	TruncatedChunkBodies int64
	FetchedChunks        int64
	ChunkFetchErrors     int64
}

type bundleChunkFetcher func(context.Context, string, int64) ([]byte, bool, error)

type pageEvaluator interface {
	Eval(context.Context, string) (string, error)
}

type browserChunkFetchResult struct {
	OK        bool   `json:"ok"`
	Body      string `json:"body"`
	Truncated bool   `json:"truncated"`
	Status    int    `json:"status"`
	Error     string `json:"error"`
}

// extractJSWithChunks parses captured JavaScript and fetches statically
// recovered, same-origin lazy chunks through the active browser page. It only
// reads response text; fetched code is never evaluated or imported.
func extractJSWithChunks(ctx context.Context, evaluator pageEvaluator, bodies []storage.JSBody, pageURL string) jsExtractionResult {
	return expandBundleChunks(ctx, bodies, pageURL, browserBundleChunkFetcher(evaluator))
}

func expandBundleChunks(ctx context.Context, initial []storage.JSBody, pageURL string, fetch bundleChunkFetcher) jsExtractionResult {
	allBodies := append([]storage.JSBody(nil), initial...)
	depths := make(map[string]int, len(initial))
	knownBodies := make(map[string]struct{}, len(initial)+maxBundleChunkFetches)
	for _, body := range initial {
		key := chunkURLKey(body.URL)
		if key != "" {
			knownBodies[key] = struct{}{}
			depths[key] = 0
		}
	}

	var attempted, fetched, fetchErrors, truncatedBodies int64
	var totalBytes int64
	for {
		current := extractJS(allBodies, pageURL, depths)
		if fetch == nil || ctx.Err() != nil || attempted >= maxBundleChunkFetches {
			current.FetchedChunks = fetched
			current.ChunkFetchErrors = fetchErrors
			current.TruncatedChunkBodies = truncatedBodies
			return current
		}

		added := false
		for _, candidate := range current.Chunks {
			if candidate.Depth > maxBundleChunkDepth || !sameOrigin(pageURL, candidate.URL) {
				continue
			}
			key := chunkURLKey(candidate.URL)
			if key == "" {
				continue
			}
			if _, exists := knownBodies[key]; exists {
				continue
			}
			if attempted >= maxBundleChunkFetches {
				break
			}
			remainingBytes := int64(maxBundleChunkTotalSize) - totalBytes
			if remainingBytes <= 0 {
				truncatedBodies++
				break
			}
			// Mark before fetching so failures and cycles cannot cause retries.
			knownBodies[key] = struct{}{}
			attempted++
			maxBytes := int64(maxBundleChunkBytes)
			if remainingBytes < maxBytes {
				maxBytes = remainingBytes
			}
			body, truncated, err := fetch(ctx, key, maxBytes)
			if err != nil {
				fetchErrors++
				continue
			}
			if truncated || int64(len(body)) > maxBytes {
				truncatedBodies++
				continue
			}
			if len(body) == 0 {
				continue
			}
			totalBytes += int64(len(body))
			depths[key] = candidate.Depth
			allBodies = append(allBodies, storage.JSBody{URL: key, Body: body})
			fetched++
			added = true
		}
		if !added {
			current.FetchedChunks = fetched
			current.ChunkFetchErrors = fetchErrors
			current.TruncatedChunkBodies = truncatedBodies
			return current
		}
	}
}

func browserBundleChunkFetcher(evaluator pageEvaluator) bundleChunkFetcher {
	if evaluator == nil {
		return nil
	}
	return func(ctx context.Context, target string, maxBytes int64) ([]byte, bool, error) {
		raw, err := evaluator.Eval(ctx, browserChunkFetchExpression(target, maxBytes))
		if err != nil {
			return nil, false, err
		}
		var result browserChunkFetchResult
		if err := json.Unmarshal([]byte(raw), &result); err != nil {
			return nil, false, fmt.Errorf("decode browser chunk response: %w", err)
		}
		if result.Truncated {
			return nil, true, nil
		}
		if !result.OK {
			return nil, false, fmt.Errorf("browser chunk fetch failed (status %d): %s", result.Status, result.Error)
		}
		body := []byte(result.Body)
		if int64(len(body)) > maxBytes {
			return nil, true, nil
		}
		return body, false, nil
	}
}

func browserChunkFetchExpression(target string, maxBytes int64) string {
	targetJSON, _ := json.Marshal(target)
	return fmt.Sprintf(`(async () => {
	const target = %s;
	const limit = %d;
	try {
		const response = await fetch(target, {credentials: "same-origin", redirect: "follow", cache: "no-store"});
		const finalURL = new URL(response.url || target, location.href);
		if (finalURL.origin !== location.origin) return JSON.stringify({ok: false, error: "cross-origin redirect"});
		if (!response.ok) return JSON.stringify({ok: false, status: response.status, error: "HTTP error"});
		if (!response.body) return JSON.stringify({ok: false, error: "response has no readable body"});
		const reader = response.body.getReader();
		const decoder = new TextDecoder();
		let body = "";
		let size = 0;
		while (true) {
			const item = await reader.read();
			if (item.done) break;
			size += item.value.byteLength;
			if (size > limit) {
				reader.cancel().catch(() => {});
				return JSON.stringify({ok: false, truncated: true, size});
			}
			body += decoder.decode(item.value, {stream: true});
		}
		body += decoder.decode();
		return JSON.stringify({ok: true, body, size});
	} catch (error) {
		return JSON.stringify({ok: false, error: String(error)});
	}
})()`, string(targetJSON), maxBytes)
}

func chunkURLKey(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || u.Host == "" {
		return ""
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.Fragment = ""
	u.RawFragment = ""
	if u.Scheme == "http" && strings.HasSuffix(u.Host, ":80") {
		u.Host = strings.TrimSuffix(u.Host, ":80")
	} else if u.Scheme == "https" && strings.HasSuffix(u.Host, ":443") {
		u.Host = strings.TrimSuffix(u.Host, ":443")
	}
	return u.String()
}

func isJavaScriptBundleChunk(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	switch strings.ToLower(path.Ext(u.Path)) {
	case "", ".js", ".mjs":
		return true
	default:
		return false
	}
}

func sameOrigin(pageURL, targetURL string) bool {
	page, err := url.Parse(pageURL)
	if err != nil || page.User != nil {
		return false
	}
	target, err := url.Parse(targetURL)
	if err != nil || target.User != nil {
		return false
	}
	page.Scheme = strings.ToLower(page.Scheme)
	target.Scheme = strings.ToLower(target.Scheme)
	if (page.Scheme != "http" && page.Scheme != "https") || target.Scheme != page.Scheme {
		return false
	}
	return strings.EqualFold(page.Hostname(), target.Hostname()) && effectivePort(page) == effectivePort(target)
}

func effectivePort(u *url.URL) string {
	if port := u.Port(); port != "" {
		return port
	}
	if strings.EqualFold(u.Scheme, "https") {
		return "443"
	}
	if strings.EqualFold(u.Scheme, "http") {
		return "80"
	}
	return ""
}
