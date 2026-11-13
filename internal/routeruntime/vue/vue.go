// Package vue：Vue Router 运行时路由表探针。
// Vue 3：应用根元素挂 __vue_app__，经 globalProperties.$router.getRoutes()
// 取完整 matcher 表（含运行时 router.addRoute 动态注册的路由）。
// Vue 2：遍历 DOM 找 __vue__ 实例的 $router.options.routes。
package vue

import "browcrawler/internal/routeruntime/probe"

// Probe 返回 Vue 框架探针。
func Probe() probe.Probe {
	return probe.Probe{Name: "vue", JS: `function(){
  var out = [];
  try {
    var els = document.querySelectorAll('body *');
    for (var i = 0; i < els.length && out.length === 0; i++) {
      var el = els[i];
      try {
        if (el.__vue_app__) {
          var gp = el.__vue_app__.config && el.__vue_app__.config.globalProperties;
          var r = gp && gp.$router;
          if (r && r.getRoutes) {
            var rs = r.getRoutes();
            for (var j = 0; j < rs.length; j++) if (rs[j].path) out.push(rs[j].path);
            break;
          }
        }
        if (el.__vue__ && el.__vue__.$router && el.__vue__.$router.options && el.__vue__.$router.options.routes) {
          var rs2 = el.__vue__.$router.options.routes;
          for (var k = 0; k < rs2.length; k++) if (rs2[k].path) out.push(rs2[k].path);
          break;
        }
      } catch (e) {}
    }
  } catch (e) {}
  return JSON.stringify(out);
}`}
}
