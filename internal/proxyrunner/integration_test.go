package proxyrunner

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/winezer0/browcrawler/internal/storage"
)

// startProxy 起一个真实 goproxy 实例（不依赖 Obscura），用于验证录制链路集成行为。
func startProxy(t *testing.T) (*Runner, *storage.Recorder, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "traffic.jsonl")
	rec, err := storage.New(path, "it", 4096, true)
	if err != nil {
		t.Fatal(err)
	}
	p := New(tls.Certificate{}, rec, "", log.New(io.Discard, "", 0), nil)
	if err := p.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = p.Shutdown(ctx)
		_ = rec.Close()
	})
	return p, rec, path
}

func recordsAt(t *testing.T, path string) []storage.Record {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []storage.Record
	dec := json.NewDecoder(bytes.NewReader(b))
	for {
		var r storage.Record
		if err := dec.Decode(&r); err == io.EOF {
			break
		} else if err != nil {
			t.Fatalf("解析 JSONL: %v", err)
		}
		out = append(out, r)
	}
	return out
}

func TestSSEThroughProxyRecordsMetadataOnly(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: tick-1\n\ndata: tick-2\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	defer upstream.Close()

	p, _, path := startProxy(t)
	proxyURL, _ := url.Parse(p.ProxyAddr())
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}, Timeout: 5 * time.Second}
	resp, err := client.Get(upstream.URL + "/sse")
	if err != nil {
		t.Fatalf("经代理请求 SSE 失败: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if string(body) != "data: tick-1\n\ndata: tick-2\n\n" {
		t.Fatalf("真实 SSE 流被改写: %q", body)
	}
	rows := recordsAt(t, path)
	if len(rows) != 1 {
		t.Fatalf("期望 1 条记录，实际 %d", len(rows))
	}
	if rows[0].BodyOmitted != "sse" || rows[0].Body != "" {
		t.Fatalf("SSE 应只记元信息: omit=%q body=%q", rows[0].BodyOmitted, rows[0].Body)
	}
	if rows[0].Status != 200 || rows[0].ContentType != "text/event-stream" {
		t.Fatalf("SSE 元信息不完整: %+v", rows[0])
	}
}

func TestUpgrade101ThroughProxyRecordsMetadataOnly(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("源站不支持 hijack")
		}
		conn, buf, err := hj.Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = buf.WriteString("HTTP/1.1 101 Switching Protocols\r\n" +
			"Upgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
		_ = buf.Flush()
	}))
	defer upstream.Close()

	p, _, path := startProxy(t)
	proxyURL, _ := url.Parse(p.ProxyAddr())
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}, Timeout: 5 * time.Second}
	req, _ := http.NewRequest(http.MethodGet, upstream.URL+"/ws", nil)
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "Upgrade")
	resp, err := client.Do(req)
	if err != nil {
		// goproxy v1.7.2 的 http 分支不保证能透传 101；此时仍检查是否已记元信息。
		t.Logf("101 经代理透传不可用（goproxy 限制）: %v", err)
	}
	if resp != nil {
		if resp.StatusCode != http.StatusSwitchingProtocols {
			t.Logf("代理返回状态码 %d（非 101）", resp.StatusCode)
		}
		_ = resp.Body.Close()
	}
	rows := recordsAt(t, path)
	if len(rows) == 0 {
		t.Fatal("即使透传失败，也应至少记录一条请求元信息")
	}
	if rows[0].BodyOmitted != "websocket" {
		t.Fatalf("101/Upgrade 应标记 websocket，实际 omit=%q status=%d upgrade=%q",
			rows[0].BodyOmitted, rows[0].Status, rows[0].Upgrade)
	}
	if rows[0].Body != "" {
		t.Fatal("101 不应保存 body")
	}
}
