package crawl

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/winezer0/browcrawler/internal/config"
	"github.com/winezer0/browcrawler/internal/storage"
)

type httpBackedPageEvaluator struct {
	client   *http.Client
	urls     []string
	limits   []int64
	timeouts []int
}

func (e *httpBackedPageEvaluator) Eval(ctx context.Context, expression string) (string, error) {
	const targetPrefix = "const target = "
	start := strings.Index(expression, targetPrefix)
	if start < 0 {
		return "", fmt.Errorf("fetch expression is missing target")
	}
	start += len(targetPrefix)
	end := strings.IndexByte(expression[start:], ';')
	if end < 0 {
		return "", fmt.Errorf("fetch expression target is not terminated")
	}
	var target string
	if err := json.Unmarshal([]byte(expression[start:start+end]), &target); err != nil {
		return "", err
	}
	const limitPrefix = "const limit = "
	start = strings.Index(expression, limitPrefix)
	if start < 0 {
		return "", fmt.Errorf("fetch expression is missing byte limit")
	}
	start += len(limitPrefix)
	end = strings.IndexByte(expression[start:], ';')
	limit, err := strconv.ParseInt(expression[start:start+end], 10, 64)
	if err != nil {
		return "", err
	}
	const timeoutPrefix = "const timeoutMs = "
	start = strings.Index(expression, timeoutPrefix)
	if start < 0 {
		return "", fmt.Errorf("fetch expression is missing timeout")
	}
	start += len(timeoutPrefix)
	end = strings.IndexByte(expression[start:], ';')
	timeout, err := strconv.Atoi(expression[start : start+end])
	if err != nil {
		return "", err
	}
	e.urls = append(e.urls, target)
	e.limits = append(e.limits, limit)
	e.timeouts = append(e.timeouts, timeout)
	if !strings.Contains(expression, `credentials: "same-origin"`) ||
		!strings.Contains(expression, "reader.read()") ||
		!strings.Contains(expression, "signal: controller.signal") {
		return "", fmt.Errorf("fetch expression lost browser-session, streaming, or timeout limits")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return "", err
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return "", err
	}
	result := browserChunkFetchResult{OK: resp.StatusCode >= 200 && resp.StatusCode < 300, Status: resp.StatusCode}
	if int64(len(body)) > limit {
		result.Truncated = true
	} else {
		result.Body = string(body)
	}
	encoded, err := json.Marshal(result)
	return string(encoded), err
}

func testBundleChunkOptions() bundleChunkOptions {
	defaults := config.Default().Crawl.JSChunks
	return bundleChunkOptions{
		Enabled: defaults.Enabled, MaxDepth: defaults.MaxDepth, MaxCount: defaults.MaxCount,
		MaxChunkBytes: defaults.MaxChunkBytes, MaxTotalBytes: defaults.MaxTotalBytes,
		FetchTimeoutMS: defaults.FetchTimeoutMS,
	}
}

