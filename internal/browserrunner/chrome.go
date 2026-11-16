package browserrunner

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/winezer0/browcrawler/internal/config"
)

// ChromeRunner 用 Chrome for Testing 便携版作为降级渲染后端。
//
// 与 Obscura 的差异（取舍）：
//   - Chrome 不读取 SSL_CERT_FILE，无法像 Obscura 那样注入 MITM CA；
//     因此用 --ignore-certificate-errors 跳过站点证书校验（上游 goproxy 仍正常校验
//     真实目标证书），保证降级链路不改变代理与录制代码。
//   - 每会话独立 --user-data-dir，退出时随会话临时目录一并删除。
type ChromeRunner struct {
	cfg     *config.Config
	id      string
	dir     string
	cdpPort int
	cmd     *exec.Cmd
	logFile *os.File
	wsAddr  string
	logger  *log.Logger

	mu       sync.Mutex
	waitDone chan struct{}
	exitErr  error
	closed   bool
}

func NewChrome(cfg *config.Config, id, dir string, logger *log.Logger) *ChromeRunner {
	return &ChromeRunner{cfg: cfg, id: id, dir: dir, logger: logger}
}

func (c *ChromeRunner) Start(_ context.Context, proxyAddr string) error {
	port, err := freePort()
	if err != nil {
		return err
	}
	c.cdpPort = port
	profile, err := filepath.Abs(filepath.Join(c.dir, "chrome-profile"))
	if err != nil {
		return fmt.Errorf("resolve chrome profile: %w", err)
	}
	if err := os.MkdirAll(profile, 0o755); err != nil {
		return err
	}
	args := []string{
		"--headless=new",
		"--remote-debugging-port=" + strconv.Itoa(port),
		"--remote-debugging-address=127.0.0.1",
		"--remote-allow-origins=*",
		"--user-data-dir=" + profile,
		"--proxy-server=" + proxyAddr,
		// Chrome 默认隐式绕过 loopback 代理（<-loopback 取消该绕过），否则本地/内网
		// 目标的请求不会经过 goproxy，录制会出现「页面渲染成功但流量为空」。
		"--proxy-bypass-list=<-loopback>",
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-background-networking",
		"--disable-component-update",
		"--disable-domain-reliability",
		"--disable-sync",
		"--disable-default-apps",
		"--disable-breakpad",
		"--no-pings",
		"--metrics-recording-only",
		"--disable-extensions",
		"--disable-component-extensions-with-background-pages",
		"--disable-features=AutofillServerCommunication,OptimizationHints,MediaRouter,Translate,CertificateTransparencyComponentUpdater,ChromeVariations",
		"--disable-gpu",
		"--ignore-certificate-errors",
		"--window-size=1280,800",
		"about:blank",
	}
	cmd := exec.Command(c.cfg.ChromeBin, args...)
	cmd.Env = chromeEnv()
	platformSetup(cmd)
	logFile, err := os.Create(filepath.Join(c.dir, "chrome.log"))
	if err != nil {
		return err
	}
	c.logFile = logFile
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start chrome: %w", err)
	}
	c.cmd = cmd
	c.waitDone = make(chan struct{})
	go func() {
		e := cmd.Wait()
		c.mu.Lock()
		c.exitErr = e
		c.mu.Unlock()
		close(c.waitDone)
	}()
	c.logger.Printf("[chrome] pid=%d port=%d bin=%s", cmd.Process.Pid, port, c.cfg.ChromeBin)

	deadline := time.Now().Add(c.cfg.Timeout.CDPReady())
	client := &http.Client{
		Timeout:   2 * time.Second,
		Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true},
	}
	for time.Now().Before(deadline) {
		select {
		case <-c.waitDone:
			return fmt.Errorf("chrome exited before CDP ready: %v; log: %s", c.err(), tailFile(filepath.Join(c.dir, "chrome.log")))
		default:
		}
		ver, err := probeCDP(client, port)
		if err == nil {
			c.wsAddr = ver
			c.logger.Printf("[chrome] CDP ready %s", ver)
			return nil
		}
		time.Sleep(120 * time.Millisecond)
	}
	_ = c.Shutdown(context.Background())
	return fmt.Errorf("chrome CDP not ready within %s; log: %s",
		c.cfg.Timeout.CDPReady(), tailFile(filepath.Join(c.dir, "chrome.log")))
}

func (c *ChromeRunner) WSAddr() string { return c.wsAddr }

func (c *ChromeRunner) Healthy() bool {
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed || c.wsAddr == "" || c.waitDone == nil {
		return false
	}
	select {
	case <-c.waitDone:
		return false
	default:
	}
	client := &http.Client{Timeout: 500 * time.Millisecond, Transport: &http.Transport{Proxy: nil}}
	_, err := probeCDP(client, c.cdpPort)
	return err == nil
}

func (c *ChromeRunner) Shutdown(ctx context.Context) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()
	if c.cmd == nil || c.cmd.Process == nil {
		c.cleanup()
		return nil
	}
	_ = sendBrowserClose(c.wsAddr)
	if err := platformGracefulSignal(c.cmd.Process.Pid); err != nil {
		c.logger.Printf("[chrome] graceful signal failed: %v", err)
	}
	grace := min(c.cfg.Timeout.Shutdown(), 3*time.Second)
	select {
	case <-c.waitDone:
		c.logger.Printf("[chrome] exited gracefully")
		c.cleanup()
		return nil
	case <-time.After(grace):
	case <-ctx.Done():
	}
	c.logger.Printf("[chrome] grace timeout, force kill pid=%d", c.cmd.Process.Pid)
	if runtime.GOOS == "windows" {
		_ = exec.Command("taskkill", "/PID", strconv.Itoa(c.cmd.Process.Pid), "/T", "/F").Run()
	}
	_ = c.cmd.Process.Kill()
	select {
	case <-c.waitDone:
	case <-time.After(3 * time.Second):
		c.logger.Printf("[chrome] WARNING process still not reaped")
	}
	c.cleanup()
	return nil
}

func (c *ChromeRunner) cleanup() {
	if c.logFile != nil {
		_ = c.logFile.Close()
	}
}

func (c *ChromeRunner) err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.exitErr
}

// probeCDP 是 Obscura / Chrome 共用的就绪探测（/json/version）。
func probeCDP(client *http.Client, port int) (string, error) {
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/json/version", port))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("CDP /json/version status %d", resp.StatusCode)
	}
	var v struct {
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return "", err
	}
	if v.WebSocketDebuggerURL == "" {
		return "", fmt.Errorf("empty webSocketDebuggerUrl")
	}
	return v.WebSocketDebuggerURL, nil
}

func tailFile(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return "(no log)"
	}
	s := string(b)
	if len(s) > 800 {
		s = s[len(s)-800:]
	}
	return strings.ReplaceAll(s, "\n", " | ")
}

// chromeEnv 清理宿主注入的代理变量，避免 Chrome 绕过本会话 goproxy。
func chromeEnv() []string {
	drop := map[string]bool{
		"HTTP_PROXY": true, "HTTPS_PROXY": true, "ALL_PROXY": true,
		"http_proxy": true, "https_proxy": true, "all_proxy": true,
		"NO_PROXY": true, "no_proxy": true,
		"SSL_CERT_FILE": true, "SSL_CERT_DIR": true,
	}
	env := make([]string, 0, len(os.Environ()))
	for _, e := range os.Environ() {
		if k := strings.SplitN(e, "=", 2)[0]; !drop[k] {
			env = append(env, e)
		}
	}
	return env
}
