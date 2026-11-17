package storage

import "sort"

// Caller holds r.mu.
func (r *Recorder) noteRecordingLimit(code string) {
	if r.completionCounts == nil {
		r.completionCounts = map[string]int{}
	}
	r.completionCounts[code]++
}

// Caller holds r.mu. Include events recorded after the last crawl returned.
func (r *Recorder) reportCompletion() *ScanCompletion {
	if r.completion == nil && len(r.completionCounts) == 0 {
		return nil
	}
	counts := map[string]int{}
	if r.completion != nil {
		for _, reason := range r.completion.Reasons {
			counts[reason.Code] += reason.Count
		}
	}
	for code, count := range r.completionCounts {
		counts[code] += count
	}
	result := NewScanCompletion(counts)
	return &result
}

type CompletionReason struct {
	Code  string `json:"code"`
	Count int    `json:"count"`
}

type ScanCompletion struct {
	Status   string             `json:"status"`
	Complete bool               `json:"complete"`
	Reasons  []CompletionReason `json:"reasons"`
}

func NewScanCompletion(counts map[string]int) ScanCompletion {
	r := ScanCompletion{Status: "complete", Complete: true, Reasons: []CompletionReason{}}
	for code, count := range counts {
		if count > 0 {
			r.Reasons = append(r.Reasons, CompletionReason{code, count})
		}
	}
	sort.Slice(r.Reasons, func(i, j int) bool { return r.Reasons[i].Code < r.Reasons[j].Code })
	if len(r.Reasons) > 0 {
		r.Status = "limited"
		r.Complete = false
	}
	for _, code := range []string{"request_failure", "chunk_fetch_failure", "analysis_failure", "output_failure"} {
		if counts[code] > 0 {
			r.Status = "partial_failure"
		}
	}
	for _, pair := range [][2]string{{"fatal_error", "failed"}, {"canceled", "canceled"}, {"timeout", "timed_out"}} {
		if counts[pair[0]] > 0 {
			r.Status = pair[1]
		}
	}
	return r
}

// A session may be reused for multiple tasks: aggregate instead of overwriting a prior limitation.
func (r *Recorder) SetCompletion(completion ScanCompletion) {
	r.mu.Lock()
	defer r.mu.Unlock()
	counts := map[string]int{}
	if r.completion != nil {
		for _, reason := range r.completion.Reasons {
			counts[reason.Code] += reason.Count
		}
	}
	for _, reason := range completion.Reasons {
		counts[reason.Code] += reason.Count
	}
	combined := NewScanCompletion(counts)
	r.completion = &combined
}
