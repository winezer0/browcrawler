package crawl

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"strings"
	"time"

	"github.com/winezer0/basecrawler/safety"
)

// FormSubmitter 是可选的表单驱动能力（参考 crawlergo：自动填充 + 提交触发请求）。
// PageDriver 实现了它就自动获得表单提交能力；否则爬虫退化为纯链接提取。
type FormSubmitter interface {
	CollectForms(ctx context.Context) ([]FormInfo, error)
	FillAndSubmit(ctx context.Context, index int, values map[string]string) (string, error)
}

// FormInfo 是页面上一张表单的元数据（由浏览器端 JS 收集，不含敏感值）。
type FormInfo struct {
	Index  int         `json:"index"`
	Action string      `json:"action"`
	Method string      `json:"method"`
	Fields []FormField `json:"fields"`
}

type FormField struct {
	Name    string   `json:"name"`
	ID      string   `json:"id"`
	Tag     string   `json:"tag"`
	Type    string   `json:"type"`
	Hidden  bool     `json:"hidden"`
	Options []string `json:"options,omitempty"`
}

// collectFormsJS 收集每张表单的 action/method/字段清单（hidden 仅记录不填，
// 避免 CSRF token 被覆盖导致提交必然失败）。
// 注意 rod 的 Eval 会把 JS 包装为 `(fn).apply(this, args)` 调用，
// 因此这里必须是「函数定义」而非求值表达式。
const collectFormsJS = `function() {
  return Array.from(document.querySelectorAll('form')).map((f,i)=>({
    index:i,
    action:f.action||location.href,
    method:(f.method||'get').toUpperCase(),
    fields:Array.from(f.querySelectorAll('input,select,textarea')).map(el=>({
      name:el.name||'', id:el.id||'', tag:el.tagName.toLowerCase(),
      type:el.tagName.toLowerCase()==='input'?(el.type||'text'):el.tagName.toLowerCase(),
      hidden:el.type==='hidden',
      options:el.tagName==='SELECT'?Array.from(el.options).slice(0,3).map(o=>o.value):null
    }))
  }));
}`

// fillSubmitJS 填充指定表单并触发提交；checkbox/radio 勾选，其余赋值。
// React/Vue 等框架监听事件而非 property，因此同步派发 input/change 事件。
// 参数 (fidx, vals) 由 Go 侧通过 rod Eval 参数传入，避免字符串拼接注入。
const fillSubmitJS = `function(fidx, vals) {
  var f = document.querySelectorAll('form')[fidx];
  if (!f) return 'missing';
  for (var name in vals) {
    var el = f.elements[name];
    if (!el) continue;
    if (el.type === 'checkbox' || el.type === 'radio') { el.checked = true; }
    else { el.value = vals[name]; }
    el.dispatchEvent(new Event('input', {bubbles:true}));
    el.dispatchEvent(new Event('change', {bubbles:true}));
  }
  var btn = f.querySelector('button[type=submit], input[type=submit], button:not([type])');
  if (btn) { btn.click(); return 'clicked'; }
  if (f.requestSubmit) { f.requestSubmit(); return 'requested'; }
  f.submit(); return 'submitted';
}`

// BuildFormValues 按 crawlergo 思路生成填充字典：命中常见字段名/类型用对应测试值，
// select 取第一个 option，checkbox/radio 由 JS 勾选（值为空串占位）。
// creds 非空时，user/pass 类字段使用真实凭据（登录态获取）。
func BuildFormValues(f FormInfo, creds *Credentials) map[string]string {
	m := map[string]string{}
	for _, fl := range f.Fields {
		if fl.Hidden || fl.Name == "" {
			continue
		}
		switch fl.Type {
		case "submit", "button", "image", "file", "reset":
			continue
		case "checkbox", "radio":
			m[fl.Name] = ""
			continue
		}
		m[fl.Name] = pickValue(fl, creds)
	}
	return m
}

