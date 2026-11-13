// Package angular：Angular 运行时路由探针（尽力而为）。
// Router 类无法从控制台引用（需 import），唯一入口是 dev 构建暴露的 window.ng；
// 生产构建通常拿不到路由表——该框架主要依赖 generic（pushState hook）。
// 此探针尝试 ng.probe + injector 字符串键（Partial 化的 Router 令牌）。
package angular

import "browcrawler/internal/routeruntime/probe"

// Probe 返回 Angular 探针。
func Probe() probe.Probe {
	return probe.Probe{Name: "angular", JS: `function(){
  var out = [];
  try {
    if (window.ng && ng.probe && ng.getInjector) {
      var root = document.querySelector('app-root');
      if (root) {
        var inj = ng.getInjector(root);
        var r = inj && inj.get && inj.get('Router') || (inj && inj.viewer && null);
        if (r && r.config) {
          function walk(rs) {
            (rs || []).forEach(function (x) {
              if (x.path != null) out.push('/' + String(x.path).replace(/^\//, ''));
              if (x.children) walk(x.children);
            });
          }
          walk(r.config);
        }
      }
    }
  } catch (e) {}
  return JSON.stringify(out);
}`}
}
