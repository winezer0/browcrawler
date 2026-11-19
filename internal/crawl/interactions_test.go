package crawl

import (
	"strings"
	"testing"
)

func TestInteractionScriptsRecollectStableTargetsAndScroll(t *testing.T) {
	for _, fragment := range []string{"data-testid", "data-page", "aria-label", "getClientRects", "type === 'submit'"} {
		if !strings.Contains(collectInteractionsJS, fragment) {
			t.Errorf("collection script missing %q", fragment)
		}
	}
	if !strings.Contains(clickInteractionJS, "targets.find") || !strings.Contains(clickInteractionJS, "target.el.click()") {
		t.Fatal("click script must resolve the current DOM target by stable key")
	}
	if !strings.Contains(interactionScrollStateJS, "scrollHeight") || !strings.Contains(scrollInteractionJS, "window.scrollTo") {
		t.Fatal("interaction exploration must inspect and advance page scrolling")
	}
}
