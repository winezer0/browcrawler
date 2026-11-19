package crawl

import (
	"context"
	"fmt"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

// InteractionTrigger is the optional browser-driven exploration capability.
// It activates controls after each DOM change and performs bounded scrolling
// so nested menus and scroll-triggered requests can be observed.
type InteractionTrigger interface {
	// TriggerInteractions dispatches at most maxActions clicks on the current page.
	TriggerInteractions(ctx context.Context, maxActions int) (int, error)
}

// Target collection crosses open shadow roots and same-origin iframes. Stable
// root paths plus element identity let the loop re-query after framework
// rerenders without relying on stale element indexes. Volatile state such as
// aria-expanded is deliberately excluded so an opened menu is not clicked shut.
const interactionTargetsJS = `function() {
  var sel = 'button:not([disabled]), [role=button], [role=menuitem], [onclick], input[type=button], a[href^="#"]';
  var targets = [];

  function rootIdentity(el, ordinal) {
    var identity = ['id', 'name', 'src', 'data-testid', 'aria-label']
      .map(function(name) { return el.getAttribute(name) || ''; })
      .filter(Boolean).join('\u001f');
    return el.tagName.toLowerCase() + ':' + (identity || ordinal);
  }

  function walk(root, rootPath) {
    var counts = new Map();
    Array.from(root.querySelectorAll(sel)).forEach(function(el) {
      if ((el.tagName === 'BUTTON' && (el.type === 'submit' || el.type === 'reset')) ||
          el.disabled || el.getAttribute('aria-disabled') === 'true') return;
      var view = (el.ownerDocument && el.ownerDocument.defaultView) || window;
      var style = view.getComputedStyle(el);
      if (style.display === 'none' || style.visibility === 'hidden' || !el.getClientRects().length) return;

      var tag = el.tagName.toLowerCase();
      var identity = ['id', 'data-testid', 'name', 'href', 'data-page', 'aria-label']
        .map(function(name) { return el.getAttribute(name) || ''; }).join('\u001f');
      var text = (el.innerText || el.textContent || '').replace(/\s+/g, ' ').trim().slice(0, 80);
      var base = rootPath + '\u001e' + tag + '\u001e' +
        (identity.replace(/\u001f/g, '') ? identity : text);
      var ordinal = counts.get(base) || 0;
      counts.set(base, ordinal + 1);
      targets.push({key: base + '\u001d' + ordinal, tag: tag, text: text, el: el});
    });

    Array.from(root.querySelectorAll('*')).forEach(function(el, ordinal) {
      if (el.shadowRoot) walk(el.shadowRoot, rootPath + '\u001cshadow:' + rootIdentity(el, ordinal));
      if (el.tagName === 'IFRAME') {
        try {
          var frameDocument = el.contentDocument;
          if (frameDocument && frameDocument.documentElement) {
            walk(frameDocument, rootPath + '\u001cframe:' + rootIdentity(el, ordinal));
          }
        } catch (_) {
          // Cross-origin frames cannot be inspected from the parent document.
        }
      }
    });
  }

  walk(document, 'document');
  return targets.slice(0, 200);
}`

const collectInteractionsJS = `function() {
  return (` + interactionTargetsJS + `)().map(function(target) {
    return {key: target.key, tag: target.tag, text: target.text};
  });
}`

const clickInteractionJS = `function(key) {
  var target = (` + interactionTargetsJS + `)().find(function(item) { return item.key === key; });
  if (!target) return 'missing';
  target.el.click();
  return 'clicked';
}`

const interactionScrollStateJS = `function() {
  var root = document.scrollingElement || document.documentElement;
  return {height: root.scrollHeight, top: window.scrollY, viewport: window.innerHeight, width: window.innerWidth};
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
	Width    int `json:"width"`
}

func interactionScrollDelta(viewport int) int {
	step := int(float64(viewport) * 0.8)
	if step < 300 {
		return 300
	}
	return step
}

// TriggerInteractions repeatedly collects current visible controls, clicks
// each stable target once, and scrolls through newly loaded content. Target
// traversal includes open shadow roots and same-origin frames. Both operations
// are bounded: click count is maxActions and scrolling is capped at 16 viewport
// steps with two unchanged bottom checks before stopping.
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
		if state.Height <= state.Viewport+2 {
			break
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
			if state.Width > 1 && state.Viewport > 1 {
				if moveErr := p.Mouse.MoveTo(proto.Point{X: float64(state.Width / 2), Y: float64(state.Viewport / 2)}); moveErr != nil {
					return moveErr
				}
			}
			return p.Mouse.Scroll(0, float64(interactionScrollDelta(state.Viewport)), 1)
		})
		if err != nil {
			return clicked, fmt.Errorf("scroll during interaction discovery: %w", err)
		}
		scrollSteps++
		if err := waitForScrollSettle(ctx); err != nil {
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

func waitForScrollSettle(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(600 * time.Millisecond):
		return nil
	}
}
