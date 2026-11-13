package ca

import (
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBundleMergesRealCertificates(t *testing.T) {
	dir := t.TempDir()
	ownCrt, ownKey := filepath.Join(dir, "ca.crt"), filepath.Join(dir, "ca.key")
	if _, err := Ensure(ownCrt, ownKey); err != nil {
		t.Fatal(err)
	}
	// 用第二张自签证书充当「公共根」，并夹带注释文本，验证清洗逻辑。
	otherDir := filepath.Join(dir, "other")
	if _, err := Ensure(filepath.Join(otherDir, "r.crt"), filepath.Join(otherDir, "r.key")); err != nil {
		t.Fatal(err)
	}
	rootPEM, err := os.ReadFile(filepath.Join(otherDir, "r.crt"))
	if err != nil {
		t.Fatal(err)
	}
	roots := filepath.Join(dir, "roots.pem")
	if err := os.WriteFile(roots, append([]byte("## comment line\nnot a pem\n"), rootPEM...), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "bundle.pem")
	path, n, err := Bundle(ownCrt, out, roots)
	if err != nil {
		t.Fatalf("Bundle: %v", err)
	}
	if path != out || n != 1 {
		t.Fatalf("path=%s n=%d, 期望 %s / 1", path, n, out)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "## comment") {
		t.Fatal("bundle 中不应残留源文件的注释文本")
	}
	count := 0
	rest := data
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if _, err := x509.ParseCertificate(block.Bytes); err != nil {
			t.Fatalf("bundle 第 %d 块不是有效证书: %v", count+1, err)
		}
		count++
	}
	if count != 2 {
		t.Fatalf("bundle 应有 2 张证书（自签 CA + 1 公共根），实际 %d", count)
	}
}

func TestBundleRejectsBrokenRoots(t *testing.T) {
	dir := t.TempDir()
	ownCrt, ownKey := filepath.Join(dir, "ca.crt"), filepath.Join(dir, "ca.key")
	if _, err := Ensure(ownCrt, ownKey); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(dir, "bad.pem")
	// 旧实现的典型错误：把 subject DER 当成完整 CERTIFICATE 写进 bundle。
	if err := os.WriteFile(bad, []byte("-----BEGIN CERTIFICATE-----\nZm9v\n-----END CERTIFICATE-----\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Bundle(ownCrt, filepath.Join(dir, "out.pem"), bad); err == nil {
		t.Fatal("非法公共根必须报错，不能静默产出坏 bundle")
	}
}

func TestProviderPerSessionCleansKey(t *testing.T) {
	dir := t.TempDir()
	p := NewProvider(Options{Dir: dir, PerSession: true})
	cert, err := p.Cert()
	if err != nil || cert == nil {
		t.Fatalf("Cert: %v", err)
	}
	bundle, err := p.BundlePath()
	if err != nil || bundle == "" {
		t.Fatalf("BundlePath: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "ca.key")); err != nil {
		t.Fatalf("会话级私钥应存在: %v", err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "ca.key")); !os.IsNotExist(err) {
		t.Fatalf("Close 后会话级私钥应被删除，实际 %v", err)
	}
	// bundle 仍在会话目录，由 Session.Shutdown 统一删除整个目录。
	if _, err := os.Stat(bundle); err != nil {
		t.Fatalf("bundle 不应被 Provider.Close 删除: %v", err)
	}
}
