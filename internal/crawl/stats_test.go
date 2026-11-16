package crawl

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestStatsExposeBundleChunkFetchErrors(t *testing.T) {
	stats := Stats{BundleChunkFetchErrors: 3}

	encoded, err := json.Marshal(stats)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"bundle_chunk_fetch_errors":3`) {
		t.Fatalf("JSON stats omit bundle fetch failures: %s", encoded)
	}
	if !strings.Contains(stats.String(), "bundle_chunk_fetch_errors=3") {
		t.Fatalf("text stats omit bundle fetch failures: %s", stats.String())
	}
}
