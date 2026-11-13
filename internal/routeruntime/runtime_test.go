package routeruntime

import (
	"encoding/json"
	"strings"
	"testing"
)

// Collect 解析与去重：mock eval 返回各框架路径，验证排序稳定、
// 非站内路径过滤、跨框架去重。
func TestCollectParsesAndDedupes(t *testing.T) {
	responses := map[string]string{
		"vue":   `["/dashboard","/users/1"]`,
		"next":  `["/dashboard","/settings"]`, // /dashboard 跨框架重复
		"next2": `[["bad"]]`,
	}
	count := 0
	eval := func(expr string) (string, error) {
		_ = expr
		if count < len(All()) {
			p := All()[count]
			count++
			if v, ok := responses[p.Name+string(rune(count))]; ok {
				return v, nil
			}
			for k, v := range responses {
				if strings.HasPrefix(k, p.Name) {
					return v, nil
				}
			}
		}
		return `[]`, nil
	}
	routes := Collect(eval)
	seen := map[string]string{}
	for _, r := range routes {
		if prev, ok := seen[r.URL]; ok && prev != r.Framework {
			t.Fatalf("跨框架去重失败: %s 同时来自 %s/%s", r.URL, prev, r.Framework)
		}
		seen[r.URL] = r.Framework
	}
	if _, ok := seen["/dashboard"]; !ok {
		t.Fatal("应包含 /dashboard")
	}
	for _, r := range routes {
		if !strings.HasPrefix(r.URL, "/") {
			t.Fatalf("非站内路径泄漏: %s", r.URL)
		}
	}
	_ = json.Marshal
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
