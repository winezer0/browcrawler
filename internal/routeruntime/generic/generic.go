// Package generic：框架无关兜底。
// 读 history hook（由 driver 注入的 pushState/replaceState 包装）捕获到的
// 实际导航路径 + 当前 location。全部框架的最小保障。
package generic

import "browcrawler/internal/routeruntime/probe"

// HookJS 是 driver 在导航前注入的常驻脚本：包装 pushState/replaceState，
// 把每次编程式导航记录进 window.__browcrawler_routes。
const HookJS = `(function(){
  if (window.__browcrawler_hooked) return;
  window.__browcrawler_hooked = true;
  window.__browcrawler_routes = [];
  function rec(u){ try { __browcrawler_routes.push(String(u)); } catch(e){} }
  ['pushState','replaceState'].forEach(function(fn){
    var orig = history[fn];
    if (typeof orig !== 'function') return;
    history[fn] = function(s, t, url){ rec(url || location.pathname + location.search); return orig.apply(history, arguments); };
  });
  window.addEventListener('hashchange', function(){ rec(location.pathname + location.hash); });
}`

// Probe 返回 generic 探针（history hook 记录 + 当前 location）。
func Probe() probe.Probe {
	return probe.Probe{Name: "generic", JS: `function(){
  var out = [];
  try {
    var rs = window.__browcrawler_routes || [];
    for (var i = 0; i < rs.length; i++) out.push(rs[i]);
    out.push(location.pathname + location.search + location.hash);
  } catch (e) {}
  return JSON.stringify(out);
}`}
}
