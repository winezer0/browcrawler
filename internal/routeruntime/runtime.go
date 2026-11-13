// Package routeruntime：浏览器内运行时路由捕获（按框架分包探针）。
//
// 设计：每个主流前端框架一个子包，各持一段只读探针 JS（读取框架暴露在
// 运行时的路由表）；generic 包提供框架无关的兜底（pushState/hashchange
// hook 与 location 检查）。调度器 Collect 依次执行全部探针，Go 侧解析、
// 去重，交给调用方回灌队列。
//
// 只读原则：探针只读取对象/触发查询，不修改页面状态、不执行业务代码
// （区别于 basecrawler -route-exec 的主动执行模式）。
package routeruntime

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/winezer0/browcrawler/internal/routeruntime/angular"
	"github.com/winezer0/browcrawler/internal/routeruntime/generic"
	"github.com/winezer0/browcrawler/internal/routeruntime/next"
	"github.com/winezer0/browcrawler/internal/routeruntime/nuxt"
	"github.com/winezer0/browcrawler/internal/routeruntime/probe"
	"github.com/winezer0/browcrawler/internal/routeruntime/react"
	"github.com/winezer0/browcrawler/internal/routeruntime/sveltekit"
	"github.com/winezer0/browcrawler/internal/routeruntime/vue"
)

// Route 是一次运行时捕获到的路由。
type Route struct {
	URL       string // 归一化前的原始路径/URL（由调用方相对 seed 解析）
	Framework string // 探针名：vue/react/angular/next/nuxt/sveltekit/generic
}

// Probe 是单个框架的运行时探针（probe.Probe 的包内别名）。
type Probe = probe.Probe

// evalJS 在当前页面执行 JS 并返回其字符串结果。
type evalJS func(expr string) (string, error)

// All 返回全部框架探针；顺序即执行顺序（generic 兜底放最后）。
func All() []Probe {
	return []Probe{
		vue.Probe(), react.Probe(), angular.Probe(), next.Probe(),
		nuxt.Probe(), sveltekit.Probe(), generic.Probe(),
	}
}

// Collect 在当前页面上运行全部框架探针，返回去重后的路由集。
// 单个探针失败静默跳过（探针 JS 均自带 try/catch，此处兜底解析错误）。
func Collect(eval evalJS) []Route {
	var out []Route
	seen := map[string]bool{}
	for _, p := range All() {
		raw, err := eval(p.JS)
		if err != nil || raw == "" {
			continue
		}
		var paths []string
		if err := json.Unmarshal([]byte(raw), &paths); err != nil {
			continue
		}
		for _, path := range paths {
			path = strings.TrimSpace(path)
			if path == "" || !strings.HasPrefix(path, "/") {
				continue // 探针只报站内路径，绝对 URL 由通用提取层负责
			}
			if seen[path] {
				continue
			}
			seen[path] = true
			out = append(out, Route{URL: path, Framework: p.Name})
		}
	}
	return out
}

// abs 把相对路由解析为绝对 URL（基于当前页 location）。
func abs(base, path string) string {
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path
	}
	return fmt.Sprintf("%s%s", strings.TrimRight(base, "/"), path)
}
