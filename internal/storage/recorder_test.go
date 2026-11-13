package storage

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readOne(t *testing.T, path string) Record {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var rec Record
	if err := json.Unmarshal(bytes.TrimSpace(b), &rec); err != nil {
		t.Fatalf("解析 JSONL: %v / 内容=%q", err, b)
	}
	return rec
}

func TestLargeResponseOnlyMetadataAndStreamIntact(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "traffic.jsonl")
	r, err := New(path, "s1", 1024, true)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "http://x.test/big", nil)
	rec := r.Begin(req)
	payload := bytes.Repeat([]byte("x"), 4096)
	resp := &http.Response{
		StatusCode: 200, Status: "200 OK",
		Header:        http.Header{"Content-Type": []string{"text/plain"}},
		Body:          io.NopCloser(bytes.NewReader(payload)),
		ContentLength: int64(len(payload)), Request: req,
	}
	r.Attach(rec, resp)
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("录制层截断了真实响应流: got %d bytes want %d", len(got), len(payload))
	}
	_ = resp.Body.Close()
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	out := readOne(t, path)
	if out.BodySize != 4096 || out.BodyOmitted != "too_large" || out.Body != "" {
		t.Fatalf("超限响应应只记元信息: size=%d omit=%q body_len=%d", out.BodySize, out.BodyOmitted, len(out.Body))
	}
}

func TestRawDumpNeverTouchesBody(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "traffic.jsonl")
	r, err := New(path, "s1", 4096, false)
	if err != nil {
		t.Fatal(err)
	}
	r.SetRawDump(true)
	req := httptest.NewRequest(http.MethodPost, "http://x.test/api", strings.NewReader("SECRET-BODY"))
	req.Header.Set("X-Test", "yes")
	rec := r.Begin(req)
	if got, _ := io.ReadAll(req.Body); string(got) != "SECRET-BODY" {
		t.Fatal("Begin 不得消费或改写请求体")
	}
	resp := &http.Response{
		StatusCode: 200, Status: "200 OK",
		Header:  http.Header{"Content-Type": []string{"application/json"}},
		Body:    io.NopCloser(strings.NewReader("RESPONSE-BODY")),
		Request: req,
	}
	r.Attach(rec, resp)
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	out := readOne(t, path)
	if !strings.Contains(out.RequestRaw, "X-Test: yes") || !strings.Contains(out.RequestRaw, "POST ") {
		t.Fatalf("request_raw 缺失: %q", out.RequestRaw)
	}
	if strings.Contains(out.RequestRaw, "SECRET-BODY") || strings.Contains(out.ResponseRaw, "RESPONSE-BODY") {
		t.Fatal("原始报文不得包含 body")
	}
	if out.Body != "RESPONSE-BODY" {
		t.Fatalf("旁路录制仍应正常保存 body: %q", out.Body)
	}
}

func TestStaticAndSSEMetadataOnly(t *testing.T) {
	for _, tc := range []struct {
		name, ct, url, omit string
	}{
		{"static", "image/png", "http://x/logo.png", "static_resource"},
		{"sse", "text/event-stream", "http://x/events", "sse"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "t.jsonl")
			r, err := New(path, "s1", 4096, true)
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodGet, tc.url, nil)
			rec := r.Begin(req)
			resp := &http.Response{
				StatusCode: 200, Status: "200 OK",
				Header:  http.Header{"Content-Type": []string{tc.ct}},
				Body:    io.NopCloser(strings.NewReader("SHOULD-NOT-BE-STORED")),
				Request: req,
			}
			r.Attach(rec, resp)
			_ = resp.Body.Close()
			if err := r.Close(); err != nil {
				t.Fatal(err)
			}
			out := readOne(t, path)
			if out.BodyOmitted != tc.omit || out.Body != "" {
				t.Fatalf("%s: omit=%q body=%q", tc.name, out.BodyOmitted, out.Body)
			}
		})
	}
}

// LastStatusFor 必须 O(1)（write 时维护 map）且返回"最近一次"状态，
// 尾斜杠归一化与旧遍历实现语义一致。
func TestLastStatusForLatestWins(t *testing.T) {
	dir := t.TempDir()
	r, err := New(filepath.Join(dir, "t.jsonl"), "s1", 1024, true)
	if err != nil {
		t.Fatal(err)
	}
	write := func(url string, status int) {
		r.write(&Record{URL: url, Status: status})
	}
	write("http://t.test/flaky", 503)
	write("http://t.test/other", 200)
	write("http://t.test/flaky", 200)  // 同 URL 第二次录制覆盖
	write("http://t.test/flaky/", 503) // 尾斜杠变体同属一个 key

	if got := r.LastStatusFor("http://t.test/flaky"); got != 503 {
		t.Fatalf("LastStatusFor = %d, want 503 (最近一次)", got)
	}
	if got := r.LastStatusFor("http://t.test/flaky/"); got != 503 {
		t.Fatalf("带尾斜杠查询 = %d, want 503", got)
	}
	if got := r.LastStatusFor("http://t.test/other/"); got != 200 {
		t.Fatalf("其他 URL = %d, want 200", got)
	}
	if got := r.LastStatusFor("http://t.test/missing"); got != 0 {
		t.Fatalf("无记录应返回 0, got %d", got)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
}

// 双层生效：SetScopeFilter 后 write 标记 InScope，ExportRequests 过滤域外，
// traffic.jsonl 仍全量落盘（录制不丢数据）。
func TestScopeFilterMarksAndFiltersExports(t *testing.T) {
	dir := t.TempDir()
	r, err := New(filepath.Join(dir, "t.jsonl"), "s1", 4096, true)
	if err != nil {
		t.Fatal(err)
	}
	r.SetScopeFilter(func(u string) bool { return strings.Contains(u, "in-scope.test") })
	write := func(url string, status int) {
		r.write(&Record{URL: url, Status: status})
	}
	write("http://in-scope.test/page", 200)
	write("https://www.google-analytics.com/collect", 204)
	write("http://in-scope.test/api", 200)

	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	rows, err := os.ReadFile(filepath.Join(dir, "t.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var recs []Record
	for _, line := range strings.Split(strings.TrimSpace(string(rows)), "\n") {
		var rec Record
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatal(err)
		}
		recs = append(recs, rec)
	}
	if len(recs) != 3 {
		t.Fatalf("录制层必须全量: got %d", len(recs))
	}
	if recs[0].InScope == nil || !*recs[0].InScope || recs[1].InScope == nil || *recs[1].InScope {
		t.Fatalf("InScope 标记错误: %+v %+v", recs[0].InScope, recs[1].InScope)
	}
	// 请求包导出只留域内
	if err := r.ExportRequests(filepath.Join(dir, "req.txt")); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "req.txt"))
	if got := strings.Count(string(data), "HTTP/1.1"); got != 2 {
		t.Fatalf("请求包应只含域内 2 条, got %d", got)
	}
	if strings.Contains(string(data), "google-analytics") {
		t.Fatal("域外请求泄漏进请求包")
	}
}
