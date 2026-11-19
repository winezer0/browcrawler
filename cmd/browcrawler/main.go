// browcrawler 入口：只负责解析 CLI、构造覆盖项、装配应用并报告最终错误。
// 所有进程生命周期与回收顺序都在 internal/app 里，任何错误都从 run() 返回，
// 绝不在启动浏览器之后调用 log.Fatal / os.Exit（否则 defer 不执行、进程残留）。
//
// CLI 契约（GNU 风格，kong 解析）：长选项为主 `--depth 2`，高频项配短选项
// （-u/-d/-o/-b/-c/-m/-r/-s）；`--` 终止符后的内容不做选项解析。
// v0.3.0 起旧单横线长名（-depth 等）不再支持（CHANGELOG 有 breaking 记录）。
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/winezer0/browcrawler/internal/app"
	"github.com/winezer0/browcrawler/internal/config"

	"github.com/alecthomas/kong"
)

// cli 是完整 CLI 契约；字段与 v0.2 的 flag 语义一一对应。
// 布尔开关里默认开启的项用 negatable（--no-xxx 关闭），与 GNU 惯例一致。
type cli struct {
	ConfigPath string `name:"config" help:"配置文件路径" default:"config.yaml"`

	// 输入
	URL   string `name:"url" short:"u" help:"爬取起始 URL"`
	Seeds string `name:"seeds" help:"额外起始 URL，逗号分隔（配合 --concurrency 做多会话）"`
	Scope string `name:"scope" help:"域名范围，逗号分隔；re: 前缀为正则；any 表示不限域"`

	// 爬取
	Depth                int     `name:"depth" short:"d" help:"BFS 深度（-1 使用配置值）" default:"-1"`
	MaxPages             int     `name:"max-pages" short:"m" help:"单任务最大页面数（0=不限）" default:"0"`
	RatePerSec           float64 `name:"rate" short:"r" help:"每秒最多导航次数（0=不限速）" default:"0"`
	HostRate             float64 `name:"host-rate" help:"每主机每秒最多导航次数" default:"0"`
	SettleMS             int     `name:"settle-ms" short:"s" help:"页面 load 后等待毫秒数" default:"-1"`
	Resume               bool    `name:"resume" help:"从 work/checkpoints 断点恢复（按 seed 匹配）"`
	Retries              int     `name:"retries" help:"导航失败/429|503 重试次数（指数退避）" default:"0"`
	Interactions         int     `name:"interactions" help:"每页交互点击上限（触发 onclick 隐藏接口，0=禁用）" default:"0"`
	NoForms              bool    `name:"no-forms" help:"关闭表单自动填充与提交"`
	NoSafety             bool    `name:"no-safety" help:"关闭危险操作防护（登出/改密码/删除类将真实触发，慎用）"`
	NoBasecrawler        bool    `name:"no-basecrawler" help:"关闭内置 basecrawler 静态 HTTP 发现"`
	BasecrawlerMaxURLs   int     `name:"basecrawler-max-urls" help:"basecrawler 每个种子的静态请求上限" default:"-1"`
	ObeyRobots           bool    `name:"obey-robots" help:"遵守目标站点 robots.txt"`
	ScopeMode            string  `name:"scope-mode" help:"作用域档位 fqdn|rdn|dnk|regex|all（空=legacy 由 --scope 推导）"`
	ScopeValue           string  `name:"scope-value" help:"scope-mode=dnk 的关键字 / regex 的正则；fqdn/rdn 留空用 seed 主机"`
	NoJSExtract          bool    `name:"no-js-extract" help:"关闭 JS 接口提取（basecrawler/parse）"`
	NoJSChunks           bool    `name:"no-js-chunks" help:"关闭浏览器会话内的同源懒加载 JS chunk 抓取"`
	JSChunkMaxDepth      int     `name:"js-chunk-max-depth" help:"chunk 递归深度上限（-1 使用配置值）" default:"-1"`
	JSChunkMaxCount      int     `name:"js-chunk-max-count" help:"每页 chunk 抓取次数上限（-1 使用配置值）" default:"-1"`
	JSChunkMaxBytes      int64   `name:"js-chunk-max-bytes" help:"单个 JS chunk 最大字节数（-1 使用配置值）" default:"-1"`
	JSChunkMaxTotalBytes int64   `name:"js-chunk-max-total-bytes" help:"每页 JS chunk 总字节上限（-1 使用配置值）" default:"-1"`
	JSChunkTimeoutMS     int     `name:"js-chunk-timeout-ms" help:"单个 JS chunk 浏览器请求超时毫秒（-1 使用配置值）" default:"-1"`
	NoRuntimeRoute       bool    `name:"no-runtime-route" help:"关闭运行时路由捕获"`
	FeedPath             string  `name:"feed" help:"外部资产回灌文件：basecrawler JSONL 或纯文本 URL 列表"`
	NoWSCapture          bool    `name:"no-ws-capture" help:"关闭 WebSocket 帧捕获"`
	DeliverOutScope      bool    `name:"deliver-outscope" help:"域外流量单独导出 <out>.outscope.txt（第三方资产清单）"`

	// 输出
	Out              string `name:"out" short:"o" help:"流量 JSONL 输出路径"`
	Discovered       string `name:"discovered" help:"发现链接 JSONL 输出路径"`
	HAR              string `name:"har" help:"额外导出 HAR 文件路径"`
	Requests         string `name:"requests" help:"导出可重放的原始 HTTP 请求包文本（安全测试交付物）"`
	RequestsFiltered string `name:"requests-filtered" help:"导出形状去重的精简请求包（每端点+参数形状一条）"`
	Report           string `name:"report" help:"导出端点形状分组报告（crawl-report.json）"`
	SaveJS           bool   `name:"save-js" help:"JS 源码落盘到 out/js/<url-hash>.js（溯源用）"`
	RawDump          bool   `name:"raw-dump" help:"JSONL 附带原始请求/响应头报文"`
	MetricsFile      string `name:"metrics" help:"指标汇总 JSONL 输出路径"`

	// 浏览器与会话
	Backend    string `name:"backend" short:"b" help:"渲染后端：obscura / chrome / auto（失败自动降级）"`
	MaxSession int    `name:"max-session" help:"会话池容量" default:"0"`
	KeepDir    bool   `name:"keep-session-dir" help:"保留会话临时目录（含日志，便于排障）"`
	NoStealth  bool   `name:"no-stealth" help:"关闭 Obscura stealth 模式"`

	// 代理与证书
	Upstream     string `name:"upstream" help:"goproxy 上游代理；none 表示直连"`
	Passthrough  string `name:"passthrough" help:"不做 MITM 的域名，逗号分隔，支持 *.example.com"`
	MergeRoots   bool   `name:"merge-roots" help:"SSL_CERT_FILE 合并公共根证书（需 --public-roots）"`
	RootsFile    string `name:"public-roots" help:"公共根证书 PEM 文件（同时开启 merge-roots）"`
	PerSessionCA bool   `name:"per-session-ca" help:"每会话生成独立临时 CA"`

	// 认证与边界
	AuthFile string `name:"auth-file" help:"认证头文件（每行 Header-Name: value）"`
	Creds    string `name:"creds" help:"真实凭据 user:pass（优先于表单测试字典）"`
	IPAllow  string `name:"ip-allow" help:"IP/CIDR 白名单，逗号分隔"`
	IPDeny   string `name:"ip-deny" help:"IP/CIDR 黑名单，逗号分隔"`

	// 运行
	Concurrent int  `name:"concurrency" short:"c" help:"并发任务数（0=按任务数与 max_session 自动）" default:"0"`
	Version    bool `name:"version" help:"输出版本信息并退出（不读取配置/不启动浏览器）"`
}

