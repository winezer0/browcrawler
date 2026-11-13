// Package ca 生成/加载 MITM 根 CA，并产出供 Obscura 信任的 SSL_CERT_FILE bundle。
package ca

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

// Ensure 确保 CA 文件存在，返回可用于签发站点证书的 tls.Certificate。
func Ensure(crtPath, keyPath string) (*tls.Certificate, error) {
	if err := os.MkdirAll(filepath.Dir(crtPath), 0o755); err != nil {
		return nil, err
	}
	if _, err := os.Stat(crtPath); err == nil {
		if _, err2 := os.Stat(keyPath); err2 == nil {
			pair, err := tls.LoadX509KeyPair(crtPath, keyPath)
			if err != nil {
				return nil, fmt.Errorf("load CA keypair: %w", err)
			}
			return &pair, nil
		}
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   "browcrawler MITM CA",
			Organization: []string{"browcrawler"},
		},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		SignatureAlgorithm:    x509.SHA256WithRSA,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	crtPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	if err := os.WriteFile(crtPath, crtPEM, 0o644); err != nil {
		return nil, err
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return nil, err
	}
	pair, err := tls.X509KeyPair(crtPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	return &pair, nil
}
