package storage

import "testing"

func TestCompletionStatusAndAggregation(t *testing.T) {
	for _, tc := range []struct{ code, status string }{{"", "complete"}, {"page_limit", "limited"}, {"request_failure", "partial_failure"}, {"timeout", "timed_out"}, {"fatal_error", "failed"}} {
		counts := map[string]int{}
		if tc.code != "" {
			counts[tc.code] = 1
		}
		got := NewScanCompletion(counts)
		if got.Status != tc.status || got.Complete != (tc.code == "") {
			t.Fatalf("%s: %+v", tc.code, got)
		}
	}
	r := &Recorder{}
	r.SetCompletion(NewScanCompletion(map[string]int{"page_limit": 2}))
	r.SetCompletion(NewScanCompletion(nil))
	if r.completion.Complete || r.completion.Reasons[0].Count != 2 {
		t.Fatalf("lost prior limitation: %+v", r.completion)
	}
	r.noteRecordingLimit("output_failure")
	if got := r.reportCompletion(); got.Status != "partial_failure" || len(got.Reasons) != 2 {
		t.Fatalf("recording failure hidden: %+v", got)
	}
}
