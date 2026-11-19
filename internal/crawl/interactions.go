package crawl

import (
	"context"
	"fmt"
	"time"

	"github.com/go-rod/rod"
)

// InteractionTrigger is the optional browser-driven exploration capability.
// It activates controls after each DOM change and performs bounded scrolling
// so nested menus and scroll-triggered requests can be observed.
type InteractionTrigger interface {
	// TriggerInteractions dispatches at most maxActions clicks on the current page.
	TriggerInteractions(ctx context.Context, maxActions int) (int, error)
}

// Stable keys let the interaction loop re-query after framework rerenders
// without relying on stale element indexes. Volatile state such as
// aria-expanded is deliberately excluded so an opened menu is not clicked shut.
const collectInteractionsJS = `function() {
  var sel = 'button:not([disabled]), [role=button], [role=menuitem], [onclick], input[type=button], a[href^="#"]';
  var counts = new Map();
  return Array.from(document.querySelectorAll(sel)).filter(function(el) {
    if ((el.tagName === 'BUTTON' && (el.type === 'submit' || el.type === 'reset')) ||
        el.disabled || el.getAttribute('aria-disabled') === 'true') return false;
    var style = window.getComputedStyle(el);
    return style.display !== 'none' && style.visibility !== 'hidden' && el.getClientRects().length > 0;
  }).slice(0, 200).map(function(el) {
    var tag = el.tagName.toLowerCase();
    var identity = ['id', 'data-testid', 'name', 'href', 'data-page', 'aria-label']
      .map(function(name) { return el.getAttribute(name) || ''; }).join('\u001f');
    var text = (el.innerText || el.textContent || '').replace(/\s+/g, ' ').trim().slice(0, 80);
    var base = tag + '\u001e' + (identity.replace(/\u001f/g, '') ? identity : text);
    var ordinal = counts.get(base) || 0;
    counts.set(base, ordinal + 1);
    return {key: base + '\u001d' + ordinal, tag: tag, text: text};
  });
}`

const clickInteractionJS = `function(key) {
  var sel = 'button:not([disabled]), [role=button], [role=menuitem], [onclick], input[type=button], a[href^="#"]';
  var counts = new Map();
  var targets = Array.from(document.querySelectorAll(sel)).filter(function(el) {
    if ((el.tagName === 'BUTTON' && (el.type === 'submit' || el.type === 'reset')) ||
        el.disabled || el.getAttribute('aria-disabled') === 'true') return false;
    var style = window.getComputedStyle(el);
    return style.display !== 'none' && style.visibility !== 'hidden' && el.getClientRects().length > 0;
  }).slice(0, 200).map(function(el) {
    var tag = el.tagName.toLowerCase();
    var identity = ['id', 'data-testid', 'name', 'href', 'data-page', 'aria-label']
      .map(function(name) { return el.getAttribute(name) || ''; }).join('\u001f');
    var text = (el.innerText || el.textContent || '').replace(/\s+/g, ' ').trim().slice(0, 80);
    var base = tag + '\u001e' + (identity.replace(/\u001f/g, '') ? identity : text);
    var ordinal = counts.get(base) || 0;
    counts.set(base, ordinal + 1);
    return {key: base + '\u001d' + ordinal, el: el};
  });
  var target = targets.find(function(item) { return item.key === key; });
  if (!target) return 'missing';
  target.el.click();
  return 'clicked';
}`

const interactionScrollStateJS = `function() {
  var root = document.scrollingElement || document.documentElement;
  return {height: root.scrollHeight, top: window.scrollY, viewport: window.innerHeight};
}`

const scrollInteractionJS = `function() {
  var root = document.scrollingElement || document.documentElement;
  var maxTop = Math.max(0, root.scrollHeight - window.innerHeight);
  var step = Math.max(300, Math.floor(window.innerHeight * 0.8));
  window.scrollTo(0, Math.min(maxTop, window.scrollY + step));
  return 'scrolled';
}`

type interactionTarget struct {
	Key  string `json:"key"`
	Tag  string `json:"tag"`
	Text string `json:"text"`
}

type interactionScrollState struct {
	Height   int `json:"height"`
	Top      int `json:"top"`
	Viewport int `json:"viewport"`
}

// TriggerInteractions repeatedly collects current visible controls, clicks
// each stable target once, and scrolls through newly loaded content. Both
// operations are bounded: click count is maxActions and scrolling is capped at
// 16 viewport steps with two unchanged bottom checks before stopping.
func (d *RodDriver) TriggerInteractions(ctx context.Context, maxActions int) (int, error) {
	if maxActions <= 0 {
		return 0, nil
	}

	clicked := 0
	scrollSteps := 0
	idleAtBottom := 0
	lastHeight := -1
	handled := make(map[string]struct{})
	for clicked < maxActions {
		if err := ctx.Err(); err != nil {
			return clicked, err
		}

		targets, err := d.collectInteractionTargets(ctx)
		if err != nil {
			return clicked, fmt.Errorf("collect interactions: %w", err)
		}
		var next *interactionTarget
		for i := range targets {
			if _, exists := handled[targets[i].Key]; exists {
				continue
			}
			next = &targets[i]
			break
		}
		if next != nil {
			handled[next.Key] = struct{}{}
			var outcome string
			err := d.withRetry(ctx, func(p *rod.Page) error {
				obj, evalErr := p.Eval(clickInteractionJS, next.Key)
				if evalErr != nil {
					return evalErr
				}
				outcome = obj.Value.Str()
				return nil
			})
			if err != nil {
				if ctx.Err() != nil {
					return clicked, ctx.Err()
				}
				continue
			}
			if outcome != "clicked" {
				continue
			}
			clicked++
			if err := waitForInteractionSettle(ctx); err != nil {
				return clicked, err
			}
			continue
		}

		if scrollSteps >= 16 {
			break
		}
		var state interactionScrollState
		err = d.withRetry(ctx, func(p *rod.Page) error {
			obj, evalErr := p.Eval(interactionScrollStateJS)
			if evalErr != nil {
				return evalErr
			}
			return obj.Value.Unmarshal(&state)
		})
		if err != nil {
			return clicked, fmt.Errorf("read interaction scroll state: %w", err)
		}
		atBottom := state.Top+state.Viewport >= state.Height-2
		if atBottom && state.Height == lastHeight {
			idleAtBottom++
		} else {
			idleAtBottom = 0
		}
		if idleAtBottom >= 2 {
			break
		}
		lastHeight = state.Height
		err = d.withRetry(ctx, func(p *rod.Page) error {
			_, evalErr := p.Eval(scrollInteractionJS)
			return evalErr
		})
		if err != nil {
			return clicked, fmt.Errorf("scroll during interaction discovery: %w", err)
		}
		scrollSteps++
		if err := waitForInteractionSettle(ctx); err != nil {
			return clicked, err
		}
	}
	return clicked, nil
}

func (d *RodDriver) collectInteractionTargets(ctx context.Context) ([]interactionTarget, error) {
	var targets []interactionTarget
	err := d.withRetry(ctx, func(p *rod.Page) error {
		obj, evalErr := p.Eval(collectInteractionsJS)
		if evalErr != nil {
			return evalErr
		}
		return obj.Value.Unmarshal(&targets)
	})
	return targets, err
}

func waitForInteractionSettle(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(350 * time.Millisecond):
		return nil
	}
}
