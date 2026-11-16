package crawl

import (
	"net/url"
	"strings"

	"github.com/winezer0/basecrawler/parse"
	parseast "github.com/winezer0/basecrawler/parse/ast"
	"golang.org/x/net/html"

	"github.com/winezer0/browcrawler/internal/storage"
)

// Discovery 是从页面/JS 中挖掘出的 URL 或接口。
type Discovery struct {
	URL    string `json:"url"`
	Source string `json:"source"` // ast:... / regex:... / bundle-chunk:... / bundle-route:... / browser DOM sources
	From   string `json:"from"`
	Depth  int    `json:"depth"`
	Method string `json:"method,omitempty"`
}

// ExtractLinks 从渲染后的 DOM 提取链接（含 iframe/form/script）。
func ExtractLinks(base, doc string) []Discovery {
	baseURL, err := url.Parse(base)
	if err != nil {
		return nil
	}
	root, err := html.Parse(strings.NewReader(doc))
	if err != nil {
		return nil
	}
	var out []Discovery
	seen := map[string]bool{}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			attrs := attrMap(n)
			type rule struct{ tag, attr, src string }
			rules := []rule{
				{"a", "href", "a@href"}, {"area", "href", "area@href"},
				{"iframe", "src", "iframe@src"}, {"frame", "src", "frame@src"},
				{"form", "action", "form@action"}, {"script", "src", "script@src"},
				{"link", "href", "link@href"}, {"img", "src", "img@src"},
			}
			for _, r := range rules {
				if strings.EqualFold(n.Data, r.tag) {
					raw := attrs[r.attr]
					if raw == "" {
						continue
					}
					abs := resolve(baseURL, raw)
					key := strings.Join([]string{abs, r.src, strings.ToUpper(attrs["method"])}, "\x00")
					if abs == "" || seen[key] {
						continue
					}
					seen[key] = true
					d := Discovery{URL: abs, Source: r.src, From: base}
					if r.tag == "form" {
						d.Method = strings.ToUpper(defaultStr(attrs["method"], "GET"))
					}
					out = append(out, d)
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(root)
	return out
}

// ExtractJS uses AST-first parsing, light static deobfuscation, and bundle
// recovery to extract candidates from recorded JavaScript response bodies.
func ExtractJS(jsBodies []storage.JSBody, base string) []Discovery {
	return extractJS(jsBodies, base, nil).Discoveries
}

func extractJS(jsBodies []storage.JSBody, base string, depths map[string]int) jsExtractionResult {
	baseURL, err := url.Parse(base)
	if err != nil {
		return jsExtractionResult{}
	}
	result := jsExtractionResult{}
	seen := map[string]bool{}
	chunkSeen := map[string]struct{}{}
	parseOptions := parse.DefaultOptions()
	parseOptions.JSMode = parse.JSModeAST
	parseOptions.Deobf = parse.DeobfOff // each body is recovered once before both parse paths
	for _, jb := range jsBodies {
		depth := depths[chunkURLKey(jb.URL)]
		bundleSource := jb.Body
		if recovered, changed := parse.Recover(string(jb.Body)); changed {
			bundleSource = []byte(recovered)
		}
		var bundle parse.BundleResult
		if parse.LooksLikeBundle(bundleSource) {
			bundle = parse.ParseBundle(bundleSource, parse.Options{ChunkRecover: true})
		}
		if bundle.Truncated {
			result.TruncatedBundles++
		}
		bundleRouteURLs := make(map[string]struct{}, len(bundle.Routes))
		for _, route := range bundle.Routes {
			if abs := resolve(baseURL, route.Path); abs != "" {
				bundleRouteURLs[abs] = struct{}{}
			}
		}

		astUsed := false
		parseOptions.JSExtract = func(src string) ([]parse.Request, bool) {
			reqs, ok := parseast.Auto(src)
			astUsed = ok
			return reqs, ok
		}
		reqs := parse.ParseJS(bundleSource, parseOptions)
		mode := "regex"
		if astUsed {
			mode = "ast"
		}
		for _, req := range reqs {
			raw := strings.TrimSpace(req.Raw)
			if raw == "" || strings.ContainsAny(raw, "${}\n") {
				continue
			}
			if req.Confidence == "template" {
				continue // 占位符路径不可直接访问
			}
			abs := resolve(baseURL, raw)
			if abs == "" || !isNavigable(abs) {
				continue
			}
			if _, isBundleRoute := bundleRouteURLs[abs]; isBundleRoute {
				continue
			}
			addJSDiscovery(&result.Discoveries, seen, baseURL, raw, jsSource(mode, req.Source), jb.URL, req.Method)
		}

		scriptBase := scriptBaseURL(baseURL, jb.URL)
		for _, chunk := range bundle.Chunks {
			chunkURL := resolve(scriptBase, chunk.Path)
			if chunkURL != "" && isJavaScriptBundleChunk(chunkURL) {
				key := chunkURLKey(chunkURL)
				if _, duplicate := chunkSeen[key]; !duplicate {
					chunkSeen[key] = struct{}{}
					result.Chunks = append(result.Chunks, bundleChunkCandidate{
						URL: chunkURL, Depth: depth + 1,
					})
				}
			}
			addJSDiscovery(&result.Discoveries, seen, scriptBase, chunk.Path,
				jsSource("bundle-chunk", chunk.Source), jb.URL, "")
		}
		for _, route := range bundle.Routes {
			addJSDiscovery(&result.Discoveries, seen, baseURL, route.Path,
				jsSource("bundle-route", route.Source), jb.URL, route.Method)
		}
	}
	return result
}

func addJSDiscovery(out *[]Discovery, seen map[string]bool, baseURL *url.URL, raw, source, from, method string) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.ContainsAny(raw, "${}\n") {
		return
	}
	abs := resolve(baseURL, raw)
	key := strings.Join([]string{abs, source, from, method}, "\x00")
	if abs == "" || seen[key] {
		return
	}
	seen[key] = true
	*out = append(*out, Discovery{URL: abs, Source: source, From: from, Method: method})
}

func jsSource(mode, detail string) string {
	if detail == "" {
		return mode + ":unknown"
	}
	return mode + ":" + detail
}

func scriptBaseURL(pageURL *url.URL, script string) *url.URL {
	if script == "" {
		return pageURL
	}
	u, err := url.Parse(script)
	if err != nil {
		return pageURL
	}
	return pageURL.ResolveReference(u)
}

func attrMap(n *html.Node) map[string]string {
	m := make(map[string]string, len(n.Attr))
	for _, a := range n.Attr {
		m[strings.ToLower(a.Key)] = a.Val
	}
	return m
}

func resolve(base *url.URL, raw string) string {
	raw = strings.TrimSpace(raw)
	low := strings.ToLower(raw)
	for _, p := range []string{"javascript:", "mailto:", "data:", "tel:", "blob:", "about:"} {
		if strings.HasPrefix(low, p) {
			return ""
		}
	}
	if strings.HasPrefix(raw, "#") {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	abs := base.ResolveReference(u)
	if abs.Scheme != "http" && abs.Scheme != "https" {
		return ""
	}
	abs.Fragment = ""
	return abs.String()
}

func defaultStr(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
