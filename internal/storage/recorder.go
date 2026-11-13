// Package storage 只读流量录制：JSONL 输出、body 限额、静态资源/长连接过滤、HAR 导出。
package storage

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

type Record struct {
	SessionID          string      `json:"session_id"`
	TaskID             string      `json:"task_id"`
	Seq                int64       `json:"seq"`
	StartedAt          string      `json:"started_at"`
	DurationMS         int64       `json:"duration_ms"`
	Method             string      `json:"method"`
	URL                string      `json:"url"`
	Host               string      `json:"host"`
	Scheme             string      `json:"scheme"`
	RequestHeaders     http.Header `json:"request_headers"`
	RequestBody        string      `json:"request_body,omitempty"`
	RequestBodyEnc     string      `json:"request_body_encoding,omitempty"`
	RequestBodyOmitted string      `json:"request_body_omitted,omitempty"`
	Status             int         `json:"status"`
	StatusText         string      `json:"status_text,omitempty"`
	RespHeaders        http.Header `json:"response_headers"`
	ContentType        string      `json:"content_type"`
	ResourceType       string      `json:"resource_type"`
	BodySize           int64       `json:"body_size"`
	Body               string      `json:"body,omitempty"`
	BodyEncoding       string      `json:"body_encoding,omitempty"`
	BodyOmitted        string      `json:"body_omitted,omitempty"`
	Upgrade            string      `json:"upgrade,omitempty"`
	RequestRaw         string      `json:"request_raw,omitempty"`
	ResponseRaw        string      `json:"response_raw,omitempty"`
	Error              string      `json:"error,omitempty"`
	// InScope：导航作用域判定标记（双层生效——录制层不丢数据，仅标记）。
	// nil = 未启用作用域过滤；true/false = 域内/域外。
	InScope *bool `json:"in_scope,omitempty"`
	// WS：WebSocket 专属字段（帧捕获经 CDP，见 ws.go）；nil = 非 WS 记录。
	WS      *WSInfo   `json:"ws,omitempty"`
	started time.Time `json:"-"`
}

type JSBody struct {
	URL  string
	Body []byte
}

type Recorder struct {
	// saveJS: 开启后原始 .js 响应落盘到 saveJSDir（文件名 = URL SHA-256 前 16 位），
	// 供人工溯源分析；默认关闭。
	saveJS    bool
	saveJSDir string

	mu         sync.Mutex
	file       *os.File
	enc        *json.Encoder
	sessionID  string
	taskID     string
	maxBody    int64
	skipStatic bool
	rawDump    bool
	seq        int64
	records    []Record
	// lastStatus: URL（去尾斜杠）→ 最近一次录制的状态码，供爬虫 429/503
	// 重试判定 O(1) 查询。独立于 records 内存上限（长任务 records 停止
	// 追加后，map 仍持续更新，语义始终是"最近一次"）。
	lastStatus map[string]int
	// scopeFilter: URL 级作用域判定（由上层注入 crawl.ScopeFromCfg 的包装），
	// write 时标记 InScope，导出（requests/requests-filtered/report）默认过滤域外。
	scopeFilter func(string) bool
	jsMu        sync.Mutex
	jsBodies    []JSBody
	written     int64
	maxInMemory int
	memBytes    int64
	maxMemBytes int64
}

func New(outPath, sessionID string, maxBody int64, skipStatic bool) (*Recorder, error) {
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return nil, err
	}
	f, err := os.Create(outPath)
	if err != nil {
		return nil, err
	}
	return &Recorder{
		file: f, enc: json.NewEncoder(f), sessionID: sessionID,
		maxBody: maxBody, skipStatic: skipStatic, maxInMemory: 20000,
		// HAR 导出依赖内存快照，按字节兜底避免长任务因大量 2MB body 而 OOM。
		maxMemBytes: 64 * 1024 * 1024,
		lastStatus:  map[string]int{},
	}, nil
}

// SetSaveJS 开启/配置 JS 源码落盘（默认关闭）：原始 .js 响应写入 saveJSDir，
// 文件名 = URL SHA-256 前 16 位 hex，供人工溯源分析。
func (r *Recorder) SetSaveJS(enabled bool, dir string) {
	r.mu.Lock()
	r.saveJS = enabled
	r.saveJSDir = dir
	r.mu.Unlock()
}

// saveJSSource 将 JS 响应体落盘；失败静默（不影响录制主流程）。
func (r *Recorder) saveJSSource(rec *Record) {
	if r.saveJSDir == "" {
		return
	}
	sum := sha256.Sum256([]byte(rec.URL))
	name := hex.EncodeToString(sum[:8]) + ".js"
	path := filepath.Join(r.saveJSDir, name)
	if err := os.MkdirAll(r.saveJSDir, 0o755); err != nil {
		return
	}
	_ = os.WriteFile(path, []byte(rec.Body), 0o644)
}

