// Package next：Next.js 运行时路由探针。
// Pages Router：window.next.router.components 的键即已注册页面路径；
// App Router：self.__next_f RSC 流属静态可解析（assetcrawler 已覆盖），
// 此处只做运行时对象兜底。
package next

import "browcrawler/internal/routeruntime/probe"

// Probe 返回 Next.js 探针。
func Probe() probe.Probe {
	return probe.Probe{Name: "next", JS: `function(){
  var out = [];
  try {
    if (window.next && next.router && next.router.components) {
      for (var p in next.router.components) out.push(p);
    }
  } catch (e) {}
  return JSON.stringify(out);
}`}
}
