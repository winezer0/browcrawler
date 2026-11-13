# F1 Safety 危险操作防护——实施提示词

> 用途：把本提示词交给任意 Agent（或作为自己的执行清单）即可独立完成 F1 实施。
> 背景：browcrawler 为纯浏览器模式安全检查爬虫，form_submit 默认自动提交全部表单，
> 真实站点上存在改密码/删数据/登出的实际风险（Argo 项目在 DVWA 上实测发生）。

## 提示词（可直接复制）

```
在 browcrawler 项目（C:/Users/windows/Desktop/browcrawler，Go 1.26，当前分支 main）
实现 F1「safety 危险操作防护」。项目规范先读 AGENTS.md/AGENTS.common.md（不存在则跳过）。
实施前先 git 提交暂存（本次为单批次修改，直接在新提交中完成）；只做针对性测试，
不跑全量测试。

## 功能语义

form_submit 默认自动提交页面全部表单、自动跟随全部链接。safety 在两个点拦截，
且必须保证「发现不受损」：被拦截的 URL/表单仍写入 discovered.jsonl
（source=url-skipped / form-skipped），只是不真实触发。默认开启，可显式关闭。

## 实现

1. 新建 internal/crawl/safety.go：

   type Safety struct{ enabled bool; urlSkip, formSkip []*regexp.Regexp }
   func NewSafety(enabled bool, extraURL, extraForm []string) (*Safety, error)
   func (s *Safety) SkipURL(raw string) bool        // 匹配原始 URL 串
   func (s *Safety) SkipForm(action string, fields []string) bool
                                                    // 匹配 action + 全部字段名/ID 联合串

   内置默认词表（(?i) 大小写不敏感，Argo safety 词表基线）：
   - URL:  logout|signout|sign-out|sign_out|log-out|\bexit\b|\bquit\b|注销|登出|退出登录
   - 表单: password_new|password_conf|password_confirm|password_current|
           old_?pass(word)?|new_?pass(word)?|curr_?pass(word)?|pass_?conf|
           修改密码|更改密码|重置密码|
           delete_?(user|account|post|article)|remove_?(user|account)|
           删除用户|删除账号|删除文章|reset_?password
   - extraURL/extraForm 为追加正则（叠加不覆盖）；非法正则返回错误
   - enabled=false 时 SkipURL/SkipForm 恒为 false
   - 匹配大小写不敏感；表单匹配用 action + 字段名/ID 联合串（比 outerHTML 稳）

2. 配置（internal/config/config.go）：
   - CrawlConfig 新增 Safety bool `yaml:"safety"`（Default() 置 true）、
     SafetyURLSkip []string `yaml:"safety_url_skip"`、
     SafetyFormSkip []string `yaml:"safety_form_skip"`
3. CLI（cmd/crawler/main.go，kong）：`--no-safety` 布尔项，help 注明「关闭后
   危险操作将真实触发，慎用」；internal/app/options.go 的 Overrides 增加
   NoSafety bool 并在 Apply 中置 c.Crawl.Safety=false；config.example.yaml 同步注释
4. 接线（internal/crawl/crawler.go）：
   - Crawler 持有 safety 字段，New() 中构造（含错误返回）
   - links/postLinks 入队循环：SkipURL 命中 → Discovery{Source:"url-skipped"}
     写 discovered + metrics.Inc("crawl.safety_url_skipped",1) + continue（不入队）
5. 表单（internal/crawl/forms.go）：SubmitPageForms 签名增加 safety *Safety；
   提交循环里 SkipForm 命中 → 记录 Discovery{Source:"form-skipped", URL:action}
   → continue（不提交）
6. 测试页（testdata/site.py）：新增 /safety.html——含改密码表单（字段名
   password_new/password_confirm）、删除用户表单（action 含 delete-user）、
   logout 链接、一个正常搜索表单
7. 单测 internal/crawl/safety_test.go：词表命中/正常不误伤（如 /catalogue、
   普通登录表单 password 单独不触发）/enabled=false 全放行/追加正则生效/非法正则报错
8. 端到端：爬 /safety.html，验证危险表单无 POST 流量记录、discovered 含
   form-skipped/url-skipped 条目、正常表单仍提交
9. scripts/verify.py 增加断言（GNU 契约 --flag 形式）

## 验收

- safety 单测全过；本地 e2e 危险操作零触发 + 发现记录完整
- scripts/verify.py --quick 全绿（基线 18 PASS 不回退）
- go vet 干净；git 提交推送后 CI 绿灯
- config.example.yaml/CHANGELOG 同步；回复中如实区分已验证/未验证
```

## 补充说明（给执行 Agent 的决策边界）

- **不做**：assetcrawler 侧不加 safety——其架构是「只报告，显式授权才行动」，
  危险口全部已有 opt-in 闸门，自动防护层属冗余（见 docs/对标分析.md）。
  **状态更新（2026-11-13）**：此论断已过时。assetcrawler 自 11-12 起默认自动
  填充并发送全部方法的测试请求，已实现自己的 safety 链；随后两边统一为
  直接调用公共包 `github.com/winezer0/basecrawler/safety`，本项目
  `internal/crawl/safety.go` 的重复实现已删除。本提示词保留为 F1 实施记录。
- **词表维护**：内置词表只收「明确改变目标状态」的操作类词；「password」单独
  不触发（普通登录表单含 password），必须命中改密/删除语义。
- **与只读约束的关系**：safety 不解决「只读」，它解决「自动提交的破坏性」——
  只读解锁后的主动探测（fuzz 等）属另一批次，与本功能正交。
- 若实施中发现词表误伤某真实站正常路径，走 `safety_url_skip` 不可行（那是追加
  拦截）——应修正内置词表或用 `-no-safety` 临时关闭并在报告中注明。
