package policy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseRobotsRules(t *testing.T) {
	body := `User-agent: *
Disallow: /admin
Allow: /admin/public
Disallow: /private/*  # comment
Disallow: /exact$
User-agent: BadBot
Disallow: /

User-agent: *
Allow: /
`
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(body))}
	rules, err := parseRobots(resp)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 5 {
		t.Fatalf("rules=%d want 5: %+v", len(rules), rules)
	}
}

func TestRobotsAllowedThroughHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			_, _ = w.Write([]byte("User-agent: *\nDisallow: /blocked\nAllow: /blocked/public\n"))
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()
	rb := NewRobots("")
	ctx := context.Background()
	cases := []struct {
		path string
		want bool
	}{
		{"/allowed", true},
		{"/blocked", false},
		{"/blocked/secret", false},
		{"/blocked/public", true},
	}
	for _, tc := range cases {
		got, err := rb.Allowed(ctx, srv.URL+tc.path)
		if err != nil {
			t.Fatalf("%s: %v", tc.path, err)
		}
		if got != tc.want {
			t.Errorf("%s: got %v want %v", tc.path, got, tc.want)
		}
	}
}

func TestRobotsFailOpen(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	rb := NewRobots("")
	got, err := rb.Allowed(context.Background(), srv.URL+"/anything")
	if err != nil {
		t.Fatalf("404 应视为无限制且无错误: %v", err)
	}
	if !got {
		t.Fatal("404 时应放行")
	}
}

func TestMatchPath(t *testing.T) {
	for _, tc := range []struct {
		path, rule string
		want       bool
	}{
		{"/admin/x", "/admin", true},
		{"/adminish", "/admin", true},
		{"/exact", "/exact$", true},
		{"/exact/more", "/exact$", false},
		{"/a/b/c", "/a/*/c", true},
		{"/", "/", true},
	} {
		if got := matchPath(tc.path, tc.rule); got != tc.want {
			t.Errorf("matchPath(%q,%q)=%v want %v", tc.path, tc.rule, got, tc.want)
		}
	}
}
