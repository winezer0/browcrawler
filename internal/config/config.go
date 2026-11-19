// Package config 集中定义 browcrawler 的全部配置结构与校验规则。
//
// 分层约定：本包只做「纯数据 + 校验」，不启动进程、不读写运行期资源（CA 文件除外，
// 由 ca 包负责）。任何新增配置项必须同时具备 (1) yaml 字段 (2) 校验规则 (3) 真实消费点，
// 避免出现「配置写了却不生效」的假象。
package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Backend 渲染后端取值。
const (
	BackendObscura = "obscura"
	BackendChrome  = "chrome"
	BackendAuto    = "auto" // 优先 Obscura，启动失败自动降级 Chrome
)

type Config struct {
	BrowserBackend string `yaml:"browser_backend"`
	ObscuraBin     string `yaml:"obscura_bin"`
	ChromeBin      string `yaml:"chrome_bin"`
	MaxSession     int    `yaml:"max_session"`

	MaxBodySize    int64             `yaml:"max_body_size"`
	Stealth        bool              `yaml:"stealth"`
	ObeyRobots     bool              `yaml:"obey_robots"`
	KeepSessionDir bool              `yaml:"keep_session_dir"`
	UpstreamProxy  string            `yaml:"upstream_proxy"`
	Env            map[string]string `yaml:"env"`

	Crawl   CrawlConfig   `yaml:"crawl"`
	Mitm    MitmConfig    `yaml:"mitm"`
	Output  OutputConfig  `yaml:"output"`
	Timeout TimeoutConfig `yaml:"timeout"`
	Pool    PoolConfig    `yaml:"pool"`
	Policy  PolicyConfig  `yaml:"policy"`
	Metrics MetricsConfig `yaml:"metrics"`
}

type CrawlConfig struct {
	Depth int    `yaml:"depth"`
	Scope string `yaml:"scope"`
	// BasecrawlerEnabled runs the bounded static HTTP crawler before browser navigation.
	BasecrawlerEnabled bool `yaml:"basecrawler_enabled"`
	// BasecrawlerMaxURLs limits static HTTP fetches per seed.
	BasecrawlerMaxURLs int `yaml:"basecrawler_max_urls"`
	// WSCapture WebSocket 帧捕获（默认开，纯只读观察）：经 CDP Network 事件
	// 记录 WS 建连/握手/帧/关闭，帧以 resource_type=websocket 进 JSONL。
	WSCapture bool `yaml:"ws_capture"`
	// FeedPath 外部资产回灌文件（basecrawler JSONL 或纯文本 URL 列表）：
	// 静态发现的 chunk/路由/候选 URL 由浏览器真实打开并录制请求包。空 = 不灌。
	FeedPath string `yaml:"feed"`
	// Safety 危险操作防护（默认开）：登出/改密码/删除类 URL 与表单不真实
	// 触发，发现不受损（仍进 discovered，source=url-skipped/form-skipped）。
	Safety bool `yaml:"safety"`
	// SafetyURLSkip / SafetyFormSkip 追加危险正则（对内置词表叠加）。
	SafetyURLSkip  []string `yaml:"safety_url_skip"`
	SafetyFormSkip []string `yaml:"safety_form_skip"`
	// ScopeMode 作用域档位：fqdn（精确主机）| rdn（根域+子域，katana 默认
	// 语义）| dnk（域名关键字）| regex（自定义正则）| all（不限域）。
	// 空 = legacy 兼容（由 Scope 字符串推导）。双层生效：导航入队 +
	// 交付物（discovered/requests/report）默认过滤域外，JSONL 附 InScope 标记。
	ScopeMode      string  `yaml:"scope_mode"`
	ScopeValue     string  `yaml:"scope_value"` // dnk: 关键字；regex: 正则；fqdn/rdn 留空用 seed 主机
	JSExtract      bool    `yaml:"js_extract"`  // JS 接口提取（basecrawler/parse）
	SkipStaticBody bool    `yaml:"skip_static_body"`
	SettleMS       int     `yaml:"settle_ms"`
	MaxPages       int     `yaml:"max_pages"`    // 0 = 不限
	RatePerSec     float64 `yaml:"rate_per_sec"` // 0 = 不限速
	FormSubmit     bool    `yaml:"form_submit"`  // crawlergo 式表单自动填充与提交（安全测试用）
	// URLPatternThreshold 启用动态路径段泛化去重（requestfilter）：路径位置累计
	// ≥N 个不同值即折叠为模式，同模式变体不再导航。0 = 禁用；爬虫建议 10。
	URLPatternThreshold int `yaml:"url_pattern_threshold"`
	// URLPatternTTLSeconds 去重状态的条目过期时间（秒）：超过 TTL 未再出现的
	// 形状/值会被淘汰，容量自然回收，避免超大站触发容量拒断。0 = 不过期
	// （维持现状）；大站（预期端点形状 >1 万）建议 3600。
	URLPatternTTLSeconds int `yaml:"url_pattern_ttl_seconds"`
	// NavRetries 导航失败或 429/503 的重试次数（指数退避 + jitter）。0 = 不重试。
	NavRetries int `yaml:"nav_retries"`
	// HostRatePerSec 每主机每秒最多导航次数（与全局限速叠加）。0 = 不限。
	HostRatePerSec float64 `yaml:"host_rate_per_sec"`
	// AuthFile 静态认证头文件（每行 Header-Name: value），导航前注入浏览器。
	AuthFile string `yaml:"auth_file"`
	// Credentials 真实凭据 "user:pass"，优先于表单测试字典。
	Credentials string `yaml:"credentials"`
	// MaxInteractions 每页最大交互点击数（katana -interactive 简化版）：触发
	// onclick/fetch 等隐藏接口。0 = 禁用（默认）。
	MaxInteractions int `yaml:"max_interactions"`
	// RuntimeRoute 运行时路由捕获（默认开）：按框架分包探针（vue/react/
	// angular/next/nuxt/sveltekit/generic）读浏览器内路由表 + history hook，
	// scope 内路由回灌 BFS 队列真实打开并录制。纯只读观察。
	RuntimeRoute bool `yaml:"runtime_route"`
	// JSChunks controls bounded same-origin fetching of recovered lazy JavaScript chunks.
	JSChunks JSChunksConfig `yaml:"js_chunks"`
	// Resume 由 CLI -resume 设置；config 里无对应 yaml（避免误开）。
	Resume bool `yaml:"-"`
}

