package routeruntime

import (
	"strings"
	"testing"
)

// Collect 解析与去重：mock eval 返回各框架路径，验证排序稳定、
// 非站内路径过滤、同框架去重并保留跨框架证据。
func TestCollectParsesAndDedupes(t *testing.T) {
	responses := map[string]string{
		"vue":  `["/dashboard","/dashboard","/users/1","relative"]`,
		"next": `["/dashboard","/settings"]`, // /dashboard 跨框架重复
	}
	count := 0
	eval := func(expr string) (string, error) {
		_ = expr
		if count < len(All()) {
			p := All()[count]
			count++
			if v, ok := responses[p.Name]; ok {
				return v, nil
			}
		}
		return `[]`, nil
	}
	routes := Collect(eval)
	seen := map[string]bool{}
	for _, r := range routes {
		key := r.Framework + ":" + r.URL
		if seen[key] {
			t.Fatalf("重复来源证据: %s", key)
		}
		seen[key] = true
	}
	if !seen["vue:/dashboard"] || !seen["next:/dashboard"] || len(routes) != 4 {
		t.Fatalf("来源丢失: %+v", routes)
	}
	for _, r := range routes {
		if !strings.HasPrefix(r.URL, "/") {
			t.Fatalf("非站内路径泄漏: %s", r.URL)
		}
	}
}

// 全部探针 JS 必须是函数定义（rod Eval 以 (fn).apply 调用）且自带 try/catch。
func TestProbesAreSafeFunctionDefs(t *testing.T) {
	for _, p := range All() {
		if !strings.HasPrefix(p.JS, "function(){") {
			t.Fatalf("%s 探针不是函数定义（rod Eval 要求）", p.Name)
		}
		if !strings.Contains(p.JS, "try") || !strings.Contains(p.JS, "catch") {
			t.Fatalf("%s 探针缺少异常保护", p.Name)
		}
		if p.Name == "" || p.JS == "" {
			t.Fatalf("%s 探针字段为空", p.Name)
		}
	}
}
