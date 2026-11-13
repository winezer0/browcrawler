package crawl

import (
	"testing"

	"github.com/winezer0/browcrawler/internal/config"
)

func TestLegacyScope(t *testing.T) {
	cases := []struct {
		scope string
		host  string
		want  bool
	}{
		{"example.com", "example.com", true},
		{"example.com", "api.example.com", true},
		{"example.com", "notexample.com", false},
		{"example.com,other.org", "other.org", true},
		{"*", "anything.test", true},
		{"re:^dev-\\w+\\.example\\.com$", "dev-1.example.com", true},
		{"re:^dev-\\w+\\.example\\.com$", "prod.example.com", false},
	}
	for _, tc := range cases {
		fn, err := ScopeFromCfg(config.CrawlConfig{Scope: tc.scope})
		if err != nil {
			t.Fatalf("scope=%q: %v", tc.scope, err)
		}
		if got := fn(tc.host); got != tc.want {
			t.Errorf("scope=%q host=%q: got %v want %v", tc.scope, tc.host, got, tc.want)
		}
	}
}

func TestNormalize(t *testing.T) {
	cases := map[string]string{
		"http://Example.com:80/a#frag": "http://example.com/a",
		"https://Example.com:443/x":    "https://example.com/x",
		"http://a.com/x#one":           "http://a.com/x",
		"http://a.com/x#two":           "http://a.com/x",
	}
	for in, want := range cases {
		if got := normalize(in); got != want {
			t.Errorf("normalize(%q)=%q want %q", in, got, want)
		}
	}
}

func TestIsNavigable(t *testing.T) {
	for _, u := range []string{"http://a.com/page", "https://a.com/api?q=1"} {
		if !isNavigable(u) {
			t.Errorf("%q 应可导航", u)
		}
	}
	for _, u := range []string{"http://a.com/x.png", "http://a.com/app.js", "mailto:a@b.com", "javascript:void(0)"} {
		if isNavigable(u) {
			t.Errorf("%q 不应导航", u)
		}
	}
}
