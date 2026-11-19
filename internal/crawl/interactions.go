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
  var targets = [];

  function intersection(rect, clip) {
    var left = Math.max(rect.left, clip.left);
    var top = Math.max(rect.top, clip.top);
    var right = Math.min(rect.right, clip.right);
    var bottom = Math.min(rect.bottom, clip.bottom);
    if (right - left < 2 || bottom - top < 2) return null;
    return {left: left, top: top, right: right, bottom: bottom};
  }

  function rootIdentity(el, ordinal) {
    var identity = ['id', 'data-testid', 'aria-label', 'name']
      .map(function(name) { return el.getAttribute(name) || ''; })
      .filter(Boolean).join('\u001f');
    return el.tagName.toLowerCase() + ':' + (identity || ordinal);
  }

  function addWindow(view, rootPath, offsetX, offsetY, clip) {
    var doc = view.document;
    var root = doc.scrollingElement || doc.documentElement;
    var box = intersection({
      left: offsetX, top: offsetY,
      right: offsetX + view.innerWidth, bottom: offsetY + view.innerHeight
    }, clip);
    if (!box || root.scrollHeight <= view.innerHeight + 2) return;
    targets.push({
      key: rootPath + '\u001ewindow',
      top: Math.max(0, view.scrollY || root.scrollTop || 0),
      height: root.scrollHeight,
      viewport: view.innerHeight,
      x: (box.left + box.right) / 2,
      y: (box.top + box.bottom) / 2
    });
  }

  function walk(root, rootPath, view, offsetX, offsetY, clip) {
    Array.from(root.querySelectorAll('*')).forEach(function(el, ordinal) {
      var ownerView = (el.ownerDocument && el.ownerDocument.defaultView) || view;
      var style = ownerView.getComputedStyle(el);
      var isDocumentRoot = el === ownerView.document.scrollingElement ||
        el === ownerView.document.documentElement || el === ownerView.document.body;
      if (!isDocumentRoot && /^(auto|scroll|overlay)$/.test(style.overflowY || '')) {
        var rect = el.getBoundingClientRect();
        var box = intersection({
          left: offsetX + rect.left, top: offsetY + rect.top,
          right: offsetX + rect.right, bottom: offsetY + rect.bottom
        }, clip);
        if (box && el.scrollHeight > el.clientHeight + 2) {
          targets.push({
            key: rootPath + '\u001e' + rootIdentity(el, ordinal),
            top: Math.max(0, el.scrollTop),
            height: el.scrollHeight,
            viewport: el.clientHeight,
            x: (box.left + box.right) / 2,
            y: (box.top + box.bottom) / 2
          });
        }
      }

      if (el.shadowRoot) {
        walk(el.shadowRoot, rootPath + '\u001cshadow:' + rootIdentity(el, ordinal), view, offsetX, offsetY, clip);
      }
      if (el.tagName === 'IFRAME') {
        try {
          var frameDocument = el.contentDocument;
          var frameView = frameDocument && frameDocument.defaultView;
          if (frameDocument && frameView && frameDocument.documentElement) {
            var frameRect = el.getBoundingClientRect();
            var childOffsetX = offsetX + frameRect.left + el.clientLeft;
            var childOffsetY = offsetY + frameRect.top + el.clientTop;
            var frameClip = intersection({
              left: childOffsetX, top: childOffsetY,
              right: childOffsetX + Math.min(el.clientWidth, frameView.innerWidth),
              bottom: childOffsetY + Math.min(el.clientHeight, frameView.innerHeight)
            }, clip);
            if (frameClip) {
              var framePath = rootPath + '\u001cframe:' + rootIdentity(el, ordinal);
              walk(frameDocument, framePath, frameView, childOffsetX, childOffsetY, frameClip);
              addWindow(frameView, framePath, childOffsetX, childOffsetY, frameClip);
            }
          }
        } catch (_) {
          // Cross-origin frames cannot be inspected from the parent document.
        }
      }
    });
  }

  walk(document, 'document', window, 0, 0, {
    left: 0, top: 0, right: window.innerWidth, bottom: window.innerHeight
  });
  addWindow(window, 'document', 0, 0, {
    left: 0, top: 0, right: window.innerWidth, bottom: window.innerHeight
  });
  return targets;
}`

type interactionTarget struct {
	Key  string `json:"key"`
	Tag  string `json:"tag"`
	Text string `json:"text"`
}

type interactionScrollTarget struct {
	Key      string  `json:"key"`
	Height   int     `json:"height"`
	Top      int     `json:"top"`
	Viewport int     `json:"viewport"`
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
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
// are bounded: click count is maxActions and scrolling is capped at 32 steps
// overall and 16 steps per scroll container.
func (d *RodDriver) TriggerInteractions(ctx context.Context, maxActions int) (int, error) {
	if maxActions <= 0 {
		return 0, nil
	}

	clicked := 0
	scrollSteps := 0
	scrollStepsByTarget := make(map[string]int)
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

		if scrollSteps >= 32 {
			break
		}
		var scrollTargets []interactionScrollTarget
		err = d.withRetry(ctx, func(p *rod.Page) error {
			obj, evalErr := p.Eval(interactionScrollStateJS)
			if evalErr != nil {
				return evalErr
			}
			return obj.Value.Unmarshal(&scrollTargets)
		})
		if err != nil {
			return clicked, fmt.Errorf("read interaction scroll targets: %w", err)
		}
		var nextScroll *interactionScrollTarget
		for i := range scrollTargets {
			maxTop := scrollTargets[i].Height - scrollTargets[i].Viewport
			if scrollTargets[i].Top >= maxTop-2 || scrollStepsByTarget[scrollTargets[i].Key] >= 16 {
				continue
			}
			nextScroll = &scrollTargets[i]
			break
		}
		if nextScroll == nil {
			break
		}
		delta := interactionScrollDelta(nextScroll.Viewport)
		err = d.withRetry(ctx, func(p *rod.Page) error {
			if moveErr := p.Mouse.MoveTo(proto.Point{X: nextScroll.X, Y: nextScroll.Y}); moveErr != nil {
				return moveErr
			}
			return p.Mouse.Scroll(0, float64(delta), 1)
		})
		if err != nil {
			return clicked, fmt.Errorf("scroll during interaction discovery: %w", err)
		}
		scrollSteps++
		scrollStepsByTarget[nextScroll.Key]++
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
