package crawl

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/winezer0/browcrawler/internal/config"
	"github.com/winezer0/browcrawler/internal/policy"
	"github.com/winezer0/browcrawler/internal/storage"
)

type configuredChunkDriver struct {
	*httpBackedPageEvaluator
}

func (*configuredChunkDriver) Connect(context.Context, string) error  { return nil }
func (*configuredChunkDriver) Navigate(context.Context, string) error { return nil }
func (*configuredChunkDriver) HTML(context.Context) (string, error)   { return "<html></html>", nil }
func (*configuredChunkDriver) Close() error                           { return nil }

func TestCrawlerRunUsesLoadedJSChunkConfiguration(t *testing.T) {
	var firstFetches, secondFetches, thirdFetches atomic.Int32
	chunkServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		switch r.URL.Path {
		case "/assets/first.js":
			firstFetches.Add(1)
			_, _ = io.WriteString(w, `fetch("/api/from-first");`)
		case "/assets/second.js":
			secondFetches.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
		case "/assets/third.js":
			thirdFetches.Add(1)
			_, _ = io.WriteString(w, `fetch("/api/from-third");`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer chunkServer.Close()

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	configYAML := `crawl:
  depth: 0
  js_extract: true
  skip_static_body: false
  form_submit: false
  runtime_route: false
  ws_capture: false
  js_chunks:
    enabled: true
    max_depth: 1
    max_count: 2
    max_chunk_bytes: 96
    max_total_bytes: 120
    fetch_timeout_ms: 7000
`
	if err := os.WriteFile(configPath, []byte(configYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}

	recorder, err := storage.New(filepath.Join(t.TempDir(), "traffic.jsonl"), "test", cfg.MaxBodySize, cfg.Crawl.SkipStaticBody)
	if err != nil {
		t.Fatal(err)
	}
	defer recorder.Close()
	seed := chunkServer.URL + "/page"
	recordJavaScriptResponse(t, recorder, chunkServer.URL+"/assets/app.js", `import("./first.js"); import("./second.js"); import("./third.js");`)

	evaluator := &httpBackedPageEvaluator{client: chunkServer.Client()}
	driver := &configuredChunkDriver{httpBackedPageEvaluator: evaluator}
	crawler, err := New(Options{
		Cfg: cfg, Driver: driver, Rec: recorder,
		DiscoveredPath: filepath.Join(t.TempDir(), "discovered.jsonl"),
		Logger:         log.New(io.Discard, "", 0), Limiter: policy.New(cfg),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer crawler.Close()

	stats, err := crawler.Run(context.Background(), seed)
	if err != nil {
		t.Fatal(err)
	}
	if stats.BundleChunksFetched != 1 || stats.BundleChunkFetchErrors != 1 {
		t.Fatalf("unexpected crawler chunk stats: %+v", stats)
	}
	if stats.Completion.Complete || stats.Completion.Status != "partial_failure" {
		t.Fatalf("chunk failure marked complete: %+v", stats.Completion)
	}
	reasons := map[string]int{}
	for _, reason := range stats.Completion.Reasons {
		reasons[reason.Code] = reason.Count
	}
	if reasons["chunk_fetch_failure"] != 1 || reasons["chunk_count_limit"] != 1 {
		t.Fatalf("missing failure/quota evidence: %+v", reasons)
	}
	if firstFetches.Load() != 1 || secondFetches.Load() != 1 || thirdFetches.Load() != 0 {
		t.Fatalf("configured max_count not honored: first=%d second=%d third=%d", firstFetches.Load(), secondFetches.Load(), thirdFetches.Load())
	}
	if len(evaluator.limits) != 2 || evaluator.limits[0] != 96 || evaluator.limits[1] != 95 ||
		len(evaluator.timeouts) != 2 || evaluator.timeouts[0] != 7000 || evaluator.timeouts[1] != 7000 {
		t.Fatalf("loaded byte/timeout budgets not passed to browser fetch: limits=%v timeouts=%v", evaluator.limits, evaluator.timeouts)
	}
}

func recordJavaScriptResponse(t *testing.T, recorder *storage.Recorder, target, source string) {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	record := recorder.Begin(request)
	response := &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     http.Header{"Content-Type": []string{"application/javascript"}},
		Body:       io.NopCloser(strings.NewReader(source)),
		Request:    request,
	}
	recorder.Attach(record, response)
	if _, err := io.Copy(io.Discard, response.Body); err != nil {
		t.Fatal(err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
}
