package crawl

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/url"
	"testing"

	"github.com/winezer0/browcrawler/internal/config"
	"github.com/winezer0/browcrawler/internal/metrics"
	"github.com/winezer0/browcrawler/internal/storage"
)

type routeEvidenceDriver struct{ PageDriver }

func (routeEvidenceDriver) Eval(context.Context, string) (string, error) {
	return `["/lazy"]`, nil
}

func TestRuntimeEvidenceDoesNotMarkQueuedRoutesVisited(t *testing.T) {
	var buffer bytes.Buffer
	cfg := &config.Config{}
	cfg.Crawl.Depth = 2
	c := &Crawler{cfg: cfg, driver: routeEvidenceDriver{}, enc: json.NewEncoder(&buffer),
		scope: func(string) bool { return true }, metrics: metrics.Nop(), logger: log.New(io.Discard, "", 0)}
	seen := map[string]bool{}
	var queue []item
	c.collectRuntimeRoutes(context.Background(), item{URL: "http://t/page"}, seen, &queue)
	if len(queue) == 0 || seen["http://t/lazy"] {
		t.Fatalf("queued route would be skipped before navigation: queue=%+v seen=%v", queue, seen)
	}
	first := bytes.Count(buffer.Bytes(), []byte("\n"))
	seen["http://t/lazy"] = true
	queue = nil
	c.collectRuntimeRoutes(context.Background(), item{URL: "http://t/other"}, seen, &queue)
	if len(queue) != 0 || bytes.Count(buffer.Bytes(), []byte("\n")) != first*2 {
		t.Fatalf("visited route should retain its new parent without navigation: %s", buffer.String())
	}
	c.collectRuntimeRoutes(context.Background(), item{URL: "http://t/other"}, seen, &queue)
	if bytes.Count(buffer.Bytes(), []byte("\n")) != first*2 {
		t.Fatal("identical evidence was emitted twice")
	}
}

func TestDiscoveryPreservesDistinctSources(t *testing.T) {
	var buffer bytes.Buffer
	c := &Crawler{enc: json.NewEncoder(&buffer)}
	rows := []Discovery{
		{URL: "http://t/shared", From: "http://t/a", Source: "a@href"},
		{URL: "http://t/shared", From: "http://t/b", Source: "a@href"},
		{URL: "http://t/shared", From: "http://t/a", Source: "form@action", Method: "POST"},
	}
	for _, row := range append(rows, rows...) {
		if _, err := c.writeDiscovery(row); err != nil {
			t.Fatal(err)
		}
	}
	if bytes.Count(buffer.Bytes(), []byte("\n")) != 3 {
		t.Fatalf("lost or duplicated evidence: %s", buffer.String())
	}
}

func TestJSAndDOMKeepMethodsAndScriptParents(t *testing.T) {
	dom := ExtractLinks("http://t/page", `<a href="/same">a</a><form action="/same" method="post"></form>`)
	if len(dom) != 2 {
		t.Fatalf("DOM source collision: %+v", dom)
	}
	rows := ExtractJS([]storage.JSBody{
		{URL: "http://t/a.js", Body: []byte(`fetch("/same")`)},
		{URL: "http://t/b.js", Body: []byte(`fetch("/same")`)},
	}, "http://t/page")
	parents := map[string]bool{}
	for _, row := range rows {
		u, _ := url.Parse(row.URL)
		if u.Path == "/same" {
			parents[row.From] = true
		}
	}
	if len(parents) != 2 {
		t.Fatalf("JS parents lost: %+v", rows)
	}
}
