package storage

import (
	"net/http"
	"testing"

	"github.com/winezer0/requestfilter"
)

func TestFilterRecordsSameShapeRetriesCollapsesAndKeeps2xx(t *testing.T) {
	recs := []Record{{Method: "GET", URL: "http://127.0.0.1:18080/flaky", Status: 503, RequestHeaders: http.Header{"Accept": []string{"text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7"}, "Accept-Encoding": []string{"gzip, deflate, br, zstd"}, "Accept-Language": []string{"en-US,en;q=0.9"}, "Priority": []string{"u=0, i"}, "Sec-Ch-Ua": []string{"\"Not:A-Brand\";v=\"99\", \"Google Chrome\";v=\"145\", \"Chromium\";v=\"145\""}, "Sec-Ch-Ua-Mobile": []string{"?0"}, "Sec-Ch-Ua-Platform": []string{"\"Windows\""}, "Sec-Fetch-Dest": []string{"document"}, "Sec-Fetch-Mode": []string{"navigate"}, "Sec-Fetch-Site": []string{"none"}, "Sec-Fetch-User": []string{"?1"}, "Upgrade-Insecure-Requests": []string{"1"}, "User-Agent": []string{"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/145.0.0.0 Safari/537.36"}}},
		{Method: "GET", URL: "http://127.0.0.1:18080/flaky", Status: 503, RequestHeaders: http.Header{"Accept": []string{"text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7"}, "Accept-Encoding": []string{"gzip, deflate, br, zstd"}, "Accept-Language": []string{"en-US,en;q=0.9"}, "Priority": []string{"u=0, i"}, "Sec-Ch-Ua": []string{"\"Not:A-Brand\";v=\"99\", \"Google Chrome\";v=\"145\", \"Chromium\";v=\"145\""}, "Sec-Ch-Ua-Mobile": []string{"?0"}, "Sec-Ch-Ua-Platform": []string{"\"Windows\""}, "Sec-Fetch-Dest": []string{"document"}, "Sec-Fetch-Mode": []string{"navigate"}, "Sec-Fetch-Site": []string{"none"}, "Sec-Fetch-User": []string{"?1"}, "Upgrade-Insecure-Requests": []string{"1"}, "User-Agent": []string{"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/145.0.0.0 Safari/537.36"}}},
		{Method: "GET", URL: "http://127.0.0.1:18080/flaky", Status: 200, RequestHeaders: http.Header{"Accept": []string{"text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7"}, "Accept-Encoding": []string{"gzip, deflate, br, zstd"}, "Accept-Language": []string{"en-US,en;q=0.9"}, "Priority": []string{"u=0, i"}, "Sec-Ch-Ua": []string{"\"Not:A-Brand\";v=\"99\", \"Google Chrome\";v=\"145\", \"Chromium\";v=\"145\""}, "Sec-Ch-Ua-Mobile": []string{"?0"}, "Sec-Ch-Ua-Platform": []string{"\"Windows\""}, "Sec-Fetch-Dest": []string{"document"}, "Sec-Fetch-Mode": []string{"navigate"}, "Sec-Fetch-Site": []string{"none"}, "Sec-Fetch-User": []string{"?1"}, "Upgrade-Insecure-Requests": []string{"1"}, "User-Agent": []string{"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/145.0.0.0 Safari/537.36"}}}}
	out, err := filterRecords(recs)
	if err != nil {
		t.Fatal(err)
	}
	// 打印逐条判定原因
	{
		reqs := make([]requestfilter.Request, 0, len(recs))
		for _, rec := range recs {
			reqs = append(reqs, requestfilter.Request{
				Method: rec.Method, URL: rec.URL, Header: http.Header(rec.RequestHeaders),
				Body: []byte(rec.RequestBody), BodyCaptured: true})
		}
		res, e := requestfilter.FilterBatch(reqs, requestfilter.Options{
			DedupStrategy: requestfilter.DedupStrategyCoverageParameters})
		if e == nil {
			for i, d := range res.Decisions {
				t.Logf("decision #%d action=%s reason=%s paramstatus=%s url=%q", i+1, d.Action, d.Reason, d.ParameterStatus, reqs[i].URL)
			}
		}
	}
	t.Logf("in=%d out=%d", len(recs), len(out))
	for _, r := range out {
		t.Logf("  kept: %s %s status=%d", r.Method, r.URL, r.Status)
	}
	if len(out) != 1 {
		t.Fatalf("out=%d, want 1 (same shape should collapse)", len(out))
	}
	if out[0].Status != 200 {
		t.Fatalf("kept status=%d, want 200 (2xx-priority retention)", out[0].Status)
	}
}