func TestExpandBundleChunksFindsAPIsInUnloadedNestedChunks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		switch r.URL.Path {
		case "/assets/chunk.js":
			_, _ = io.WriteString(w, `fetch("/api/chunk-secret"); import("./nested.js"); /* nested lazy module */`)
		case "/assets/nested.js":
			_, _ = io.WriteString(w, `fetch("/api/nested-secret");`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	evaluator := &httpBackedPageEvaluator{client: server.Client()}
	entry := `/* webpack app entry with lazy modules */ import("./chunk.js"); import("./chunk.js");`
	if len(entry) < 64 {
		entry += strings.Repeat(" ", 64-len(entry))
	}
	result := extractJSWithChunks(context.Background(), evaluator, []storage.JSBody{{
		URL:  server.URL + "/assets/app.js",
		Body: []byte(entry),
	}}, server.URL+"/", testBundleChunkOptions())

	if result.FetchedChunks != 2 {
		t.Fatalf("fetched %d chunks, want 2; fetches=%v", result.FetchedChunks, evaluator.urls)
	}
	if len(evaluator.urls) != 2 || evaluator.urls[0] != server.URL+"/assets/chunk.js" || evaluator.urls[1] != server.URL+"/assets/nested.js" {
		t.Fatalf("unexpected chunk requests: %v", evaluator.urls)
	}
	found := map[string]Discovery{}
	for _, discovery := range result.Discoveries {
		found[discovery.URL] = discovery
	}
	for path, from := range map[string]string{
		"/api/chunk-secret":  server.URL + "/assets/chunk.js",
		"/api/nested-secret": server.URL + "/assets/nested.js",
	} {
		discovery, ok := found[server.URL+path]
		if !ok {
			t.Errorf("missing API recovered from unloaded chunk %s: %+v", path, result.Discoveries)
			continue
		}
		if discovery.From != from {
			t.Errorf("API %s source URL = %q, want %q", path, discovery.From, from)
		}
		if !strings.HasPrefix(discovery.Source, "ast:") {
			t.Errorf("API %s parser source = %q, want the actual AST engine", path, discovery.Source)
		}
	}
}

func TestExpandBundleChunksSkipsCapturedAndCrossOriginChunks(t *testing.T) {
	const entry = `/* loaded and remote lazy modules */ import("./captured.js"); import("https://other.test/remote.js");`
	result := expandBundleChunks(context.Background(), []storage.JSBody{
		{URL: "https://site.test/assets/app.js", Body: []byte(entry)},
		{URL: "https://site.test/assets/captured.js", Body: []byte(`fetch("/api/captured");`)},
	}, "https://site.test/", func(context.Context, string, int64) ([]byte, bool, error) {
		t.Fatal("captured and cross-origin chunks must not be fetched")
		return nil, false, nil
	}, testBundleChunkOptions())
	if result.FetchedChunks != 0 {
		t.Fatalf("unexpected fetched chunk count: %d", result.FetchedChunks)
	}
	for _, discovery := range result.Discoveries {
		if discovery.URL == "https://site.test/api/captured" {
			return
		}
	}
	t.Fatalf("captured chunk API was not parsed: %+v", result.Discoveries)
}

func TestExpandBundleChunksEnforcesFetchCountLimit(t *testing.T) {
	options := testBundleChunkOptions()
	options.MaxCount = 3
	var source strings.Builder
	source.WriteString("/* lazy chunks */ ")
	for i := 0; i < options.MaxCount+5; i++ {
		fmt.Fprintf(&source, "import(\"/chunks/%d.js\");", i)
	}
	calls := 0
	result := expandBundleChunks(context.Background(), []storage.JSBody{{
		URL: "https://site.test/assets/app.js", Body: []byte(source.String()),
	}}, "https://site.test/", func(context.Context, string, int64) ([]byte, bool, error) {
		calls++
		return []byte(`fetch("/api/from-chunk");`), false, nil
	}, options)
	if calls != options.MaxCount || result.FetchedChunks != int64(options.MaxCount) {
		t.Fatalf("fetch budget not enforced: calls=%d fetched=%d", calls, result.FetchedChunks)
	}
}

func TestExpandBundleChunksUsesConfiguredDepthAndByteBudgets(t *testing.T) {
	entry := []byte(`/* configured depth fixture */ import("./chunk.js");`)
	options := testBundleChunkOptions()
	options.MaxDepth = 1
	options.MaxChunkBytes = 64
	options.MaxTotalBytes = 80
	var limits []int64
	result := expandBundleChunks(context.Background(), []storage.JSBody{{
		URL: "https://site.test/assets/app.js", Body: entry,
	}}, "https://site.test/", func(_ context.Context, target string, maxBytes int64) ([]byte, bool, error) {
		limits = append(limits, maxBytes)
		if strings.HasSuffix(target, "/chunk.js") {
			return []byte(`import("./nested.js"); fetch("/api/first");`), false, nil
		}
		return nil, true, nil
	}, options)
	if result.FetchedChunks != 1 || result.TruncatedChunkBodies != 0 {
		t.Fatalf("depth limit was not honored: %+v", result)
	}
	if len(limits) != 1 || limits[0] != 64 {
		t.Fatalf("fetch did not receive configured byte budget: %v", limits)
	}
}

func TestExpandBundleChunksReducesPerFetchLimitByRemainingPageBudget(t *testing.T) {
	entry := []byte(`/* byte budget fixture */ import("./first.js"); import("./second.js");`)
	options := testBundleChunkOptions()
	options.MaxChunkBytes = 64
	options.MaxTotalBytes = 40
	var limits []int64
	result := expandBundleChunks(context.Background(), []storage.JSBody{{
		URL: "https://site.test/assets/app.js", Body: entry,
	}}, "https://site.test/", func(_ context.Context, _ string, maxBytes int64) ([]byte, bool, error) {
		limits = append(limits, maxBytes)
		if len(limits) == 1 {
			return []byte(strings.Repeat("x", 25)), false, nil
		}
		return nil, true, nil
	}, options)
	if len(limits) != 2 || limits[0] != 40 || limits[1] != 15 {
		t.Fatalf("page byte budget was not applied to each fetch: %v", limits)
	}
	if result.FetchedChunks != 1 || result.TruncatedChunkBodies != 1 {
		t.Fatalf("unexpected byte-budget outcome: %+v", result)
	}
}

func TestExpandBundleChunksReportsOversizedBodies(t *testing.T) {
	entry := `/* oversized lazy module */ import("./large.js");`
	if len(entry) < 64 {
		entry += strings.Repeat(" ", 64-len(entry))
	}
	result := expandBundleChunks(context.Background(), []storage.JSBody{{
		URL: "https://site.test/assets/app.js", Body: []byte(entry),
	}}, "https://site.test/", func(context.Context, string, int64) ([]byte, bool, error) {
		return nil, true, nil
	}, testBundleChunkOptions())
	if result.TruncatedChunkBodies != 1 {
		t.Fatalf("oversized chunk count = %d, want 1", result.TruncatedChunkBodies)
	}
}

func TestDisabledBundleFetchingKeepsStaticExtraction(t *testing.T) {
	entry := []byte(`/* disabled chunk fetch */ import("./lazy.js");`)
	result := extractJSWithChunks(context.Background(), nil, []storage.JSBody{{
		URL: "https://site.test/assets/app.js", Body: entry,
	}}, "https://site.test/", bundleChunkOptions{})
	for _, discovery := range result.Discoveries {
		if discovery.URL == "https://site.test/assets/lazy.js" && strings.HasPrefix(discovery.Source, "bundle-chunk:") {
			return
		}
	}
	t.Fatalf("disabling fetch should keep static chunk discoveries: %+v", result.Discoveries)
}
