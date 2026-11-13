// feed.go：外部资产发现结果回灌（assetcrawler 对接）。
//
// 设计：assetcrawler 负责静态发现（HTML 20+ 标签、JS AST、chunk graph、
// importmap、SSR payload、sitemap、OpenAPI 探测——发现面远大于浏览器渲染），
// browcrawler 经 -feed 把它的结果灌进 BFS 队列，由浏览器真实打开并 MITM
// 录制完整请求包——静态发现拿不到请求包的部分（懒加载 chunk、前端路由、
// XHR 链）由此闭环。
//
// 输入格式自动识别（按行）：
//   - JSON 行：assetcrawler Result（URL 必填；Method/Kind/Source/Confidence
//     可选，未知字段忽略）
//   - 纯文本行：每行一个绝对 URL（ffuf/dirsearch/URLFinder 的 txt 输出直接可用）
package crawl

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
)

// FeedSeed 是一条外部回灌候选。
type FeedSeed struct {
	URL    string
	Method string // 空 = GET；非 GET 只记录为发现（浏览器导航仅 GET）
	Kind   string // assetcrawler: chunk|route（透传到 discovered.source）
	Source string // assetcrawler 的 Source 透传，便于溯源
}

// LoadFeed 读取 -feed 文件：JSONL（assetcrawler）或纯文本 URL 列表。
// 解析失败的行跳过并在 error 汇总中注明（不因个别脏行放弃整批）。
func LoadFeed(path string) ([]FeedSeed, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open feed: %w", err)
	}
	defer f.Close()

	var out []FeedSeed
	var skipped []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024) // 单行上限 1MB
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
			continue
		}
		if strings.HasPrefix(line, "{") {
			var obj struct {
				URL       string `json:"URL"`
				Method    string `json:"Method"`
				Kind      string `json:"Kind"`
				Source    string `json:"Source"`
				LowerURL  string `json:"url"`
				LowerKind string `json:"kind"`
			}
			if err := json.Unmarshal([]byte(line), &obj); err != nil {
				skipped = append(skipped, fmt.Sprintf("line %d: bad json", lineNo))
				continue
			}
			u := obj.URL
			if u == "" {
				u = obj.LowerURL
			}
			kind := obj.Kind
			if kind == "" {
				kind = obj.LowerKind
			}
			if u == "" {
				skipped = append(skipped, fmt.Sprintf("line %d: missing URL", lineNo))
				continue
			}
			out = append(out, FeedSeed{URL: u, Method: strings.ToUpper(obj.Method), Kind: kind, Source: obj.Source})
			continue
		}
		// 纯文本行：须为绝对 http(s) URL
		u, perr := url.Parse(line)
		if perr != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			skipped = append(skipped, fmt.Sprintf("line %d: not an absolute URL", lineNo))
			continue
		}
		out = append(out, FeedSeed{URL: line})
	}
	if err := sc.Err(); err != nil {
		return out, fmt.Errorf("read feed: %w", err)
	}
	if len(skipped) > 0 {
		// 不因脏行失败：汇总前几条供日志
		return out, fmt.Errorf("%d 行跳过（示例: %s）", len(skipped), strings.Join(skipped[:min(len(skipped), 3)], "; "))
	}
	return out, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
