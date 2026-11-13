package browserrunner

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/winezer0/browcrawler/internal/config"
)

func TestBuildObscuraArgs(t *testing.T) {
	args := buildObscuraArgs(9222, "http://127.0.0.1:5000", "/tmp/profile", true)
	got := strings.Join(args, " ")
	for _, want := range []string{"serve", "--port 9222", "--proxy http://127.0.0.1:5000",
		"--storage-dir /tmp/profile", "--stealth"} {
		if !strings.Contains(got, want) {
			t.Fatalf("args 缺 %q: %v", want, args)
		}
	}
	// robots.txt 由爬虫层实现，不传给 Obscura（v0.2.6 serve 模式不支持）
	if strings.Contains(got, "robots") {
		t.Fatal("不应传 --obey-robots（serve 模式无效）")
	}
	// 非 stealth 不含该参数
	args2 := buildObscuraArgs(1, "p", "d", false)
	if strings.Contains(strings.Join(args2, " "), "--stealth") {
		t.Fatal("stealth=false 不应含 --stealth")
	}
}

func TestBuildEnvDropsProxyAndInjectsCA(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://leak:1")
	t.Setenv("HTTPS_PROXY", "http://leak:2")
	t.Setenv("SSL_CERT_FILE", "C://host//ca.pem")
	cfg := config.Default()
	cfg.Env = map[string]string{"OBSCURA_ALLOW_PRIVATE_NETWORK": "1"}
	env := buildEnv(cfg, "bundle.pem")
	joined := strings.Join(env, "\n")
	if strings.Contains(joined, "HTTP_PROXY") || strings.Contains(joined, "SSL_CERT_FILE=C:") {
		t.Fatal("代理与宿主 CA 变量未被清理")
	}
	if !strings.Contains(joined, "SSL_CERT_FILE=bundle.pem") {
		t.Fatal("未注入 MITM CA")
	}
	if !strings.Contains(joined, "OBSCURA_ALLOW_PRIVATE_NETWORK=1") {
		t.Fatal("配置 env 未透传")
	}
	if !strings.Contains(joined, "OBSCURA_BLOCK_TRACKERS=0") {
		t.Fatal("只读采集需关闭 tracker 拦截")
	}
}

func TestProbeCDP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/json/version" {
			w.WriteHeader(404)
			return
		}
		_, _ = w.Write([]byte(`{"webSocketDebuggerURL":"ws://127.0.0.1:1/devtools/browser"}`))
	}))
	defer srv.Close()
	client := srv.Client()
	port := portOf(t, srv.URL)
	ws, err := probeCDP(client, port)
	if err != nil || ws != "ws://127.0.0.1:1/devtools/browser" {
		t.Fatalf("probeCDP = (%q, %v)", ws, err)
	}
	// 非 /json/version 路径的端点必须失败（不误认任意 200）
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer bad.Close()
	if _, err := probeCDP(bad.Client(), portOf(t, bad.URL)); err == nil {
		t.Fatal("缺少 webSocketDebuggerURL 的响应不应被视为 CDP ready")
	}
}

func TestTailFileTruncates(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "log")
	long := strings.Repeat("x", 2000)
	if err := os.WriteFile(p, []byte(long), 0o644); err != nil {
		t.Fatal(err)
	}
	got := tailFile(p)
	if len(got) > 900 || !strings.Contains(got, strings.Repeat("x", 100)) {
		t.Fatalf("tailFile 应截断保留尾部: len=%d", len(got))
	}
	if tailFile(filepath.Join(dir, "missing")) != "(no log)" {
		t.Fatal("缺失文件应返回 (no log)")
	}
}

func TestFreePortUniqueEnough(t *testing.T) {
	a, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	b, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatalf("两次 freePort 不应返回同端口 %d", a)
	}
}

func TestObscuraShutdownIdempotentWithoutProcess(t *testing.T) {
	cfg := config.Default()
	cfg.ObscuraBin = "definitely-missing-binary"
	o := NewObscura(cfg, "s-t", t.TempDir(), "bundle.pem", testLogger())
	// 未 Start（cmd==nil）时 Shutdown 应安全且幂等
	if err := o.Shutdown(t.Context()); err != nil {
		t.Fatalf("Shutdown(nil cmd) error = %v", err)
	}
	if err := o.Shutdown(t.Context()); err != nil {
		t.Fatalf("Shutdown twice error = %v", err)
	}
	// Healthy：未启动时必须 false
	if o.Healthy() {
		t.Fatal("未启动的 runner 不应 Healthy")
	}
}

func testLogger() *log.Logger { return log.New(io.Discard, "", 0) }

// portOf 从 httptest server URL 提取端口（probeCDP 以端口号为参数）。
func portOf(t *testing.T, serverURL string) int {
	t.Helper()
	idx := strings.LastIndex(serverURL, ":")
	if idx < 0 {
		t.Fatalf("bad server url %q", serverURL)
	}
	var port int
	for _, c := range serverURL[idx+1:] {
		if c < '0' || c > '9' {
			break
		}
		port = port*10 + int(c-'0')
	}
	if port == 0 {
		t.Fatalf("bad port in %q", serverURL)
	}
	_ = time.Second
	return port
}