// toOverrides 映射为 internal/app 的覆盖项（v0.2 Overrides.Apply 语义不变）。
func (c *cli) toOverrides() app.Overrides {
	o := app.Overrides{
		Scope:                 c.Scope,
		Depth:                 c.Depth,
		Traffic:               c.Out,
		Discovered:            c.Discovered,
		HAR:                   c.HAR,
		Requests:              c.Requests,
		Report:                c.Report,
		RequestsFiltered:      c.RequestsFiltered,
		SaveJS:                c.SaveJS,
		ScopeMode:             c.ScopeMode,
		ScopeValue:            c.ScopeValue,
		NoForms:               c.NoForms,
		Resume:                c.Resume,
		NavRetries:            c.Retries,
		MaxInteractions:       c.Interactions,
		HostRate:              c.HostRate,
		AuthFile:              c.AuthFile,
		Creds:                 c.Creds,
		Upstream:              c.Upstream,
		NoStealth:             c.NoStealth,
		ObeyRobots:            c.ObeyRobots,
		MergeRoots:            c.MergeRoots,
		PerSession:            c.PerSessionCA,
		KeepDir:               c.KeepDir,
		SettleMS:              c.SettleMS,
		NoJSExtract:           c.NoJSExtract,
		NoJSChunks:            c.NoJSChunks,
		JSChunkMaxDepth:       optionalInt(c.JSChunkMaxDepth),
		JSChunkMaxCount:       optionalInt(c.JSChunkMaxCount),
		JSChunkMaxBytes:       optionalInt64(c.JSChunkMaxBytes),
		JSChunkMaxTotalBytes:  optionalInt64(c.JSChunkMaxTotalBytes),
		JSChunkFetchTimeoutMS: optionalInt(c.JSChunkTimeoutMS),
		NoRuntimeRoute:        c.NoRuntimeRoute,
		NoWSCapture:           c.NoWSCapture,
		NoSafety:              c.NoSafety,
		NoBasecrawler:         c.NoBasecrawler,
		BasecrawlerMaxURLs:    c.BasecrawlerMaxURLs,
		FeedPath:              c.FeedPath,
		DeliverOutScope:       c.DeliverOutScope,
		Backend:               c.Backend,
		MaxSession:            c.MaxSession,
		MaxPages:              c.MaxPages,
		RatePerSec:            c.RatePerSec,
		RawDump:               c.RawDump,
		MetricsFile:           c.MetricsFile,
		RootsFile:             c.RootsFile,
		Passthrough:           c.Passthrough,
		IPAllow:               c.IPAllow,
		IPDeny:                c.IPDeny,
	}
	return o
}

