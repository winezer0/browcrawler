# basecrawler 解析包统一化改造——实施提示词

> 用途：把本提示词交给任意 Agent 即可在 basecrawler 仓库独立完成改造。
> 背景：browcrawler 已接入 `extractor.FromJS`（正则档）。本次把 basecrawler 的
> 解析能力收敛为**一个独立包 + 一个配置结构体**，让外部工具（browcrawler 等）
> 一次 import 拿到全部解析能力，开关统一配置。

## 提示词（可直接复制）

```
在 basecrawler 项目（GitHub: winezer0/basecrawler，Go 1.26，分支 main）
实施「解析包统一化对外提供」改造。先读 AGENTS.md/AGENTS.common.md（无则跳过）。
实施前确认 git 干净；分两个提交：①统一包+配置结构体 ②旧包兼容映射。

## 现状问题

1. 解析能力分散在四处：extractor（HTML/JS/响应头 URL 提取，已有 Options
   结构体与 JSExtract 注入点）、jsext（AST 提取统一类型）、jsext/goja
   （AST 引擎，`Extract(src []byte) ([]jsext.Request, error)`）、
   routegraph（webpack/vite chunk 与路由静态推导）、deobf（反混淆，被
   extractor 内部调用）。
2. 开关形式不一：extractor.Options 较完整，routegraph 用 ExecOptions，
   jsext 引擎无配置。
3. routegraph 把「静态解析」（chunk.go/esm.go/lexer.go）与「执行」
   （exec.go 的 ExtractByExec = route-exec 核心）混在同一包，外部接入时
   靠调用纪律防误用。

## 改造目标

1. 新建独立包 `pkg/parse`（若 pkg/ 目录不存在则创建），作为唯一对外入口：
   - **一个配置结构体**，全部开关集中、零值 = 安全默认：

     type Options struct {
         // JS 提取档位：JSModeRegex（默认，零依赖）| JSModeAST（goja 解析器）
         JSMode JSMode
         // AST 引擎注入点（覆盖 JSMode；签名沿用 extractor.Options.JSExtract，
         // 第二返回值 false 表示解析失败回退正则）
         JSExtract func(src string) ([]base.Request, bool)
         // 反混淆：DeobfOff（默认）| DeobfLight（packer/_0x 静态解包）
         // 注意：DeobfForce / DeobfExec（执行目标代码）**不进入本包**
         Deobf DeobfLevel
         // chunk/路由静态推导开关（routegraph 的 chunk.go/esm.go/lexer 部分）
         ChunkRecover bool
         // 静态资源过滤（图片/字体/CSS 路径丢弃），默认 true
         SkipStaticAssets bool
         // confidence=template 的占位路径过滤，默认 true（不可直接访问）
         SkipTemplates bool
         // 长度/数量限额（沿用现有 LimitReport）
         Limits *base.LimitReport
     }

   - **一组纯函数入口**（保持"纯函数、无状态、无 IO、无并发"既有约束）：
     ParseJS(src []byte, o Options) []base.Request
     ParseHTML(doc []byte, o Options) []base.Request
     ParseHeaders(h http.Header, o Options) []base.Request
     ParseRobots / ParseSitemap（如已有则收敛进来）
   - **统一输出类型**：base.Request（Raw/Method/Source/Confidence/Kind/
     Params/ParamValues），各解析器共用，不再各包自定义
2. 依赖纪律：
   - pkg/parse 核心只依赖 base + golang.org/x/net（HTML 解析）
   - AST 引擎经 JSExtract 注入（调用方自行 import jsext/goja 装配），
     pkg/parse **不直接 import goja/treesitter**——保证轻量使用者零重依赖
3. 边界（硬约束）：
   - 本包只做解析。执行类能力一律排除：routegraph/exec.go 的
     ExtractByExec、deobf 的 DeobfExec 不进 pkg/parse、不被它调用
4. 兼容：
   - 原 extractor 包公开 API 保持编译兼容（内部改为薄委托 pkg/parse），
     原有调用方（CLI/测试）不改
   - routegraph 拆分：静态解析函数迁入/委托 pkg/parse，exec.go 留原包
     并在包注释标注「执行类，opt-in」
   - CHANGELOG 记录：新增 pkg/parse；开关集中；执行类边界重申

## 实施步骤

1. 梳理现有公开函数清单（extractor/js.go 三类正则、base 过滤器、
   routegraph chunk/esm/lexer 静态入口、deobf.Do），列出映射表
2. 写 pkg/parse/options.go（Options 结构体 + Default() + 校验）
3. 写 pkg/parse/parse.go（ParseJS/ParseHTML/ParseHeaders，内部组合
   现有实现——优先委托而非复制代码）
4. extractor 各包改薄委托；routegraph 拆静态/执行
5. 测试迁移 + 新增表驱动用例：Options 组合（JSMode×Deobf×ChunkRecover）
   × 输入样例（正常 JS/混淆 JS/ESM/webpack chunk）；断言零值默认行为
6. go.mod 检查：pkg/parse 自身 require 仅 x/net；goja/treesitter 保持
   在需要者（jsext/goja、CLI）侧

## 验收

- pkg/parse 不 import 任何"本工程其他包"（base 除外），不 import
  goja/treesitter/esbuild
- 单一 Options 结构体覆盖既有全部解析开关；零值默认 = 安全行为
- 旧 extractor API 编译兼容；全仓 go test 通过
- browcrawler 侧兼容性锚点（写进包注释）：Options.JSExtract 签名 =
  func(src string) ([]base.Request, bool)
```

## 给 basecrawler 维护者的补充说明

- **消费方锚点**：browcrawler 的接入点只有两处——`extractor.FromJS`（已用，
  统一后换 `pkg/parse.ParseJS`）与 `Options.JSExtract` 注入（预留 AST 升级）。
  统一包落地后，browcrawler 从"正则档"升级"AST 档"只是装配一行注入的事。
- **边界语义**：`JSModeAST` 是 goja **解析器**（AST 遍历），不是执行——与
  DeobfExec/ExtractByExec 的"运行目标代码"有本质区别，前者可进统一包，后者不行。
- **依赖权衡**：如果认为 AST 引擎应进统一包（免注入），改为 `pkg/parse/ast.go`
  子文件 + build tag `ast`，保持默认构建零重依赖——两种形态二选一，写进 CHANGELOG。
