package crawl

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/go-rod/rod/lib/proto"
)

// Credentials 是可选的真实登录凭据；存在时优先于测试字典填充 user/pass 类字段。
type Credentials struct {
	User string
	Pass string
}

// AuthApplier 是可选的认证态注入能力：导航前把静态 Cookie/Headers 注入浏览器。
// RodDriver 实现它；auth 态属于会话级，由编排层在 Run 之前调用。
type AuthApplier interface {
	ApplyAuth(ctx context.Context, headers http.Header, targetURL string) error
}

// ParseAuthFile 解析认证文件：每行一个 `Header-Name: value`，空行与 # 注释忽略。
// 典型内容：Cookie: session=xxx 或 X-API-KEY: xxx。
func ParseAuthFile(path string) (http.Header, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	header := http.Header{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("auth file %s: invalid line %q", path, line)
		}
		header.Add(strings.TrimSpace(name), strings.TrimSpace(value))
	}
	if len(header) == 0 {
		return nil, fmt.Errorf("auth file %s: no headers", path)
	}
	return header, nil
}

// ApplyAuth 注入认证态：Cookie 行解析为浏览器 Cookie（域绑定目标主机），
// 其余头通过 CDP 附加到后续所有请求；goproxy 录制到的请求即带认证态。
func (d *RodDriver) ApplyAuth(ctx context.Context, headers http.Header, targetURL string) error {
	target, err := url.Parse(targetURL)
	if err != nil {
		return err
	}
	host := target.Hostname()
	var cookies []*proto.NetworkCookieParam
	var extraPairs []string
	for name, values := range headers {
		for _, value := range values {
			if strings.EqualFold(name, "Cookie") {
				for _, pair := range strings.Split(value, ";") {
					cookieName, cookieValue, ok := strings.Cut(strings.TrimSpace(pair), "=")
					if !ok || cookieName == "" {
						continue
					}
					cookies = append(cookies, &proto.NetworkCookieParam{
						Name: cookieName, Value: cookieValue, Domain: host, Path: "/",
					})
				}
				continue
			}
			extraPairs = append(extraPairs, name, value)
		}
	}
	p, cancel := d.scoped(ctx)
	defer cancel()
	if len(cookies) > 0 {
		if err := p.SetCookies(cookies); err != nil {
			return fmt.Errorf("set auth cookies: %w", err)
		}
	}
	if len(extraPairs) > 0 {
		if _, err := p.SetExtraHeaders(extraPairs); err != nil {
			return fmt.Errorf("set extra auth headers: %w", err)
		}
	}
	return nil
}
