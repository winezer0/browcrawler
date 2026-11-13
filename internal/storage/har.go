package storage

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// HAR 1.2 导出。独立于录制逻辑，便于补充字段而不触碰「只读」主链路。
// 说明：timings 只有总耗时（goproxy 旁路录制不拦截各阶段），因此把总时长记入 wait，
// send/receive 置 0，避免伪造无法观测的数据。
type harNameValue struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type harCookie struct {
	Name     string `json:"name"`
	Value    string `json:"value"`
	Path     string `json:"path,omitempty"`
	Domain   string `json:"domain,omitempty"`
	Expires  string `json:"expires,omitempty"`
	HTTPOnly bool   `json:"httpOnly,omitempty"`
	Secure   bool   `json:"secure,omitempty"`
}

type harContent struct {
	Size     int64  `json:"size"`
	MimeType string `json:"mimeType"`
	Text     string `json:"text,omitempty"`
	Encoding string `json:"encoding,omitempty"`
}

type harRequest struct {
	Method      string         `json:"method"`
	URL         string         `json:"url"`
	HTTPVersion string         `json:"httpVersion"`
	Cookies     []harCookie    `json:"cookies"`
	Headers     []harNameValue `json:"headers"`
	QueryString []harNameValue `json:"queryString"`
	PostData    string         `json:"postData,omitempty"`
	HeadersSize int64          `json:"headersSize"`
	BodySize    int64          `json:"bodySize"`
}

type harResponse struct {
	Status      int64          `json:"status"`
	StatusText  string         `json:"statusText"`
	HTTPVersion string         `json:"httpVersion"`
	Cookies     []harCookie    `json:"cookies"`
	Headers     []harNameValue `json:"headers"`
	Content     harContent     `json:"content"`
	RedirectURL string         `json:"redirectURL"`
	HeadersSize int64          `json:"headersSize"`
	BodySize    int64          `json:"bodySize"`
}

type harTimings struct {
	Send    int64 `json:"send"`
	Wait    int64 `json:"wait"`
	Receive int64 `json:"receive"`
}

type harEntry struct {
	StartedDateTime string      `json:"startedDateTime"`
	Time            int64       `json:"time"`
	Request         harRequest  `json:"request"`
	Response        harResponse `json:"response"`
	Cache           struct{}    `json:"cache"`
	Timings         harTimings  `json:"timings"`
	PageRef         string      `json:"pageref,omitempty"`
}

// ExportHAR 导出 HAR 1.2（含 cookies / queryString / timings / 原始报文引用）。
func (r *Recorder) ExportHAR(path string) error {
	r.mu.Lock()
	records := append([]Record(nil), r.records...)
	r.mu.Unlock()
	entries := make([]harEntry, 0, len(records))
	for _, rec := range records {
		e := harEntry{StartedDateTime: rec.StartedAt, Time: rec.DurationMS}
		e.Timings.Wait = rec.DurationMS

		e.Request.Method = rec.Method
		e.Request.URL = rec.URL
		e.Request.HTTPVersion = "HTTP/1.1"
		e.Request.Cookies = parseRequestCookies(rec.RequestHeaders.Get("Cookie"))
		e.Request.Headers = flattenHeaders(rec.RequestHeaders)
		e.Request.QueryString = parseQuery(rec.URL)
		e.Request.PostData = rec.RequestBody
		e.Request.HeadersSize = estimateHeaderSize(rec.RequestHeaders)
		e.Request.BodySize = int64(len(rec.RequestBody))

		e.Response.Status = int64(rec.Status)
		e.Response.StatusText = rec.StatusText
		e.Response.HTTPVersion = "HTTP/1.1"
		e.Response.Cookies = parseSetCookies(rec.RespHeaders.Values("Set-Cookie"))
		e.Response.Headers = flattenHeaders(rec.RespHeaders)
		e.Response.Content = harContent{
			Size: rec.BodySize, MimeType: rec.ContentType, Text: rec.Body,
			Encoding: rec.BodyEncoding,
		}
		e.Response.RedirectURL = rec.RespHeaders.Get("Location")
		e.Response.HeadersSize = estimateHeaderSize(rec.RespHeaders)
		e.Response.BodySize = rec.BodySize
		entries = append(entries, e)
	}
	doc := map[string]any{"log": map[string]any{
		"version": "1.2",
		"creator": map[string]string{"name": "browcrawler", "version": "0.2"},
		"entries": entries,
	}}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

func flattenHeaders(h http.Header) []harNameValue {
	out := make([]harNameValue, 0, len(h))
	for k, vs := range h {
		for _, v := range vs {
			out = append(out, harNameValue{Name: k, Value: v})
		}
	}
	return out
}

func estimateHeaderSize(h http.Header) int64 {
	n := int64(0)
	for k, vs := range h {
		for _, v := range vs {
			n += int64(len(k) + len(v) + 4)
		}
	}
	return n
}

func parseQuery(raw string) []harNameValue {
	u, err := url.Parse(raw)
	if err != nil {
		return []harNameValue{}
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return []harNameValue{}
	}
	out := make([]harNameValue, 0, len(q))
	for k, vs := range q {
		for _, v := range vs {
			out = append(out, harNameValue{Name: k, Value: v})
		}
	}
	return out
}

func parseRequestCookies(header string) []harCookie {
	if header == "" {
		return []harCookie{}
	}
	out := []harCookie{}
	for _, pair := range strings.Split(header, ";") {
		name, value, ok := strings.Cut(strings.TrimSpace(pair), "=")
		if !ok || name == "" {
			continue
		}
		out = append(out, harCookie{Name: strings.TrimSpace(name), Value: strings.TrimSpace(value)})
	}
	return out
}

func parseSetCookies(values []string) []harCookie {
	out := []harCookie{}
	for _, raw := range values {
		parts := strings.Split(raw, ";")
		name, value, ok := strings.Cut(strings.TrimSpace(parts[0]), "=")
		if !ok || name == "" {
			continue
		}
		c := harCookie{Name: strings.TrimSpace(name), Value: strings.TrimSpace(value)}
		for _, attr := range parts[1:] {
			k, v, _ := strings.Cut(strings.TrimSpace(attr), "=")
			switch strings.ToLower(strings.TrimSpace(k)) {
			case "path":
				c.Path = strings.TrimSpace(v)
			case "domain":
				c.Domain = strings.TrimSpace(v)
			case "expires":
				c.Expires = strings.TrimSpace(v)
			case "httponly":
				c.HTTPOnly = true
			case "secure":
				c.Secure = true
			}
		}
		out = append(out, c)
	}
	return out
}
