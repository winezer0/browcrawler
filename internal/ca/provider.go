package ca

import (
	"crypto/tls"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Provider 抽象「一张 MITM 根 CA + 一份给浏览器信任的 PEM bundle」。
// Session 只依赖此接口，可在测试里注入假实现，也可切换全局 CA / 会话级临时 CA。
type Provider interface {
	Cert() (*tls.Certificate, error)
	BundlePath() (string, error)
	Close() error
}

// Options 是 Provider 的纯数据入参，刻意不引用 config 包，保持底层包可独立测试。
type Options struct {
	CrtPath         string // 全局 CA 路径；PerSession=true 时忽略，改用 Dir 内路径
	KeyPath         string
	Dir             string // 会话目录（bundle 与临时 CA 均落在此处）
	PublicRootsFile string // 非空时合并到 bundle
	PerSession      bool   // 每会话生成独立 CA，Close 时删除
}

// FileProvider 惰性生成并缓存 CA 与 bundle；并发安全、幂等。
type FileProvider struct {
	o      Options
	once   sync.Once
	cert   *tls.Certificate
	bundle string
	err    error
}

func NewProvider(o Options) Provider {
	return &FileProvider{o: o}
}

func (p *FileProvider) ensure() {
	p.once.Do(func() {
		crt, key := p.o.CrtPath, p.o.KeyPath
		if p.o.PerSession {
			crt = filepath.Join(p.o.Dir, "ca.crt")
			key = filepath.Join(p.o.Dir, "ca.key")
		}
		p.cert, p.err = Ensure(crt, key)
		if p.err != nil {
			p.err = fmt.Errorf("ensure CA: %w", p.err)
			return
		}
		bundleOut := filepath.Join(p.o.Dir, "ca-bundle.pem")
		path, _, err := Bundle(crt, bundleOut, p.o.PublicRootsFile)
		if err != nil {
			p.err = fmt.Errorf("build CA bundle: %w", err)
			return
		}
		p.bundle = path
	})
}

func (p *FileProvider) Cert() (*tls.Certificate, error) {
	p.ensure()
	return p.cert, p.err
}

func (p *FileProvider) BundlePath() (string, error) {
	p.ensure()
	return p.bundle, p.err
}

func (p *FileProvider) Close() error {
	if !p.o.PerSession {
		return nil
	}
	var first error
	for _, f := range []string{filepath.Join(p.o.Dir, "ca.crt"), filepath.Join(p.o.Dir, "ca.key")} {
		if err := os.Remove(f); err != nil && !os.IsNotExist(err) && first == nil {
			first = err
		}
	}
	return first
}