// SetRawDump 开启后旁路附带原始请求/响应「头部」报文（不含 body），便于重放/人工排障。
// 硬约束：只序列化内存中的 header，绝不读取或改写 req.Body / resp.Body。
func (r *Recorder) SetRawDump(on bool) { r.rawDump = on }

// SetScopeFilter 注入 URL 级作用域判定：write 时标记 InScope，
// ExportRequests / ExportRequestsFiltered / ExportReport 默认过滤域外记录。
func (r *Recorder) SetScopeFilter(fn func(string) bool) {
	r.mu.Lock()
	r.scopeFilter = fn
	r.mu.Unlock()
}

// inScope 判定单条记录是否在作用域内（未启用过滤视为域内）。
func (r *Recorder) inScope(rec Record) bool {
	if r.scopeFilter == nil {
		return true
	}
	return r.scopeFilter(rec.URL)
}

// RecordConnect 记录 CONNECT 隧道元信息。CONNECT 在 goproxy 中走 handleHttps，
// 不经过 OnRequest/OnResponse 回调，因此由 HandleConnect 显式调用，避免 passthrough
// 流量变成「完全无记录」。
func (r *Recorder) RecordConnect(host string) {
	rec := &Record{
		SessionID: r.sessionID, TaskID: r.task(), started: time.Now(),
		StartedAt: time.Now().Format(time.RFC3339Nano),
		Method:    http.MethodConnect, URL: host, Host: host, Scheme: "https",
		ResourceType: "connect", BodyOmitted: "tls_passthrough",
		StatusText: "CONNECT tunnel (not decrypted)",
	}
	r.write(rec)
}

func (r *Recorder) SetTaskID(id string) {
	r.mu.Lock()
	r.taskID = id
	r.mu.Unlock()
}

func (r *Recorder) task() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.taskID
}

// Begin 在 goproxy OnRequest 回调里调用，创建一条待完成记录。
func (r *Recorder) Begin(req *http.Request) *Record {
	rec := &Record{
		SessionID: r.sessionID, TaskID: r.task(), started: time.Now(),
		StartedAt: time.Now().Format(time.RFC3339Nano),
		Method:    req.Method, URL: cleanURL(req), Host: req.Host,
		Scheme: req.URL.Scheme, RequestHeaders: req.Header.Clone(),
	}
	if rec.Host == "" {
		rec.Host = req.URL.Host
	}
	// 请求体只读录制：仅在长度已知且 <= 64KB 时完整读取并原样放回
	// （读取量 == ContentLength，绝不截断、不改 req.ContentLength）。
	// 大请求体 / chunked 一律不触碰 req.Body，避免破坏上游传输或篡改请求内容。
	const reqBodyLimit int64 = 64 * 1024
	if req.Body != nil {
		switch {
		case req.ContentLength > 0 && req.ContentLength <= reqBodyLimit:
			data, readErr := io.ReadAll(io.LimitReader(req.Body, req.ContentLength))
			_ = req.Body.Close()
			req.Body = io.NopCloser(bytes.NewReader(data))
			if readErr == nil && int64(len(data)) == req.ContentLength {
				rec.RequestBody, rec.RequestBodyEnc = printableEncoded(data, reqBodyLimit)
			} else {
				rec.RequestBodyOmitted = "read_error"
			}
		case req.ContentLength > reqBodyLimit:
			rec.RequestBodyOmitted = "too_large"
		case req.ContentLength < 0:
			rec.RequestBodyOmitted = "chunked"
		}
	}
	if r.rawDump {
		rec.RequestRaw = dumpHead(req.Method+" "+cleanURL(req)+" HTTP/1.1", req.Header)
	}
	return rec
}

// dumpHead 只序列化内存中的起始行与 header，不触碰任何 body（只读硬约束）。
func dumpHead(startLine string, h http.Header) string {
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(startLine)
	b.WriteString("\r\n")
	for _, k := range keys {
		for _, v := range h[k] {
			fmt.Fprintf(&b, "%s: %s\r\n", k, v)
		}
	}
	b.WriteString("\r\n")
	return b.String()
}

