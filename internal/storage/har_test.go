package storage

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExportHARIncludesCookiesQueryAndTimings(t *testing.T) {
	dir := t.TempDir()
	traffic := filepath.Join(dir, "t.jsonl")
	r, err := New(traffic, "s1", 4096, false)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "http://x.test/api?id=7&tag=a", nil)
	req.Header.Set("Cookie", "sid=abc; theme=dark")
	rec := r.Begin(req)
	resp := &http.Response{
		StatusCode: 200, Status: "200 OK",
		Header: http.Header{
			"Content-Type": []string{"application/json"},
			"Set-Cookie":   []string{"token=xyz; Path=/; HttpOnly; Secure"},
			"Location":     []string{"http://x.test/next"},
		},
		Body: io.NopCloser(strings.NewReader(`{"ok":true}`)), Request: req,
	}
	r.Attach(rec, resp)
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	harPath := filepath.Join(dir, "t.har")
	if err := r.ExportHAR(harPath); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(harPath)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Log struct {
			Version string `json:"version"`
			Entries []struct {
				Time    int64 `json:"time"`
				Request struct {
					Cookies     []harCookie    `json:"cookies"`
					QueryString []harNameValue `json:"queryString"`
					Headers     []harNameValue `json:"headers"`
				} `json:"request"`
				Response struct {
					Cookies     []harCookie `json:"cookies"`
					RedirectURL string      `json:"redirectURL"`
					Content     harContent  `json:"content"`
				} `json:"response"`
				Timings harTimings `json:"timings"`
			} `json:"entries"`
		} `json:"log"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Log.Version != "1.2" || len(doc.Log.Entries) != 1 {
		t.Fatalf("HAR 结构异常: version=%s entries=%d", doc.Log.Version, len(doc.Log.Entries))
	}
	e := doc.Log.Entries[0]
	if len(e.Request.Cookies) != 2 || e.Request.Cookies[0].Name != "sid" {
		t.Fatalf("请求 cookie 解析失败: %+v", e.Request.Cookies)
	}
	if len(e.Response.Cookies) != 1 || !e.Response.Cookies[0].HTTPOnly || !e.Response.Cookies[0].Secure ||
		e.Response.Cookies[0].Path != "/" {
		t.Fatalf("Set-Cookie 属性解析失败: %+v", e.Response.Cookies)
	}
	if len(e.Request.QueryString) != 2 {
		t.Fatalf("queryString 解析失败: %+v", e.Request.QueryString)
	}
	if e.Response.RedirectURL != "http://x.test/next" || e.Response.Content.Text != `{"ok":true}` {
		t.Fatalf("响应字段不完整: %+v", e.Response)
	}
	if e.Timings.Wait != e.Time || e.Timings.Send != 0 || e.Timings.Receive != 0 {
		t.Fatalf("timings 应只填可观测的 wait: %+v / time=%d", e.Timings, e.Time)
	}
}
