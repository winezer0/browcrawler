package crawl

import (
	"net/url"
	"strings"

	"github.com/winezer0/basecrawler/extractor"
	"golang.org/x/net/html"

	"github.com/winezer0/browcrawler/internal/storage"
)

// Discovery 是从页面/JS 中挖掘出的 URL 或接口。
type Discovery struct {
	URL    string `json:"url"`
	Source string `json:"source"` // a@href / iframe@src / form@action / script@src / jsext:js ...
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
					if abs == "" || seen[abs] {
						continue
					}
					seen[abs] = true
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

// ExtractJS 用 assetcrawler 的 extractor 响应处理包解析录制到的 JS 响应体，
// 提取其中的接口 URL 候选（正则档：绝对 URL / 引号路径 / 相对路径三类，
// 包内自带噪声过滤、静态资源过滤、模板占位跳过与去重）。
// AST 引擎（jsext）注入为后续升级路径，当前正则档已覆盖主要字面量场景。
func ExtractJS(jsBodies []storage.JSBody, base string) []Discovery {
	baseURL, err := url.Parse(base)
	if err != nil {
		return nil
	}
	var out []Discovery
	seen := map[string]bool{}
	for _, jb := range jsBodies {
		for _, req := range extractor.FromJS(string(jb.Body), extractor.DefaultOptions()) {
			raw := strings.TrimSpace(req.Raw)
			if raw == "" || strings.ContainsAny(raw, "${}\n") {
				continue
			}
			if req.Confidence == "template" {
				continue // 占位符路径不可直接访问
			}
			abs := resolve(baseURL, raw)
			if abs == "" || seen[abs] {
				continue
			}
			seen[abs] = true
			src := "jsext"
			if req.Source != "" {
				src = "jsext:" + req.Source
			}
			out = append(out, Discovery{URL: abs, Source: src, From: jb.URL, Method: req.Method})
		}
	}
	return out
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