func optionalInt(value int) *int {
	if value < 0 {
		return nil
	}
	return &value
}

func optionalInt64(value int64) *int64 {
	if value < 0 {
		return nil
	}
	return &value
}

func main() {
	c := &cli{}
	kong.Parse(c,
		kong.Name("crawler"),
		kong.Description("纯浏览器模式的安全检查爬虫：渲染 + MITM 全流量录制，产出可重放请求包"),
		kong.UsageOnError(),
		kong.Exit(func(code int) { os.Exit(code) }),
	)
	if c.Version {
		fmt.Println(VersionString())
		os.Exit(0)
	}
	if strings.TrimSpace(c.URL) == "" && strings.TrimSpace(c.Seeds) == "" {
		fmt.Fprintln(os.Stderr, "usage: crawler --url <seed> [--depth 2] [--out out/traffic.jsonl] [--backend auto]")
		os.Exit(2)
	}
	if err := run(c); err != nil {
		log.Printf("[fatal] %v", err)
		os.Exit(1)
	}
}

func run(c *cli) error {
	cfg, err := config.Load(c.ConfigPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	c.toOverrides().Apply(cfg)
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	seeds := buildSeeds(c.URL, c.Seeds)
	if err := app.EnsureScope(cfg, seeds[0]); err != nil {
		return err
	}
	logger := log.New(os.Stdout, "", log.LstdFlags|log.Lmicroseconds)
	if strings.TrimSpace(c.Scope) == "" {
		logger.Printf("[config] scope 未指定，默认限定为 %s（跨站爬取请显式传 --scope all）", cfg.Crawl.Scope)
	}
	logger.Printf("[config] backend=%s max_session=%d tasks=%d", cfg.BrowserBackend, cfg.MaxSession, len(seeds))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	a, err := app.New(cfg, logger)
	if err != nil {
		return fmt.Errorf("init app: %w", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.Timeout.Shutdown())
		defer cancel()
		if e := a.Close(shutdownCtx); e != nil {
			logger.Printf("[app] close error: %v", e)
		}
	}()
	return a.Run(ctx, seeds, c.Concurrent)
}

func buildSeeds(primary, extra string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range append([]string{primary}, strings.Split(extra, ",")...) {
		s = strings.TrimSpace(s)
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