// Attach 在 goproxy OnResponse 回调里调用。
// 对 WebSocket/SSE 等长连接只记元信息且不触碰 body；其余以旁路 Tee 包装，
// 保证真实响应流完整无损地送达浏览器。
func (r *Recorder) Attach(rec *Record, resp *http.Response) {
	if rec == nil {
		return
	}
	rec.Status = resp.StatusCode
	rec.StatusText = resp.Status
	rec.RespHeaders = resp.Header.Clone()
	rec.ContentType = resp.Header.Get("Content-Type")
	ct := strings.ToLower(rec.ContentType)
	rec.ResourceType = classify(rec.URL, ct)
	upgrade := resp.Header.Get("Upgrade")
	if upgrade == "" && resp.Request != nil {
		upgrade = resp.Request.Header.Get("Upgrade")
	}
	rec.Upgrade = upgrade
	rec.DurationMS = time.Since(rec.started).Milliseconds()
	if r.rawDump {
		rec.ResponseRaw = dumpHead("HTTP/1.1 "+resp.Status, resp.Header)
	}

	if resp.StatusCode == http.StatusSwitchingProtocols ||
		strings.Contains(strings.ToLower(upgrade), "websocket") {
		rec.BodyOmitted = "websocket"
		r.write(rec)
		return
	}
	if strings.Contains(ct, "text/event-stream") {
		rec.BodyOmitted = "sse"
		r.write(rec)
		return
	}
	if r.skipStatic && isStatic(rec.ResourceType) {
		rec.BodyOmitted = "static_resource"
		if resp.ContentLength > 0 {
			rec.BodySize = resp.ContentLength
		}
		r.write(rec)
		return
	}
	if resp.Body == nil {
		rec.BodySize = 0
		r.write(rec)
		return
	}
	resp.Body = &teeBody{
		ReadCloser: resp.Body, rec: rec, r: r,
		buf: bytes.NewBuffer(make([]byte, 0, int(min(r.maxBody, 64*1024)))),
	}
}

func (r *Recorder) finalize(rec *Record, buf []byte, total int64, truncated bool) {
	rec.BodySize = total
	rec.DurationMS = time.Since(rec.started).Milliseconds()
	data := buf
	if strings.Contains(strings.ToLower(rec.RespHeaders.Get("Content-Encoding")), "gzip") {
		if zr, err := gzip.NewReader(bytes.NewReader(data)); err == nil {
			if plain, err := io.ReadAll(io.LimitReader(zr, r.maxBody+1)); err == nil {
				data = plain
			}
			_ = zr.Close()
		}
	}
	ct := strings.ToLower(rec.ContentType)
	switch {
	case total > r.maxBody:
		// 设计文档 §6：超限只记录元信息（status/header/url/size），丢弃 body。
		rec.BodyOmitted = "too_large"
	case !isTextual(ct, data):
		rec.BodyOmitted = "binary"
	case truncated:
		rec.BodyOmitted = "stream_closed"
		rec.Body, rec.BodyEncoding = printableEncoded(data, r.maxBody)
	default:
		rec.Body, rec.BodyEncoding = printableEncoded(data, r.maxBody)
	}
	if strings.Contains(ct, "javascript") && len(data) > 0 && rec.BodyOmitted == "" {
		r.jsMu.Lock()
		r.jsBodies = append(r.jsBodies, JSBody{URL: rec.URL, Body: append([]byte(nil), data...)})
		r.jsMu.Unlock()
	}
	r.write(rec)
}

func (r *Recorder) write(rec *Record) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if rec.Seq != 0 { // 已写过
		return
	}
	r.seq++
	rec.Seq = r.seq
	if r.scopeFilter != nil {
		b := r.scopeFilter(rec.URL)
		rec.InScope = &b
	}
	if err := r.enc.Encode(rec); err == nil {
		r.written++
	}
	r.lastStatus[strings.TrimSuffix(rec.URL, "/")] = rec.Status
	size := int64(len(rec.Body) + len(rec.RequestBody) + len(rec.URL) + 512)
	if len(r.records) < r.maxInMemory && r.memBytes+size <= r.maxMemBytes {
		r.records = append(r.records, *rec)
		r.memBytes += size
	}
	if r.saveJS && rec.ResourceType == "script" && rec.Body != "" {
		r.saveJSSource(rec)
	}
}

// TakeJS 取出录制过程中收集到的 JS 响应体（供 JS 接口提取）。
func (r *Recorder) TakeJS() []JSBody {
	r.jsMu.Lock()
	defer r.jsMu.Unlock()
	out := r.jsBodies
	r.jsBodies = nil
	return out
}

