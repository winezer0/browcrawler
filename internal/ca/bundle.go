package ca

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
)

// Bundle 生成 SSL_CERT_FILE 内容：始终包含本 CA，可选拼接一份显式指定的公共根 PEM。
//
// 修正说明（2026-11-08）：旧实现用 x509.SystemCertPool().Subjects() 拼 PEM。
// 该 API 返回的是「证书 subject 的 DER」，不是完整证书；且实测 Windows 上返回 0 条。
// 也就是说 merge_public_roots=true 过去是「看似生效、实际未合并」，在非 Windows 上
// 甚至可能把无效 DER 写进 bundle 导致 rustls 解析失败。现改为读取显式配置的 PEM 文件，
// 并逐块校验每个证书可解析，缺文件时由 config.Validate 在启动前明确报错。
func Bundle(crtPath, outPath, publicRootsFile string) (string, int, error) {
	crtPEM, err := os.ReadFile(crtPath)
	if err != nil {
		return "", 0, err
	}
	all := append([]byte(nil), crtPEM...)
	n := 0
	if publicRootsFile != "" {
		roots, err := os.ReadFile(publicRootsFile)
		if err != nil {
			return "", 0, fmt.Errorf("read public roots %s: %w", publicRootsFile, err)
		}
		count, clean, err := cleanCertPEM(roots)
		if err != nil {
			return "", 0, fmt.Errorf("invalid public roots %s: %w", publicRootsFile, err)
		}
		if count == 0 {
			return "", 0, fmt.Errorf("public roots %s contains no CERTIFICATE PEM block", publicRootsFile)
		}
		if !hasTrailingNewline(all) {
			all = append(all, '\n')
		}
		all = append(all, clean...)
		n = count
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return "", 0, err
	}
	if err := os.WriteFile(outPath, all, 0o644); err != nil {
		return "", 0, err
	}
	return outPath, n, nil
}

// cleanCertPEM 逐个校验并重新编码 CERTIFICATE 块，顺便去掉源文件里的注释文本，
// 保证交给 rustls/webpki 的 bundle 是纯 PEM。
func cleanCertPEM(pemData []byte) (int, []byte, error) {
	rest := pemData
	n := 0
	out := make([]byte, 0, len(pemData))
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		if _, err := x509.ParseCertificate(block.Bytes); err != nil {
			return 0, nil, fmt.Errorf("block %d: %w", n+1, err)
		}
		out = append(out, pem.EncodeToMemory(block)...)
		n++
	}
	return n, out, nil
}

func hasTrailingNewline(b []byte) bool {
	return len(b) > 0 && b[len(b)-1] == '\n'
}
