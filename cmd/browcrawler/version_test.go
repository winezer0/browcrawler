package main

import (
	"strings"
	"testing"
)

func TestVersionStringDefault(t *testing.T) {
	// 未注入时 version 保持 dev；git 仓库内构建应带 vcs.time，
	// 无仓库环境输出 unknown——两种都以 (commit 前缀校验格式。
	v := VersionString()
	if !strings.HasPrefix(v, "dev (commit ") || !strings.HasSuffix(v, ")") {
		t.Fatalf("VersionString() = %q, want dev (commit ...)", v)
	}
}

func TestVersionStringInjected(t *testing.T) {
	old := version
	defer func() { version = old }()
	version = "v9.9.9"
	if got := VersionString(); !strings.HasPrefix(got, "v9.9.9 (commit ") {
		t.Fatalf("VersionString() = %q, want v9.9.9 (commit ...)", got)
	}
}

func TestVCSFormat(t *testing.T) {
	// vcsTime 只允许真实 ISO 时间或 unknown。
	v := vcsTime()
	if v == "unknown" {
		return
	}
	if !strings.Contains(v, "T") || !strings.Contains(v, "Z") && !strings.Contains(v, "+") {
		t.Fatalf("vcsTime() = %q, want ISO8601 time", v)
	}
}