type JSChunksConfig struct {
	Enabled        bool  `yaml:"enabled"`
	MaxDepth       int   `yaml:"max_depth"`
	MaxCount       int   `yaml:"max_count"`
	MaxChunkBytes  int64 `yaml:"max_chunk_bytes"`
	MaxTotalBytes  int64 `yaml:"max_total_bytes"`
	FetchTimeoutMS int   `yaml:"fetch_timeout_ms"`
}

type MitmConfig struct {
	CACrt            string   `yaml:"ca_crt"`
	CAKey            string   `yaml:"ca_key"`
	MergePublicRoots bool     `yaml:"merge_public_roots"`
	PublicRootsFile  string   `yaml:"public_roots_file"` // merge=true 时必填（PEM bundle）
	PerSessionCA     bool     `yaml:"per_session_ca"`    // 每会话独立临时 CA
	TLSPassthrough   []string `yaml:"tls_passthrough"`   // 不做 MITM、直接隧道的域名
}

type OutputConfig struct {
	Traffic          string `yaml:"traffic"`
	Discovered       string `yaml:"discovered"`
	HAR              string `yaml:"har"`
	Requests         string `yaml:"requests"`          // 可重放的原始 HTTP 请求包文本（安全测试交付物）
	Report           string `yaml:"report"`            // 端点形状分组报告（crawl-report）
	RequestsFiltered string `yaml:"requests_filtered"` // 形状去重的精简请求包
	SaveJSDir        string `yaml:"save_js_dir"`       // 非空时 JS 源码落盘目录
	OutScope         string `yaml:"out_scope"`         // 域外流量清单输出路径（--deliver-outscope 自动生成）
	RawDump          bool   `yaml:"raw_dump"`          // 旁路附带原始请求/响应头报文（不消费 body）
}

