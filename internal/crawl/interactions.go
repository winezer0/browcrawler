package crawl

import (
	"context"
	"fmt"
	"time"

	"github.com/go-rod/rod"
)

// InteractionTrigger 是可选的交互式探索能力（katana -interactive / crawlergo
// 事件分发的简化版）：页面加载后对可点击元素逐个派发 click，让 JS 侧行为
// （onclick fetch 等）真实触发，其请求经 goproxy 进入请求包。
// hook addEventListener 的完整状态机版本视简化版收益再评估。
type InteractionTrigger interface {
	// TriggerInteractions 对当前页面最多派发 maxActions 次点击，返回实际派发数。
	TriggerInteractions(ctx context.Context, maxActions int) (int, error)
}

// collectInteractionsJS 收集可点击元素（排他处理：不含表单 submit，避免与
// 表单提交逻辑重复触发同一请求）。
const collectInteractionsJS = `function() {
  var sel = 'button:not([disabled]), [role=button], [onclick], input[type=button], a[href^="#"]';
  var targets = Array.from(document.querySelectorAll(sel)).filter(el =>
    !(el.tagName === 'BUTTON' && (el.type === 'submit' || el.type === 'reset'))
  );
  return targets.slice(0, 200).map((el, i) => ({
    index: i,
    tag: el.tagName.toLowerCase(),
    text: (el.textContent || '').trim().slice(0, 30)
  }));
}`

// clickInteractionJS 点击第 n 个可点击元素；收集与点击间 DOM 可能因框架重渲染
// 而漂移，点击目标以实时 query 为准，越界即跳过（爬虫场景可接受）。
const clickInteractionJS = `function(n) {
  var sel = 'button:not([disabled]), [role=button], [onclick], input[type=button], a[href^="#"]';
  var targets = Array.from(document.querySelectorAll(sel)).filter(el =>
    !(el.tagName === 'BUTTON' && (el.type === 'submit' || el.type === 'reset'))
  );
  var el = targets[n];
  if (!el) return 'missing';
  el.click();
  return 'clicked';
}`

// TriggerInteractions 实现 InteractionTrigger。
func (d *RodDriver) TriggerInteractions(ctx context.Context, maxActions int) (int, error) {
	if maxActions <= 0 {
		return 0, nil
	}
	var targets []struct {
		Index int    `json:"index"`
		Tag   string `json:"tag"`
		Text  string `json:"text"`
	}
	err := d.withRetry(ctx, func(p *rod.Page) error {
		obj, e := p.Eval(collectInteractionsJS)
		if e != nil {
			return e
		}
		return obj.Value.Unmarshal(&targets)
	})
	if err != nil {
		return 0, fmt.Errorf("collect interactions: %w", err)
	}
	if len(targets) > maxActions {
		targets = targets[:maxActions]
	}
	clicked := 0
	for _, t := range targets {
		if ctx.Err() != nil {
			break
		}
		var how string
		err := d.withRetry(ctx, func(p *rod.Page) error {
			obj, e := p.Eval(clickInteractionJS, t.Index)
			if e != nil {
				return e
			}
			how = obj.Value.Str()
			return nil
		})
		if err != nil || how == "missing" {
			continue
		}
		clicked++
		// 每次点击后等待 settle，让 onclick 里的 fetch/XHR 发出并被录制。
		select {
		case <-ctx.Done():
			return clicked, ctx.Err()
		case <-time.After(400 * time.Millisecond):
		}
	}
	return clicked, nil
}
