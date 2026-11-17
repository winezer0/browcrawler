// runtime_route.go：运行时路由捕获（浏览器级 JS 发现）。
//
// 框架探针分包见 internal/routeruntime（vue/react/angular/next/nuxt/
// sveltekit/generic）。本文件负责采集编排：
//  1. 首次连接后注入 generic 的 history hook（EvalOnNewDocument——SPA 任意
//     框架的编程式导航必经 pushState/replaceState/hashchange）；
//  2. 每次页面 settle 后与交互点击后调用 CollectRuntimeRoutes：跑全部框架
//     探针读运行时路由表，解析为绝对 URL；
//  3. scope 内的新路由写 discovered（source=runtime-route:<框架>）并回灌
//     BFS 队列真实打开——渲染对应懒加载页面，其 XHR/chunk 被 MITM 录制。
//
// 只读约束：探针只读取框架暴露的路由对象，不触发业务逻辑（与
// basecrawler -route-exec 的主动执行模式划清边界）。
package crawl

import (
	"context"
	"net/url"

	"github.com/winezer0/browcrawler/internal/metrics"
	"github.com/winezer0/browcrawler/internal/routeruntime"
	"github.com/winezer0/browcrawler/internal/routeruntime/generic"
	"github.com/winezer0/browcrawler/internal/storage"

	"github.com/go-rod/rod"
)

// HookInjector 是支持"每个新文档自动注入脚本"的驱动能力（RodDriver 实现）。
type HookInjector interface {
	InjectOnNewDocument(ctx context.Context, js string) error
}

// InjectOnNewDocument 实现 HookInjector：经 CDP 在每个新文档创建时执行脚本。
// 连接级错误由 withRetry 自愈重连一次。
func (d *RodDriver) InjectOnNewDocument(ctx context.Context, js string) error {
	return d.withRetry(ctx, func(p *rod.Page) error {
		_, err := p.EvalOnNewDocument(js)
		return err
	})
}

// InjectRuntimeRouteHook 给实现了 HookInjector 的驱动注入 generic history hook。
// 非 rod 驱动或注入失败均静默降级（generic 探针读不到 hook 数组即返回空）。
func InjectRuntimeRouteHook(ctx context.Context, d PageDriver) {
	if hi, ok := d.(HookInjector); ok {
		_ = hi.InjectOnNewDocument(ctx, generic.HookJS)
	}
}

// CollectRuntimeRoutes 返回 scope 内的路由证据。导航是否已见不影响来源保留。
func CollectRuntimeRoutes(ctx context.Context, d PageDriver, pageURL string,
	scope func(string) bool, seen map[string]bool, m metrics.Recorder) []Discovery {

	eval := func(expr string) (string, error) { return d.Eval(ctx, expr) }
	routes := routeruntime.Collect(eval)
	if len(routes) == 0 {
		return nil
	}
	base, err := url.Parse(pageURL)
	if err != nil {
		return nil
	}
	var out []Discovery
	for _, r := range routes {
		ref, rerr := url.Parse(r.URL)
		if rerr != nil {
			continue
		}
		full := base.ResolveReference(ref).String()
		u, perr := url.Parse(full)
		if perr != nil || !scope(u.Hostname()) {
			continue
		}
		out = append(out, Discovery{
			URL:    full,
			Source: "runtime-route:" + r.Framework,
			From:   pageURL,
		})
	}
	if len(out) > 0 {
		m.Inc("crawl.runtime_routes", int64(len(out)))
	}
	return out
}

// collectRuntimeRoutes 把当前页面上捕获的运行时路由落 discovered 并回灌
// BFS 队列（scope 内、未见过、可导航）。depth 沿用当前页深度 +1。
func (c *Crawler) collectRuntimeRoutes(ctx context.Context, it item, seen map[string]bool, queue *[]item) int {
	routes := CollectRuntimeRoutes(ctx, c.driver, it.URL, c.scope, seen, c.metrics)
	count := 0
	for _, d := range routes {
		d.Depth = it.Depth + 1
		n, err := c.writeDiscovery(d)
		if err != nil {
			c.logger.Printf("[crawl] runtime-route encode: %v", err)
			c.noteCompletion("output_failure", 1)
			return count
		}
		count += n
		if !seen[normalize(d.URL)] && isNavigable(d.URL) && it.Depth >= c.cfg.Crawl.Depth {
			c.noteCompletion("depth_boundary_unvisited", 1)
		}
		// 与 links 入队同规则：深度闸门内才回灌（discovered 始终记录）
		if !seen[normalize(d.URL)] && isNavigable(d.URL) && it.Depth < c.cfg.Crawl.Depth {
			*queue = append(*queue, item{URL: d.URL, Depth: it.Depth + 1})
		}
	}
	if count > 0 {
		c.logger.Printf("[crawl] runtime routes: %d new (%s)", count, it.URL)
	}
	return count
}

// onWSEvent 把驱动转出的 WS 事件写入录制层。URL 以 created 事件登记的
// 建连地址为准（帧事件只有 requestID）。
func (c *Crawler) onWSEvent(ev WSEvent) {
	c.wsMu.Lock()
	if ev.URL != "" {
		c.wsURLs[ev.ReqID] = ev.URL
	}
	wsURL := c.wsURLs[ev.ReqID]
	c.wsMu.Unlock()
	if wsURL == "" {
		return // 帧先于 created 到达（罕见）：丢弃，避免无 URL 记录
	}
	info := &storage.WSInfo{
		Event:     ev.Kind,
		RequestID: ev.ReqID,
		Direction: ev.Direction,
		Opcode:    ev.Opcode,
		FrameSeq:  ev.Seq,
	}
	var payload []byte
	if ev.Kind == "frame" {
		payload = ev.Payload
		info.PayloadLen = len(ev.Payload)
	}
	c.rec.RecordWSEvent(wsURL, info, payload)
}
