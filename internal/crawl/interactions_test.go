package crawl

import (
	"strings"
	"testing"
)

func TestInteractionScriptsRecollectStableTargetsAndScroll(t *testing.T) {
	for _, fragment := range []string{"data-testid", "data-page", "aria-label", "getClientRects", "type === 'submit'"} {
		if !strings.Contains(interactionTargetsJS, fragment) {
			t.Errorf("collection script missing %q", fragment)
		}
	}
	for _, fragment := range []string{"shadowRoot", "contentDocument", "ownerDocument.defaultView", "frame:"} {
		if !strings.Contains(interactionTargetsJS, fragment) {
			t.Errorf("interaction traversal missing %q", fragment)
		}
	}
	if !strings.Contains(collectInteractionsJS, "shadowRoot") || !strings.Contains(clickInteractionJS, "contentDocument") {
		t.Fatal("collect and click scripts must embed the shared interaction traversal")
	}
	if strings.Contains(collectInteractionsJS, "${interactionTargetsJS}") || strings.Contains(clickInteractionJS, "${interactionTargetsJS}") {
		t.Fatal("interaction traversal placeholder was not expanded into the JavaScript scripts")
	}
	if !strings.Contains(clickInteractionJS, ".find(function(item)") || !strings.Contains(clickInteractionJS, "target.el.click()") {
		t.Fatal("click script must resolve the current DOM target by stable key")
	}
	if !strings.Contains(interactionScrollStateJS, "scrollHeight") || !strings.Contains(interactionScrollStateJS, "innerHeight") {
		t.Fatal("interaction exploration must inspect page scrolling")
	}
	if got := interactionScrollDelta(800); got != 640 {
		t.Fatalf("scroll delta for 800px viewport = %d, want 640", got)
	}
	if got := interactionScrollDelta(200); got != 300 {
		t.Fatalf("scroll delta floor = %d, want 300", got)
	}
}