func pickValue(fl FormField, creds *Credentials) string {
	if fl.Type == "select" && len(fl.Options) > 0 {
		for _, o := range fl.Options {
			if o != "" {
				return o
			}
		}
	}
	name := strings.ToLower(fl.Name + " " + fl.ID)
	if creds != nil {
		if containsAny(name, "user", "login", "account", "uname", "username") && creds.User != "" {
			return creds.User
		}
		if containsAny(name, "pass", "passwd", "pwd") && creds.Pass != "" {
			return creds.Pass
		}
	}
	switch fl.Type {
	case "email":
		return "test@example.com"
	case "password":
		return "123456"
	case "number", "range":
		return "123"
	case "date":
		return "2024-01-01"
	case "tel":
		return "13800138000"
	case "url":
		return "http://127.0.0.1/"
	}
	switch {
	case containsAny(name, "user", "login", "account", "uname", "username", "nickname"):
		return "admin"
	case containsAny(name, "email", "mail"):
		return "test@example.com"
	case containsAny(name, "pass", "passwd", "pwd"):
		return "123456"
	case containsAny(name, "phone", "mobile", "tel"):
		return "13800138000"
	case containsAny(name, "search", "query", "keyword", "s_", "kw"):
		return "test"
	case containsAny(name, "captcha", "verify", "vcode"):
		return "1234"
	case containsAny(name, "comment", "content", "desc", "msg", "message"):
		return "test"
	case containsAny(name, "name"):
		return "test"
	default:
		return "test"
	}
}

func containsAny(s string, keys ...string) bool {
	for _, k := range keys {
		if strings.Contains(s, k) {
			return true
		}
	}
	return false
}

// SubmitPageForms 收集当前页的表单并逐一填充提交；返回提交记录（写入 discovered）
// 与提交后响应页里提取到的链接。提交产生的真实请求由会话 goproxy 完整录制。
func SubmitPageForms(ctx context.Context, submitter FormSubmitter, pageURL string,
	settle time.Duration, credentials *Credentials, safetyFilter *safety.Safety, logger *log.Logger) (records, postLinks []Discovery) {
	forms, err := submitter.CollectForms(ctx)
	if err != nil {
		logger.Printf("[form] collect failed: %s: %v", pageURL, err)
		return nil, nil
	}
	for _, f := range forms {
		if ctx.Err() != nil {
			return records, postLinks
		}
		values := BuildFormValues(f, credentials)
		if len(values) == 0 && f.Method == "GET" {
			continue // 无可填字段的 GET 表单与直接访问等价，跳过避免噪声
		}
		// safety：危险表单（改密码/删除类）不真实提交，但记录为发现
		fieldNames := make([]string, 0, len(f.Fields))
		for _, fl := range f.Fields {
			fieldNames = append(fieldNames, fl.Name, fl.ID)
		}
		if safetyFilter != nil && safetyFilter.SkipForm(f.Action, fieldNames) {
			logger.Printf("[safety] form skipped: %s %s", f.Method, f.Action)
			records = append(records, Discovery{
				URL: f.Action, Source: "form-skipped", From: pageURL,
			})
			continue
		}
		how, err := submitter.FillAndSubmit(ctx, f.Index, values)
		if err != nil {
			// 提交触发导航可能销毁执行上下文，这通常意味着提交已生效。
			if isContextDestroyed(err) {
				how = "navigated"
			} else {
				logger.Printf("[form] submit failed: %s: %v", f.Action, err)
				continue
			}
		}
		logger.Printf("[form] submitted %s %s (%s)", f.Method, f.Action, how)
		fields := make([]string, 0, len(values))
		for name := range values {
			fields = append(fields, name)
		}
		records = append(records, Discovery{
			URL:    absoluteFormURL(pageURL, f.Action),
			Source: "form-submitted:fields=" + strings.Join(fields, ","),
			From:   pageURL,
			Method: f.Method,
		})
		wait := settle
		if wait <= 0 {
			wait = 800 * time.Millisecond // 提交导航到响应页的最短等待
		}
		select {
		case <-ctx.Done():
			return records, postLinks
		case <-time.After(wait):
		}
		if doc, err := submitterHTML(ctx, submitter); err == nil && doc != "" {
			for _, d := range ExtractLinks(pageURL, doc) {
				if d.URL != normalize(pageURL) {
					postLinks = append(postLinks, d)
				}
			}
		}
	}
	return records, postLinks
}

// submitterHTML 通过可选中转拿提交后页面的 DOM：FormSubmitter 由 RodDriver 实现，
// 同时 RodDriver 也是 PageDriver，这里做一次受控类型断言。
func submitterHTML(ctx context.Context, s FormSubmitter) (string, error) {
	if pd, ok := s.(PageDriver); ok {
		return pd.HTML(ctx)
	}
	return "", fmt.Errorf("driver does not expose HTML")
}

func isContextDestroyed(err error) bool {
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "destroyed") || strings.Contains(s, "navigated") ||
		strings.Contains(s, "cannot find context")
}

func absoluteFormURL(pageURL, action string) string {
	if action == "" {
		return pageURL
	}
	base, err := url.Parse(pageURL)
	if err != nil {
		return action
	}
	u, err := url.Parse(action)
	if err != nil {
		return action
	}
	return base.ResolveReference(u).String()
}
