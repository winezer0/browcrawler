package storage

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ExportRequests 从内存快照重建「可重放的原始 HTTP 请求包」文本文件。
//
// 这是安全检查的主交付物之一：每条记录输出为标准 HTTP 报文
// （请求行使用绝对 URI，可直接粘贴进 Burp Repeater / Intruder 或 curl --request-target），
// 各请求之间用分隔线隔开。数据源与 JSONL 完全一致，不触碰任何响应流。
func (r *Recorder) ExportRequests(path string) error {
	r.mu.Lock()
	records := append([]Record(nil), r.records...)
	r.mu.Unlock()

	var b strings.Builder
	for _, rec := range records {
		if rec.ResourceType == "websocket" {
			continue // WS 是有状态会话协议，不走 HTTP 重放（见 ws.go）
		}
		if !r.inScope(rec) {
			continue // 双层生效：交付包默认只留域内（JSONL 全量+InScope 标记）
		}
		if rec.Method == "CONNECT" {
			continue // 隧道未解密，无可重放的内层请求
		}
		b.WriteString(rebuildRequest(rec))
		b.WriteString("\n\n")
		b.WriteString(strings.Repeat("=", 72))
		b.WriteString("\n\n")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// rebuildRequest 还原单条原始 HTTP 请求。
func rebuildRequest(rec Record) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s HTTP/1.1\r\n", rec.Method, rec.URL)

	headers := rec.RequestHeaders.Clone()
	if headers.Get("Host") == "" && rec.Host != "" {
		headers.Set("Host", rec.Host)
	}
	body := rec.RequestBody
	if rec.RequestBodyEnc == "base64" {
		if raw, err := base64.StdEncoding.DecodeString(rec.RequestBody); err == nil {
			body = string(raw)
		}
	}
	if body != "" && headers.Get("Content-Length") == "" {
		headers.Set("Content-Length", fmt.Sprintf("%d", len(body)))
	}
	if body != "" && headers.Get("Content-Type") == "" {
		// POST 表单最常见形态；无值时不猜测 application/json 等其他类型。
		headers.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for _, k := range sortedKeys(headers) {
		for _, v := range headers[k] {
			fmt.Fprintf(&b, "%s: %s\r\n", k, v)
		}
	}
	b.WriteString("\r\n")
	if body != "" {
		b.WriteString(body)
		b.WriteString("\r\n")
	}
	return b.String()
}

func sortedKeys(h map[string][]string) []string {
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
