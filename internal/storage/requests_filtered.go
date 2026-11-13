package storage

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/winezer0/requestfilter"
)

// ExportRequestsFiltered 是 -requests-filtered 的实现：先用 requestfilter 的
// CoverageParameters 批量过滤（方法+主机+路径+参数形状值不敏感、认证组按
// Cookie 自动隔离），再导出精简请求包。
//
// 与全量 ExportRequests 的关系：全量包保留所有原始样本；精简包每个
// 「端点+参数形状」只保留一条（扫描器只需按形状测试），两者互补交付。
// CONNECT 隧道与过滤后未放行的请求不出现在精简包中。
func (r *Recorder) ExportRequestsFiltered(path string) error {
	r.mu.Lock()
	records := append([]Record(nil), r.records...)
	r.mu.Unlock()

	// 双层生效：精简包先剔除域外（JSONL 全量+InScope 标记），再形状收敛
	inScopeRecords := make([]Record, 0, len(records))
	for _, rec := range records {
		if rec.ResourceType == "websocket" {
			continue // WS 不参与 HTTP 形状收敛
		}
		if r.inScope(rec) {
			inScopeRecords = append(inScopeRecords, rec)
		}
	}
	filtered, err := filterRecords(inScopeRecords)
	if err != nil {
		return err
	}

	var b strings.Builder
	for _, rec := range filtered {
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

// filterRecords 用 FilterBatch 按形状过滤录制请求；认证组由包内建逻辑
// 按 Cookie/Authorization 自动隔离（不同权限上下文各自保留覆盖样本）。
// Decisions 与输入一一对应（跳过 CONNECT 后用索引映射回原记录）。
// 过滤器故障时 fail-open 返回全量（精简包不得丢失请求）。
func filterRecords(records []Record) ([]Record, error) {
	reqs := make([]requestfilter.Request, 0, len(records))
	idx := make([]int, 0, len(records)) // reqs[i] 对应 records[idx[i]]
	for i, rec := range records {
		if rec.Method == "CONNECT" {
			continue
		}
		reqs = append(reqs, requestfilter.Request{
			Method:       rec.Method,
			URL:          rec.URL,
			Header:       http.Header(rec.RequestHeaders),
			Body:         []byte(rec.RequestBody),
			BodyCaptured: true,
		})
		idx = append(idx, i)
	}
	result, err := requestfilter.FilterBatch(reqs, requestfilter.Options{
		DedupStrategy: requestfilter.DedupStrategyCoverageParameters,
	})
	if err != nil {
		return records, nil // fail-open：返回全量，宁可冗余不丢请求
	}
	out := make([]Record, 0, len(result.Accepted))
	for i, decision := range result.Decisions {
		if decision.Action == requestfilter.ActionAllow && i < len(idx) {
			out = append(out, records[idx[i]])
		}
	}
	// 2xx 优先保留：FilterBatch 按顺序留每组首条，但重试场景首条可能是 503。
	// 同形状中存在 2xx 时，用它替换保留的非 2xx 样本（扫描器拿到的是成功形态）。
	outIndex := make(map[string]int, len(out)) // shapeKey -> out 下标
	for i, rec := range out {
		key, _ := shapeKey(rec)
		outIndex[key] = i
	}
	for _, rec := range records {
		if rec.Method == "CONNECT" || rec.Status < 200 || rec.Status >= 300 {
			continue
		}
		key, _ := shapeKey(rec)
		if i, ok := outIndex[key]; ok && (out[i].Status < 200 || out[i].Status >= 300) {
			out[i] = rec
		}
	}
	return out, nil
}
