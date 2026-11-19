package crawl

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/winezer0/basecrawler"

	"github.com/winezer0/browcrawler/internal/config"
)

func TestBasecrawlerScopeMatchesBrowserScope(t *testing.T) {
	cases := []struct {
		name string
		cfg  config.CrawlConfig
		in   []string
		out  []string
	}{
		{
			name: "fqdn",
			cfg:  config.CrawlConfig{ScopeMode: ScopeFQDN, ScopeValue: "www.example.test"},
			in:   []string{"https://www.example.test/a", "http://www.example.test:8080/a"},
			out:  []string{"https://api.example.test/a", "https://notwww.example.test/a"},
		},
		{
			name: "rdn",
			cfg:  config.CrawlConfig{ScopeMode: ScopeRDN, ScopeValue: "docs.example.test"},
			in:   []string{"https://example.test/", "https://api.example.test/a"},
			out:  []string{"https://notexample.test/a"},
		},
		{
			name: "dnk",
			cfg:  config.CrawlConfig{ScopeMode: ScopeDNK, ScopeValue: "example"},
			in:   []string{"https://example.test/", "https://notexample.invalid/a"},
			out:  []string{"https://other.invalid/a"},
		},
		{
			name: "regex",
			cfg:  config.CrawlConfig{ScopeMode: ScopeRegex, ScopeValue: `^api[0-9]+\.example\.test$`},
			in:   []string{"https://api2.example.test/a"},
			out:  []string{"https://api.example.test/a", "https://api2.example.test.evil/a"},
		},
		{
			name: "all",
			cfg:  config.CrawlConfig{ScopeMode: ScopeAll},
			in:   []string{"https://other.invalid/a"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scope, rx, err := BasecrawlerScope(tc.cfg)
			if err != nil {
				t.Fatal(err)
			}
			for _, raw := range tc.in {
				if !inBaseScope(scope, rx, raw) {
					t.Errorf("expected in-scope: %s", raw)
				}
			}
			for _, raw := range tc.out {
				if inBaseScope(scope, rx, raw) {
					t.Errorf("unexpected in-scope URL: %s", raw)
				}
			}
		})
	}
}

func inBaseScope(scope basecrawler.Scope, rx interface{ MatchString(string) bool }, raw string) bool {
	if scope == basecrawler.ScopeAny {
		return true
	}
	return rx != nil && rx.MatchString(raw)
}

func TestLoadFeedPreservesBasecrawlerResultFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "feed.jsonl")
	line := `{"url":"https://example.test/api/items","method":"post","kind":"route","source":"js-fetch","referer":"https://example.test/","depth":2,"status":201,"content_type":"application/json","confidence":"direct"}`
	if err := os.WriteFile(path, []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	seeds, err := LoadFeed(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(seeds) != 1 {
		t.Fatalf("got %d feed seeds, want 1", len(seeds))
	}
	got := seeds[0]
	if got.URL != "https://example.test/api/items" || got.Method != "POST" || got.Kind != "route" ||
		got.Source != "js-fetch" || got.From != "https://example.test/" || got.Depth != 2 ||
		got.StatusCode != 201 || got.ContentType != "application/json" || got.Confidence != "direct" || got.Origin != "feed" {
		t.Fatalf("feed fields were not preserved: %+v", got)
	}
}

func TestPrepareInitialCandidatesDeduplicatesNavigationAndKeepsEvidence(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Crawl.ScopeMode = ScopeFQDN
	cfg.Crawl.ScopeValue = "example.test"
	crawler, err := New(Options{
		Cfg: cfg, DiscoveredPath: filepath.Join(dir, "discovered.jsonl"),
		InitialCandidates: []FeedSeed{
			{URL: "https://example.test/docs", Source: "sitemap", From: "https://example.test/sitemap.xml", Depth: 1, StatusCode: 200, ContentType: "text/html", Origin: "basecrawler"},
			{URL: "https://example.test/docs#section", Source: "a@href", From: "https://example.test/", Depth: 1, Origin: "feed"},
			{URL: "https://example.test/api/items", Source: "form", Method: "POST", Depth: 1, Origin: "basecrawler"},
			{URL: "https://example.test/users/{id}", Source: "route", Confidence: "template", Depth: 1, Origin: "basecrawler"},
			{URL: "https://outside.invalid/", Source: "sitemap", Depth: 1, Origin: "basecrawler"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer crawler.Close()
	var stats Stats
	queue, err := crawler.prepareInitialCandidates("https://example.test/", &stats)
	if err != nil {
		t.Fatal(err)
	}
	if len(queue) != 2 || queue[1].URL != "https://example.test/docs" {
		t.Fatalf("queue = %+v, want seed plus one unique URL", queue)
	}
	if stats.BasecrawlerQueued != 1 || stats.Discovered != 4 {
		t.Fatalf("stats = %+v, want one basecrawler navigation and four in-scope evidence rows", stats)
	}

	data, err := os.ReadFile(filepath.Join(dir, "discovered.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var records []Discovery
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var record Discovery
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		records = append(records, record)
	}
	if len(records) != 4 {
		t.Fatalf("got %d evidence rows, want 4", len(records))
	}
	if records[0].Source != "basecrawler:sitemap" || records[0].From != "https://example.test/sitemap.xml" ||
		records[0].StatusCode != 200 || records[0].ContentType != "text/html" {
		t.Fatalf("basecrawler provenance/status missing: %+v", records[0])
	}
}

func TestDiscoverBasecrawlerUsesStaticLinksAndReportsHTTPStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch r.URL.Path {
		case "/":
			_, _ = w.Write([]byte(`<a href="/docs">docs</a>`))
		case "/docs":
			_, _ = w.Write([]byte(`<html><title>docs</title></html>`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cfg := config.Default().Crawl
	cfg.ScopeMode = ScopeFQDN
	cfg.ScopeValue = "127.0.0.1"
	cfg.Depth = 1
	cfg.BasecrawlerMaxURLs = 10
	seeds, err := DiscoverBasecrawler(context.Background(), server.URL+"/", cfg, false, 1<<20, 0, "", nil)
	if err != nil {
		t.Fatalf("static crawl: %v", err)
	}
	for _, seed := range seeds {
		if seed.URL == server.URL+"/docs" {
			if seed.Source == "" || seed.From != server.URL+"/" || seed.StatusCode != http.StatusOK || !strings.HasPrefix(seed.ContentType, "text/html") {
				t.Fatalf("incomplete static result: %+v", seed)
			}
			return
		}
	}
	t.Fatalf("basecrawler did not return /docs; candidates=%+v", seeds)
}
