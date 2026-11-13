package crawl

import (
	"testing"

	"github.com/winezer0/basecrawler/safety"
)

func TestSafetyDefaults(t *testing.T) {
	s, err := safety.NewSafety(true, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	// URL 词表：登出类命中
	for _, u := range []string{
		"http://t/logout", "http://t/signout?x=1", "http://t/sign-out",
		"http://t/user/exit", "http://t/app/quit", "http://t/用户/登出",
	} {
		if !s.SkipURL(u) {
			t.Fatalf("应跳过: %s", u)
		}
	}
	// 正常 URL 不误伤（catalogue 含 "cat" 无关；exit 子串不受词边界影响须实测）
	for _, u := range []string{"http://t/normal-page", "http://t/catalogue", "http://t/exhibit"} {
		if s.SkipURL(u) {
			t.Fatalf("不应误伤: %s", u)
		}
	}
	// 表单词表：改密码（字段名联合匹配）+ 删除类 action
	if !s.SkipForm("http://t/account", []string{"password_new", "password_confirm"}) {
		t.Fatal("改密码表单应跳过")
	}
	if !s.SkipForm("http://t/admin/delete-user", []string{"confirm"}) {
		t.Fatal("删除类 action 应跳过")
	}
	if !s.SkipForm("http://t/user/remove", []string{"remove_account"}) {
		t.Fatal("删除类字段应跳过")
	}
	// 普通登录表单：password 单独不触发（关键防误伤点）
	if s.SkipForm("http://t/login", []string{"username", "password"}) {
		t.Fatal("普通登录表单不应命中")
	}
	if s.SkipForm("http://t/search", []string{"q"}) {
		t.Fatal("搜索表单不应命中")
	}
	// 关闭后全放行
	off, _ := safety.NewSafety(false, nil, nil)
	if off.SkipURL("http://t/logout") || off.SkipForm("http://t/x", []string{"password_new"}) {
		t.Fatal("关闭后不应拦截")
	}
}

func TestSafetyExtraRules(t *testing.T) {
	s, err := safety.NewSafety(true, []string{`/vip/secret`}, []string{`transfer_?money`})
	if err != nil {
		t.Fatal(err)
	}
	if !s.SkipURL("http://t/vip/secret/panel") {
		t.Fatal("追加 URL 正则未生效")
	}
	if !s.SkipForm("http://t/pay", []string{"transfer_money"}) {
		t.Fatal("追加表单正则未生效")
	}
	// 非法正则报错（启动前失败）
	if _, err := safety.NewSafety(true, []string{"((( "}, nil); err == nil {
		t.Fatal("非法 URL 正则应报错")
	}
	if _, err := safety.NewSafety(true, nil, []string{"*bad"}); err == nil {
		t.Fatal("非法表单正则应报错")
	}
}