// LastStatusFor 返回某 URL 最近一次录制的状态码；无记录返回 0。
// 用于爬虫在导航成功后识别 429/503 并触发退避重试。O(1) 查询（write 时
// 维护 lastStatus map），不依赖 records 内存快照是否仍在追加。
func (r *Recorder) LastStatusFor(targetURL string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if st, ok := r.lastStatus[strings.TrimSuffix(targetURL, "/")]; ok {
		return st
	}
	return r.lastStatus[targetURL]
}

// ExportOutScope 导出作用域外流量清单（第三方资产：CDN/埋点/统计）。
// 仅在启用 scope 过滤（SetScopeFilter）且有域外记录时产出；未启用过滤时
// 输出空文件（语义：全部域内）。每行：URL<TAB>状态码。
func (r *Recorder) ExportOutScope(path string) error {
	r.mu.Lock()
	var lines []string
	for _, rec := range r.records {
		if rec.InScope != nil && !*rec.InScope {
			lines = append(lines, fmt.Sprintf("%s\t%d", rec.URL, rec.Status))
		}
	}
	r.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
}

func (r *Recorder) Stats() (records int64, inMemory int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.written, len(r.records)
}

func (r *Recorder) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.file.Close()
}

// teeBody 旁路复制响应流，不改变任何字节。
type teeBody struct {
	io.ReadCloser
	rec       *Record
	r         *Recorder
	buf       *bytes.Buffer
	total     int64
	truncated bool
	done      bool
}

func (b *teeBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.total += int64(n)
		room := b.r.maxBody - int64(b.buf.Len())
		if room > 0 {
			take := min(int64(n), room)
			_, _ = b.buf.Write(p[:take])
		}
	}
	if err == io.EOF || err != nil {
		b.finish()
	}
	return n, err
}

func (b *teeBody) Close() error {
	if !b.done {
		b.truncated = b.total > 0
	}
	b.finish()
	return b.ReadCloser.Close()
}

func (b *teeBody) finish() {
	if b.done {
		return
	}
	b.done = true
	b.r.finalize(b.rec, b.buf.Bytes(), b.total, b.truncated)
}

// cleanURL 仅规范化展示用 URL（goproxy MITM 会重写成 https://host:443/path），
// 不修改 req.URL 本身，保证只读。
func cleanURL(req *http.Request) string {
	u := *req.URL
	u.Fragment = ""
	if (u.Scheme == "https" && strings.HasSuffix(u.Host, ":443")) ||
		(u.Scheme == "http" && strings.HasSuffix(u.Host, ":80")) {
		u.Host = u.Host[:strings.LastIndex(u.Host, ":")]
	}
	return u.String()
}

// printableEncoded 返回可直接放进 JSON 的文本；非 UTF-8 内容转 base64 并标注编码，
// 避免下游把二进制误当文本解析。
func printableEncoded(data []byte, limit int64) (string, string) {
	if int64(len(data)) > limit {
		data = data[:limit]
	}
	if utf8.Valid(data) {
		return string(data), ""
	}
	return base64.StdEncoding.EncodeToString(data), "base64"
}

func isTextual(ct string, data []byte) bool {
	if strings.HasPrefix(ct, "text/") || strings.Contains(ct, "json") ||
		strings.Contains(ct, "javascript") || strings.Contains(ct, "xml") ||
		strings.Contains(ct, "x-www-form-urlencoded") || strings.Contains(ct, "graphql") {
		return true
	}
	return utf8.Valid(data) && len(data) > 0
}

func classify(rawURL, ct string) string {
	path := strings.ToLower(strings.SplitN(rawURL, "?", 2)[0])
	switch {
	case strings.Contains(ct, "javascript"):
		return "script"
	case strings.Contains(ct, "text/css") || strings.HasSuffix(path, ".css"):
		return "stylesheet"
	case strings.HasPrefix(ct, "image/") || hasAny(path, ".png", ".jpg", ".jpeg", ".gif", ".webp", ".svg", ".ico"):
		return "image"
	case strings.HasPrefix(ct, "font/") || hasAny(path, ".woff", ".woff2", ".ttf", ".otf"):
		return "font"
	case strings.HasPrefix(ct, "video/") || strings.HasPrefix(ct, "audio/"):
		return "media"
	case strings.Contains(ct, "json"):
		return "xhr"
	case strings.Contains(ct, "text/html"):
		return "document"
	default:
		return "other"
	}
}

func isStatic(t string) bool {
	return t == "image" || t == "font" || t == "media" || t == "stylesheet"
}

func hasAny(s string, subs ...string) bool {
	for _, x := range subs {
		if strings.HasSuffix(s, x) {
			return true
		}
	}
	return false
}

func (r *Recorder) String() string {
	n, _ := r.Stats()
	return fmt.Sprintf("records=%d", n)
}
