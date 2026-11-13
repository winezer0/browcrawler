package proxyrunner

import (
	"crypto/tls"
	"testing"
)

func emptyRunner(hosts []string) *Runner {
	return New(tls.Certificate{}, nil, "", nil, hosts)
}

func TestShouldPassthrough(t *testing.T) {
	r := emptyRunner([]string{"pinned.example.com", "*.cdn.test"})
	for _, host := range []string{"pinned.example.com", "pinned.example.com:443", "a.cdn.test", "a.cdn.test:8443"} {
		if !r.shouldPassthrough(host) {
			t.Errorf("%q 应命中 passthrough", host)
		}
	}
	for _, host := range []string{"other.com", "cdn.test", "notpinned.example.com", "pinned.example.com.evil"} {
		if r.shouldPassthrough(host) {
			t.Errorf("%q 不应命中 passthrough", host)
		}
	}
}

func TestShouldPassthroughEmptyByDefault(t *testing.T) {
	r := emptyRunner(nil)
	if r.shouldPassthrough("anything.com") {
		t.Fatal("默认必须全量 MITM")
	}
}

func TestIsLocal(t *testing.T) {
	for _, h := range []string{"localhost", "127.0.0.1", "10.1.2.3", "172.16.5.4", "192.168.1.1", "169.254.1.1", "::1", "fd00::1", "svc.local"} {
		if !isLocal(h) {
			t.Errorf("%q 应判定为内网/回环", h)
		}
	}
	for _, h := range []string{"example.com", "93.184.216.34", "8.8.8.8"} {
		if isLocal(h) {
			t.Errorf("%q 不应判定为内网", h)
		}
	}
}