// PoolConfig 会话池与健康巡检。
type PoolConfig struct {
	HealthIntervalMS int `yaml:"health_interval_ms"`
	ReuseLimit       int `yaml:"reuse_limit"` // 会话复用 N 次后强制重建，0 = 不限
	MaxIdleMS        int `yaml:"max_idle_ms"` // 空闲超时回收，0 = 不回收
}

// PolicyConfig 授权边界与流量策略。
type PolicyConfig struct {
	IPAllow []string `yaml:"ip_allow"` // CIDR 白名单，空 = 不限
	IPDeny  []string `yaml:"ip_deny"`  // CIDR 黑名单，优先级高于白名单
}

type MetricsConfig struct {
	Enabled bool   `yaml:"enabled"`
	File    string `yaml:"file"`
}

// TimeoutConfig 单位毫秒（yaml 直接写整数，避免 Duration 反序列化歧义）。
type TimeoutConfig struct {
	CDPReadyMS   int `yaml:"cdp_ready_ms"`
	NavigationMS int `yaml:"navigation_ms"`
	ShutdownMS   int `yaml:"shutdown_ms"`
	TaskMS       int `yaml:"task_ms"`
}

func (t TimeoutConfig) CDPReady() time.Duration {
	return time.Duration(t.CDPReadyMS) * time.Millisecond
}
func (t TimeoutConfig) Navigation() time.Duration {
	return time.Duration(t.NavigationMS) * time.Millisecond
}
func (t TimeoutConfig) Shutdown() time.Duration {
	return time.Duration(t.ShutdownMS) * time.Millisecond
}
func (t TimeoutConfig) Task() time.Duration { return time.Duration(t.TaskMS) * time.Millisecond }

func (p PoolConfig) HealthInterval() time.Duration {
	return time.Duration(p.HealthIntervalMS) * time.Millisecond
}
func (p PoolConfig) MaxIdle() time.Duration { return time.Duration(p.MaxIdleMS) * time.Millisecond }

func Default() *Config {
	return &Config{
		BrowserBackend: BackendObscura,
		ObscuraBin:     "./bin/obscura/obscura.exe",
		ChromeBin:      "./bin/chrome/chrome-win64/chrome.exe",
		MaxSession:     1,
		MaxBodySize:    2 * 1024 * 1024,
		Stealth:        true,
		Crawl: CrawlConfig{
			Depth: 2, JSExtract: true, SkipStaticBody: true, SettleMS: 600,
			BasecrawlerEnabled: true, BasecrawlerMaxURLs: 200,
			MaxPages: 0, RatePerSec: 0, FormSubmit: true, URLPatternThreshold: 10,
			URLPatternTTLSeconds: 0, NavRetries: 0, HostRatePerSec: 0,
			WSCapture:    true,
			RuntimeRoute: true,
			Safety:       true,
			JSChunks: JSChunksConfig{
				Enabled: true, MaxDepth: 2, MaxCount: 16,
				MaxChunkBytes: 2 << 20, MaxTotalBytes: 8 << 20,
				FetchTimeoutMS: 15000,
			},
		},
		Mitm: MitmConfig{
			CACrt: "./assets/ca/ca.crt",
			CAKey: "./assets/ca/ca.key",
		},
		Output: OutputConfig{
			Traffic: "./out/traffic.jsonl", Discovered: "./out/discovered.jsonl",
		},
		Timeout: TimeoutConfig{
			CDPReadyMS: 30000, NavigationMS: 45000, ShutdownMS: 8000, TaskMS: 600000,
		},
		Pool:    PoolConfig{HealthIntervalMS: 3000, ReuseLimit: 0, MaxIdleMS: 0},
		Policy:  PolicyConfig{},
		Metrics: MetricsConfig{Enabled: false, File: "./work/metrics.jsonl"},
	}
}

// Load 读取 yaml 并校验；文件不存在时返回已校验的默认配置。
func Load(path string) (*Config, error) {
	c := Default()
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return c, c.Validate()
		}
		return nil, err
	}
	if err := yaml.Unmarshal(b, c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("validate %s: %w", path, err)
	}
	return c, nil
}
