# browcrawler

[![CI](https://github.com/winezer0/browcrawler/actions/workflows/ci.yml/badge.svg)](https://github.com/winezer0/browcrawler/actions/workflows/ci.yml)

纯浏览器模式的安全检查爬虫：渲染 + MITM 全流量录制 + 表单自动提交，目标是产出**完善、可重放的请求包**。

- **渲染后端**：Obscura（stealth 指纹伪装）为主，Chrome for Testing 作为降级后端，`auto` 自动切换
- **流量捕获**：浏览器发出的**全部**请求（页面/XHR/fetch/表单提交）经 goproxy MITM 解密录制——完整请求头、body、响应体，不做截断
- **定位边界**：本程序只做浏览器模式爬取；HTTP（非渲染）爬虫由其他模块承担

## 功能一览

| 能力 | 开关 |
|--|--|
| BFS 爬取（深度/域名 scope/去重） | `--depth` `--scope` |
| 渲染后端选择与自动降级 | `--backend obscura\|chrome\|auto` |
| 全流量 JSONL + body 限额（真实流不截断） | `--out` / `crawl.max_body_size` |
| 表单自动填充提交（crawlergo 式，CSRF 保护） | `crawl.form_submit`（默认开） |
| 动态路径段泛化去重（防 `/users/123` 膨胀） | `crawl.url_pattern_threshold`（默认 10） |
| 认证态注入（静态 Cookie/Header） | `--auth-file` |
| 真实凭据登录（优先于测试字典） | `--creds user:pass` |
| robots.txt / IP-CIDR 边界 / 限速 / 页数预算 | `--obey-robots` `--ip-deny` `--rate` `--max-pages` |
| 每主机限速 + 重试退避（429/503） | `--host-rate` `--retries` |
| 断点续扫 | `--resume` |
| 可重放请求包 / 端点报告 / HAR / 原始报文旁路 | `--requests` `--report` `--har` `--raw-dump` |
| 会话池（并发多会话 + 健康巡检） | `--max-session` `--seeds` `--concurrency` |
| TLS passthrough（证书固定/mTLS 目标） | `--passthrough`（需配合 `--public-roots`） |
| 指标汇总 | `--metrics <file>` |

## 快速开始

### 1. 构建

```bash
go build -o bin/crawler.exe ./cmd/crawler
```

### 2. 下载运行期依赖（不入库，`bin/`、`assets/ca/` 均被 .gitignore 排除）

以下命令走本机 `127.0.0.1:10808` 代理（换成你的代理或直连）：

```bash
# Obscura v0.2.6（主渲染后端，Windows）
curl -L -x http://127.0.0.1:10808 -o bin/obscura.zip \
  https://github.com/winezer0/obscura/releases/download/v0.2.6/obscura-x86_64-windows-stealth.zip
python -c "import zipfile;zipfile.ZipFile('bin/obscura.zip').extractall('bin/obscura')"

# Chrome for Testing 157.0.8103.38（降级后端，可选）
curl -L -x http://127.0.0.1:10808 -o bin/chrome-win64.zip \
  https://storage.googleapis.com/chrome-for-testing-public/157.0.8103.38/win64/chrome-win64.zip
python -c "import zipfile;zipfile.ZipFile('bin/chrome-win64.zip').extractall('bin/chrome')"

# 公共根证书（-public-roots / merge_public_roots 用，可选）
curl -L -x http://127.0.0.1:10808 -o assets/ca/cacert.pem https://curl.se/ca/cacert.pem
```

CA 证书与私钥会在首次运行时自动生成到 `assets/ca/`。

### 3. 一键验收

```bash
python scripts/verify.py                     # 本地全量（25 PASS）
python scripts/verify.py --https --extended  # 含外网 HTTPS 与扩展能力（41 PASS）
python scripts/stability.py --minutes 9.5    # 连续稳定性观察
```

### 4. 运行

```bash
# 单会话爬取 + 请求包导出
bin/crawler.exe --url https://quotes.toscrape.com/ --depth 2 \
  --requests out/requests.txt --har out/traffic.har

# 带认证态（Cookie）爬取登录后页面
bin/crawler.exe --url https://target.example.com/ --auth-file auth.txt --requests out/requests.txt
# auth.txt 内容示例：
#   Cookie: session=xxxx
#   X-API-KEY: xxx

# 自动登录（真实凭据优先于测试字典填充登录表单）
bin/crawler.exe --url https://target.example.com/ --creds "user:pass" --requests out/requests.txt

# 并发 3 会话 + 指标
bin/crawler.exe --url http://a.example.com/ --seeds "http://b.example.com/,http://c.example.com/" \
  --max-session 3 --concurrency 3 --metrics work/metrics.jsonl

# 限速 + 重试 + 断点续扫
bin/crawler.exe --url https://target/ --depth 3 --rate 2 --host-rate 1 --retries 3 --resume

# 交互式探索（触发 onclick 隐藏接口）+ 全套交付物
bin/crawler.exe --url https://target/ --depth 2 --interactions 5 \
  --requests out/requests.txt --requests-filtered out/requests-filtered.txt \
  --report out/report.json --har out/traffic.har --save-js

# Linux 交叉编译（jsluice 依赖 cgo，经 WSL Ubuntu 真实工具链构建）
.\build.ps1 build-linux -Version v0.2.0   # 产出 bin/crawler-linux-amd64
.\build.ps1 release -Version v0.2.0      # 同时产出 windows zip + linux tar.gz
```

## 配置速查

完整模板见 `config.example.yaml`。关键项：

| 配置 | 默认 | 说明 |
|--|--|--|
| `browser_backend` | obscura | obscura / chrome / auto |
| `max_body_size` | 2MiB | 单响应录制上限，超限只记元信息（真实流不截断） |
| `crawl.scope` | 空=seed 主机 | 跨站需显式 `--scope any` 或列举域名 |
| `crawl.form_submit` | true | 表单自动填充提交（hidden 字段不覆盖，保护 CSRF） |
| `crawl.max_interactions` | 0 | 每页交互点击上限（触发 onclick 隐藏接口） |
| `crawl.nav_retries` | 0 | 导航失败/429/503 重试次数（指数退避+jitter） |
| `crawl.host_rate_per_sec` | 0 | 每主机限速 |
| `crawl.js_chunks.enabled` | true | 读取静态恢复出的同源懒加载 JS chunk；`--no-js-chunks` 可关闭 |
| `crawl.js_chunks.max_depth` | 2 | chunk 递归深度，0 表示不抓取；硬上限 5 |
| `crawl.js_chunks.max_count` | 16 | 每页最多尝试读取的 chunk 数；硬上限 128 |
| `crawl.js_chunks.max_chunk_bytes` | 2MiB | 单 chunk 最大响应体；硬上限 16MiB |
| `crawl.js_chunks.max_total_bytes` | 8MiB | 每页累计响应体上限；硬上限 64MiB |
| `crawl.js_chunks.fetch_timeout_ms` | 15000 | 单个 chunk 浏览器请求超时；小于 `timeout.navigation_ms`，硬上限 120000，可用 `--js-chunk-timeout-ms` 覆盖 |
| `crawl.url_pattern_threshold` | 10 | 路径段泛化阈值；0 禁用 |
| `crawl.url_pattern_ttl_seconds` | 0 | 去重条目过期时间（秒）；超大站建议 3600 |
| `crawl.nav_retries` | 0 | 导航失败/429/503 重试次数（指数退避+jitter） |
| `crawl.host_rate_per_sec` | 0 | 每主机限速 |

懒加载 chunk 通过当前浏览器页的 `fetch` 读取，仅接受同源响应（包括重定向后的最终地址），并使用浏览器现有的同源凭据；响应正文只送入静态解析器，不执行或导入脚本。CLI 可用 `--js-chunk-max-depth`、`--js-chunk-max-count`、`--js-chunk-max-bytes`、`--js-chunk-max-total-bytes`、`--js-chunk-timeout-ms` 覆盖配置。运行摘要包含 `bundle_chunks_fetched`、`bundle_truncated` 和 `bundle_chunk_fetch_errors`；启用 metrics 时也会记录解析截断、超限 chunk 和抓取失败计数。
| `crawl.auth_file` / `credentials` | 空 | 认证态注入 |
| `mitm.merge_public_roots` | false | 需同时给 `public_roots_file`（PEM） |
| `mitm.tls_passthrough` | [] | 不解密直通的域名（需公共根；Chrome 后端除外） |
| `pool.max_session` | 1 | 会话池容量 |
| `output.requests` | 空 | 可重放请求包导出路径 |

## 常见问题

- **换机器后外网 502**：`config.yaml` 的 `upstream_proxy` 写死了本机 10808，改成实际代理或置空（内网/回环自动直连）
- **`browser_backend: chrome` 报错找不到二进制**：按上文下载 Chrome for Testing 并解压到 `bin/chrome/`
- **`merge_public_roots=true` 启动报错**：必须同时指定 `mitm.public_roots_file`（Windows 系统证书库无法可靠导出完整证书，不能静默降级）
- **`tls_passthrough` 导航失败**：隧道内是真实证书，必须合并公共根（Chrome 后端除外）
- **爬虫陷阱（URL 无限膨胀）**：调低 `url_pattern_threshold`（如 5）；分页等“同名参数不同值”页面不会被它误杀
- **凭据/token 管理**：认证文件、`--creds` 参数请勿提交入库；注意检查 git remote URL 不要内嵌 PAT 明文（用凭据管理器）

## 安全与授权声明

- 仅用于**已获得明确授权**的目标（自有资产、测试站点或书面授权范围）；`scope`/`ip` 策略是授权边界的实现而非建议
- 默认只读：不篡改请求/响应流、不预读超限 body；表单提交使用测试值（hidden 字段与 CSRF token 保持原值）
- 表单提交会对目标产生**真实的写操作流量**——对生产环境请先评估 `form_submit` 是否开启

## 架构

```
cmd/crawler(仅CLI) → app(编排) → pool(会话池+巡检) → session(Options注入)
    → browserrunner(Obscura/Chrome/降级组合) + proxyrunner(goproxy MITM)
    → crawl(BFS/表单/认证/去重) → config/ca/policy/metrics/storage
```

设计文档（含全部实测记录）：`docs/实施方案.md`。上游去重库：[winezer0/requestfilter](https://github.com/winezer0/requestfilter)（MIT，含本项目贡献的路径段泛化）。
