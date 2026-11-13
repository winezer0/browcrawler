// Package probe：框架探针共享类型（叶子包，被 routeruntime 与各框架包共同依赖）。
package probe

// Probe 是单个框架的运行时探针。
type Probe struct {
	Name string // 框架标识：vue/react/angular/next/nuxt/sveltekit/generic
	JS   string // 浏览器端执行的 IIFE，返回 JSON 字符串数组（或空数组）
}
