// Package nuxt：Nuxt 运行时路由探针。
// Nuxt 2：window.$nuxt.$router；Nuxt 3：window.useNuxtApp().vueApp 上的
// $router（getRoutes 含动态 addRoute）。
package nuxt

import "github.com/winezer0/browcrawler/internal/routeruntime/probe"

// Probe 返回 Nuxt 探针。
func Probe() probe.Probe {
	return probe.Probe{Name: "nuxt", JS: `function(){
  var out = [];
  try {
    var r = null;
    if (window.$nuxt && $nuxt.$router) r = $nuxt.$router;
    else if (typeof useNuxtApp === 'function') {
      var app = useNuxtApp();
      r = app && app.vueApp && app.vueApp.config && app.vueApp.config.globalProperties.$router;
    }
    if (r && r.getRoutes) {
      var rs = r.getRoutes();
      for (var i = 0; i < rs.length; i++) if (rs[i].path) out.push(rs[i].path);
    } else if (r && r.options && r.options.routes) {
      var rs2 = r.options.routes;
      for (var j = 0; j < rs2.length; j++) if (rs2[j].path) out.push(rs2[j].path);
    }
  } catch (e) {}
  return JSON.stringify(out);
}`}
}
