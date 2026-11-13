// Package react：React 系运行时路由探针。
// React Router 本体无全局对象；可触达的运行时面：
//   - Remix / React Router 7 framework 模式：window.__remixManifest.routes
//   - React Router 6 data mode（window.__reactRouterContext）路由无全局表，靠 generic
//
// 纯 CSR 的 React 应用由 generic（pushState hook）兜底。
package react

import "github.com/winezer0/browcrawler/internal/routeruntime/probe"

// Probe 返回 React 系探针。
func Probe() probe.Probe {
	return probe.Probe{Name: "react", JS: `function(){
  var out = [];
  try {
    if (window.__remixManifest && __remixManifest.routes) {
      for (var id in __remixManifest.routes) {
        var r = __remixManifest.routes[id];
        if (r && r.path) out.push(r.path);
      }
    }
  } catch (e) {}
  return JSON.stringify(out);
}`}
}
