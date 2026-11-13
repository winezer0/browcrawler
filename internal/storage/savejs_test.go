package storage

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveJSWritesScriptBody(t *testing.T) {
	dir := t.TempDir()
	r, err := New(filepath.Join(dir, "t.jsonl"), "s1", 4096, true)
	if err != nil {
		t.Fatal(err)
	}
	r.SetSaveJS(true, filepath.Join(dir, "js"))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		_, _ = w.Write([]byte("var ENDPOINTS = {user:'/api'};"))
	}))
	defer srv.Close()
	req := httptest.NewRequest(http.MethodGet, srv.URL+"/app.js", nil)
	rec := r.Begin(req)
	resp := &http.Response{
		StatusCode: 200, Status: "200 OK",
		Header: http.Header{"Content-Type": []string{"application/javascript"}},
		Body:   io.NopCloser(strings.NewReader("var ENDPOINTS = {user:'/api'};")), Request: req,
	}
	r.Attach(rec, resp)
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "js"))
	if len(entries) != 1 {
		t.Fatalf("JS 落盘文件数 = %d, want 1", len(entries))
	}
	data, _ := os.ReadFile(filepath.Join(dir, "js", entries[0].Name()))
	if !strings.Contains(string(data), "ENDPOINTS") {
		t.Fatal("落盘内容不含 JS 体")
	}
}
