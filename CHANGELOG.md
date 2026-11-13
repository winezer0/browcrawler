# Changelog

所有显著变更记录于此。格式参考 Keep a Changelog；版本号语义化。

## [Unreleased]

### 变更
- JS 静态分析启用 AST 优先解析、轻量反混淆和 bundle chunk/路由恢复；来源保留实际解析路径（`ast:*`、`regex:*`、`bundle-chunk:*`、`bundle-route:*`）。
- safety 危险操作防护改为直接调用公共工具包
  `github.com/winezer0/basecrawler/safety`：删除 `internal/crawl/safety.go`
  的重复词表与判定实现，`crawler.go` / `forms.go` 直接 import 并使用
  `*safety.Safety`，`safety_test.go` 改为对公共包做黑盒回归。
  两个项目从此共用同一份词表与判定逻辑。
- `go.mod` 升级 `basecrawler` 到 `v0.5.0` 并删除本地 `replace`：`safety/`
  已随该版本发布，CI 与他人 clone 不再依赖本机路径。`go mod tidy` 后
  `requestfilter` 保持 `v0.1.3`，indirect 依赖与 `v0.5.0` 的模块图一致。
- 文档更正：`docs/提示词-F1-safety.md` 与 `docs/对标分析.md` 中「basecrawler
  不需要 safety」的论断已过时，补状态注记。

### 已验证
`go mod tidy` / `go build ./...` / `go vet ./...` / `go test ./...` 全部通过；
上游 `basecrawler v0.5.0` 已推送并打 tag。本项目本轮改动仅本地提交，
未推送远端。

## [v0.2.0] — 2026-11-09

安全检查能力第二阶段：去重/认证/续扫/限速 + 交互探索 + 报告（§16）。

### 新增
- 动态路径段泛化去重：集成 requestfilter v0.1.2（含本项目贡献的
  `PathSegmentThreshold` 泛化能力，上游 main `04be55c`）；
  quotes.toscrape.com 实测 discovered 1308→517（-60%）
- 认证态注入：`-auth-file`（Cookie/任意头经浏览器注入，请求包即带认证态）
- 真实凭据登录：`-creds user:pass` 优先于测试字典填充登录表单（CSRF token 保护）
- 断点续扫：`-resume`，每页原子写 checkpoint（seen+queue），恢复零重复导航
- 每主机限速：`-host-rate`；导航重试退避：`-retries`（429/503，指数+jitter）
- 交互式 UI 探索：`-interactions N`（onclick/role=button 派发点击，
  触发隐藏接口进入请求包）
- 端点形状分组报告：`-report`（方法+主机+路径+参数形状+状态直方图+
  认证上下文标注，凭据原文不落盘）
- 版本号：`-version`（构建时 `-ldflags -X` 注入 + Go VCS 修订时间），
  `build.ps1` 构建/测试/验收/发布脚本

### 修复
- CDP 连接老化自愈重连（真实站 depth=2 第 74 页后整批失败）
- Chrome loopback 代理绕过（`--proxy-bypass-list=<-loopback>`）
- Chrome 后台噪声请求归零（disable-features 白名单）
- 主后端启动失败时日志句柄泄漏导致会话目录删不掉

## [v0.1.0] — 2026-11-08

阶段一/二基础版（§8/§10/§13）。

- 结构化分层：config(校验)/ca/browserrunner/session/pool/policy/metrics/app
- goproxy MITM 只读旁路录制（body 限额不截断真实流、SSE/WS/101 元信息）
- Obscura stealth + Chrome for Testing 降级 + auto 自动切换
- 表单自动填充提交（crawlergo 式，CSRF 保护）、jsluice 接口提取
- robots.txt 自研实现、IP/CIDR 边界、限速/页数预算
- TLS passthrough、raw_dump、HAR 1.2（cookies/queryString/timings）
- 会话池（并发/健康巡检/复用）、-metrics 指标、git 基线与一键验收
  `scripts/verify.py`（43 PASS 基线）、连续稳定性 485 轮实测

## v0.3.0 — 未发布

### BREAKING

- **CLI 契约升级为 GNU 风格（迁移至 kong 解析器）**：长选项改双横线（`--url`/`--depth`/`--out`），
  旧单横线长名（`-url` 等）不再支持。新增高频短选项：`-u/--url`、`-d/--depth`、
  `-o/--out`、`-b/--backend`、`-c/--concurrency`、`-m/--max-pages`、`-r/--rate`、
  `-s/--settle-ms`；`--config` 语义不变。scripts/verify.py、stability.py、README
  已同步迁移；`--` 终止符后内容不做选项解析。
- 新增 `--no-runtime-route`（关闭运行时路由捕获，F2 默认开）。

### Added

- **BREAKING（CLI）**：JS 接口提取引擎由 BishopFox/jsluice 更换为
  basecrawler `extractor` 响应处理包（正则档，包内自带噪声/静态资源/模板
  占位过滤）；配置键 `jsluice` → `js_extract`，CLI `--no-jsluice` →
  `--no-js-extract`，发现 source 前缀 `jsluice:*` → `jsext:*`
- safety 危险操作防护（默认开，`--no-safety` 关闭）：登出/改密码/删除类 URL
  与表单不真实触发，发现不受损（仍进 discovered，source=url-skipped/form-skipped）；
  内置中英双语词表（Argo 基线），`safety_url_skip`/`safety_form_skip` 追加正则
- 运行时路由捕获（按框架分包探针：vue/react/angular/next/nuxt/sveltekit/generic），
  nuxt.com 实测 328 条路由、回灌导航 361 路径。
- scope_mode 五档双层过滤、交付侧 requestfilter 精简包、发布工程（build.ps1/CI）。
