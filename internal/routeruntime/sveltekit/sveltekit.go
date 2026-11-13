// Package sveltekit：SvelteKit 运行时路由探针。
// SvelteKit 无稳定全局路由表（module 内部经 importmap 引用）；生产构建
// 只能靠 generic（pushState hook）与 HTML link 提取兜底。此探针尝试开发
// 构建的 __sveltekit 调试对象，命中即收益，未命中静默跳过。
package sveltekit

import "browcrawler/internal/routeruntime/probe"

// Probe 返回 SvelteKit 探针。
func Probe() probe.Probe {
	return probe.Probe{Name: "sveltekit", JS: `function(){
  var out = [];
  try {
    for (var k in window) {
      if (k.indexOf('__sveltekit') === 0 && window[k] && window[k].routes) {
        for (var p in window[k].routes) out.push(p);
      }
    }
  } catch (e) {}
  return JSON.stringify(out);
}`}
}
