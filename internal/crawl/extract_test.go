package crawl

import (
	"strings"
	"testing"

	"github.com/winezer0/browcrawler/internal/storage"
)

// ExtractJS uses the shared parser and preserves source and HTTP method metadata.
func TestExtractJSParserModes(t *testing.T) {
	bodies := []storage.JSBody{
		{URL: "http://t.test/static/app.js", Body: []byte(
			`fetch("/api/v1/user/list");
			 var u = "https://api.partner.example/v1/me";
			 const p = "./api/v2/order";
			 var img = "/static/logo.png"; // 静态资源应被包内过滤
			 var tpl = "/user/${id}/x";    // 模板占位应被过滤`)},
	}
	discs := ExtractJS(bodies, "http://t.test/")
	byURL := map[string]Discovery{}
	for _, d := range discs {
		byURL[d.URL] = d
		if !strings.HasPrefix(d.Source, "regex:") && !strings.HasPrefix(d.Source, "ast:") {
			t.Fatalf("source 前缀应为 regex: %s", d.Source)
		}
	}
	if _, ok := byURL["http://t.test/api/v1/user/list"]; !ok {
		t.Fatal("应提取引号绝对路径 /api/v1/user/list")
	}
	if _, ok := byURL["https://api.partner.example/v1/me"]; !ok {
		t.Fatal("应提取绝对 URL")
	}
	if _, ok := byURL["http://t.test/static/logo.png"]; ok {
		t.Fatal("静态资源应被包内过滤")
	}
	if _, ok := byURL["http://t.test/api/v2/order"]; !ok {
		t.Fatal("相对路径应相对 seed 解析")
	}
	for _, bad := range []string{"http://t.test/user/${id}/x"} {
		if _, ok := byURL[bad]; ok {
			t.Fatalf("模板占位不应提取: %s", bad)
		}
	}
}
