package crawl

import (
	"strings"
	"testing"

	"github.com/winezer0/browcrawler/internal/storage"
)

func TestExtractJSDeobfuscatesBeforeASTParsing(t *testing.T) {
	bodies := []storage.JSBody{{
		URL:  "http://t.test/static/app.js",
		Body: []byte("eval(\"fetch('\\x2fapi\\x2fv3\\x2fsecret-endpoint')\")"),
	}}
	discoveries := ExtractJS(bodies, "http://t.test/")
	for _, discovery := range discoveries {
		if discovery.URL != "http://t.test/api/v3/secret-endpoint" {
			continue
		}
		if !strings.HasPrefix(discovery.Source, "ast:") {
			t.Fatalf("expected AST parsing after deobfuscation, got %q", discovery.Source)
		}
		return
	}
	t.Fatalf("deobfuscated endpoint was not found: %+v", discoveries)
}

func TestExtractJSLabelsTreeSitterFallback(t *testing.T) {
	source := "import{a}from\"./chunks/runtime.js\";\n" +
		"export{a}from\"./chunks/shared.js\";\n" +
		"fetch(\"/v1/users\");\n" +
		"export{a};"
	bodies := []storage.JSBody{{URL: "http://t.test/assets/app.js", Body: []byte(source)}}
	discoveries := ExtractJS(bodies, "http://t.test/")
	for _, discovery := range discoveries {
		if strings.Contains(discovery.URL, "/v1/users") {
			if discovery.Source != "ast:treesitter" {
				t.Fatalf("ESM request source = %q, want ast:treesitter", discovery.Source)
			}
			return
		}
	}
	t.Fatalf("tree-sitter fallback did not recover the ESM API path: %+v", discoveries)
}

func TestExtractJSRecoversBundleChunksAndRoutes(t *testing.T) {
	source := `o.p="/static/js/",o.u=function(e){return"chunk-"+e+"."+{101:"a1b2c3"}[e]+".js"};var routes=[{path:"/admin/users/:id",name:"user",component:()=>import("./U.js")}];`
	bodies := []storage.JSBody{{URL: "http://t.test/assets/app.js", Body: []byte(source)}}
	discoveries := ExtractJS(bodies, "http://t.test/")
	var gotChunk, gotRoute bool
	for _, discovery := range discoveries {
		if strings.HasPrefix(discovery.Source, "bundle-chunk:") &&
			(strings.Contains(discovery.URL, "chunk-101.a1b2c3.js") || discovery.URL == "http://t.test/assets/U.js") {
			gotChunk = true
		}
		if strings.HasPrefix(discovery.Source, "bundle-route:") &&
			discovery.URL == "http://t.test/admin/users/:id" {
			gotRoute = true
		}
	}
	if !gotChunk || !gotRoute {
		t.Fatalf("missing recovered bundle chunk or route (chunk=%t route=%t): %+v", gotChunk, gotRoute, discoveries)
	}

}
