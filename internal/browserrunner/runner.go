// Package browserrunner 管理 Obscura 进程生命周期与 CDP 就绪探测。
//
// Windows 适配说明：设计文档里的 SIGTERM→SIGKILL 在 Windows 上不可用
// （os.Process.Signal(os.Interrupt/SIGTERM) 返回 not supported）。这里改为：
//
//	CDP Browser.close → CTRL_BREAK 进程组（CREATE_NEW_PROCESS_GROUP）→ 等待 clean exit
//	→ taskkill /T /F 进程树强杀（兜底，含 worker）。
package browserrunner

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"browcrawler/internal/config"
)

type Runner interface {
	Start(ctx context.Context, proxyAddr string) error
	Shutdown(ctx context.Context) error
	WSAddr() string
	Healthy() bool
}

type ObscuraRunner struct {
	cfg        *config.Config
	id         string
	dir        string
	bundlePath string
	cdpPort    int
	cmd        *exec.Cmd
	logFile    *os.File
	wsAddr     string
	logger     *log.Logger

	mu       sync.Mutex
	waitDone chan struct{}
	exitErr  error
	closed   bool
}

func NewObscura(cfg *config.Config, id, dir, bundlePath string, logger *log.Logger) *ObscuraRunner {
	return &ObscuraRunner{cfg: cfg, id: id, dir: dir, bundlePath: bundlePath, logger: logger}
}

func (o *ObscuraRunner) Start(_ context.Context, proxyAddr string) error {
	port, err := freePort()
	if err != nil {
		return err
	}
	o.cdpPort = port
	if err := os.MkdirAll(filepath.Join(o.dir, "profile"), 0o755); err != nil {
		return err
	}
	args := buildObscuraArgs(port, proxyAddr, filepath.Join(o.dir, "profile"), o.cfg.Stealth)
	cmd := exec.Command(o.cfg.ObscuraBin, args...)
	cmd.Env = buildEnv(o.cfg, o.bundlePath)
	platformSetup(cmd)
	logFile, err := os.Create(filepath.Join(o.dir, "obscura.log"))
	if err != nil {
		return err
	}
	o.logFile = logFile
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start obscura: %w", err)
	}
	o.cmd = cmd
	o.waitDone = make(chan struct{})
	// 必须 goroutine 执行 Wait，回收进程资源、避免僵尸。
	go func() {
		err := cmd.Wait()
		o.mu.Lock()
		o.exitErr = err
		o.mu.Unlock()
		close(o.waitDone)
	}()
	o.logger.Printf("[obscura] pid=%d port=%d args=%v", cmd.Process.Pid, port, args)

	deadline := time.Now().Add(o.cfg.Timeout.CDPReady())
	client := &http.Client{
		Timeout:   2 * time.Second,
		Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true},
	}
	for time.Now().Before(deadline) {
		select {
		case <-o.waitDone:
			return fmt.Errorf("obscura exited before CDP ready: %v; log: %s", o.err(), o.tailLog())
		default:
		}
		resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/json/version", port))
		if err == nil {
			var v struct {
				WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
			}
			decErr := json.NewDecoder(resp.Body).Decode(&v)
			_ = resp.Body.Close()
			if decErr == nil && v.WebSocketDebuggerURL != "" {
				o.wsAddr = v.WebSocketDebuggerURL
				o.logger.Printf("[obscura] CDP ready %s", o.wsAddr)
				return nil
			}
		}
		time.Sleep(120 * time.Millisecond)
	}
	_ = o.Shutdown(context.Background())
	return fmt.Errorf("CDP not ready within %s; log: %s", o.cfg.Timeout.CDPReady(), o.tailLog())
}

func (o *ObscuraRunner) WSAddr() string { return o.wsAddr }

func (o *ObscuraRunner) Healthy() bool {
	o.mu.Lock()
	closed := o.closed
	o.mu.Unlock()
	if closed || o.wsAddr == "" || o.waitDone == nil {
		return false
	}
	select {
	case <-o.waitDone:
		return false
	default:
	}
	client := &http.Client{
		Timeout:   500 * time.Millisecond,
		Transport: &http.Transport{Proxy: nil},
	}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/json/version", o.cdpPort))
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// Shutdown 依次尝试 CDP Browser.close、平台优雅信号（Windows CTRL_BREAK 进程组 /
// POSIX SIGTERM），等待进程退出；超时后 taskkill /T /F 进程树强杀并 Kill 兜底。
// 实测 v0.2.6：CDP Browser.close 只断 WS 不退进程，CTRL_BREAK 可在 ~15ms 内 clean exit。
func (o *ObscuraRunner) Shutdown(ctx context.Context) error {
	o.mu.Lock()
	if o.closed {
		o.mu.Unlock()
		return nil
	}
	o.closed = true
	o.mu.Unlock()
	if o.cmd == nil || o.cmd.Process == nil {
		// cmd.Start 失败（如二进制缺失）时也要关闭已创建的日志文件，
		// 否则 Windows 上文件句柄会阻止会话临时目录被删除。
		o.cleanup()
		return nil
	}
	_ = sendBrowserClose(o.wsAddr)
	if err := platformGracefulSignal(o.cmd.Process.Pid); err != nil {
		o.logger.Printf("[obscura] graceful signal failed: %v", err)
	}

	grace := min(o.cfg.Timeout.Shutdown(), 4*time.Second)
	select {
	case <-o.waitDone:
		o.logger.Printf("[obscura] exited gracefully")
		o.cleanup()
		return nil
	case <-time.After(grace):
	case <-ctx.Done():
	}
	o.logger.Printf("[obscura] grace timeout, force kill pid=%d", o.cmd.Process.Pid)
	if runtime.GOOS == "windows" {
		_ = exec.Command("taskkill", "/PID", strconv.Itoa(o.cmd.Process.Pid), "/T", "/F").Run()
	}
	_ = o.cmd.Process.Kill()
	select {
	case <-o.waitDone:
	case <-time.After(3 * time.Second):
		o.logger.Printf("[obscura] WARNING process still not reaped")
	}
	o.cleanup()
	return nil
}

func (o *ObscuraRunner) cleanup() {
	if o.logFile != nil {
		_ = o.logFile.Close()
	}
}

func (o *ObscuraRunner) err() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.exitErr
}

func (o *ObscuraRunner) tailLog() string {
	b, err := os.ReadFile(filepath.Join(o.dir, "obscura.log"))
	if err != nil {
		return "(no log)"
	}
	s := string(b)
	if len(s) > 800 {
		s = s[len(s)-800:]
	}
	return strings.ReplaceAll(s, "\n", " | ")
}

// buildObscuraArgs 构造 obscura serve 参数（纯函数，便于单测）。
// 不向 Obscura 传 --obey-robots：v0.2.6 的 help 明确该参数只作用于
// fetch/scrape，不含 serve（CDP）模式，实测经 rod 导航时完全不生效；
// robots.txt 约束改由 internal/policy.Robots 在爬虫层自研实现。
func buildObscuraArgs(port int, proxyAddr, profileDir string, stealth bool) []string {
	args := []string{
		"serve",
		"--port", strconv.Itoa(port),
		"--proxy", proxyAddr,
		"--storage-dir", profileDir,
	}
	if stealth {
		args = append(args, "--stealth")
	}
	return args
}

// buildEnv 清理宿主注入的代理变量（避免 Obscura 绕过本会话 goproxy），
// 注入 SSL_CERT_FILE 让 Obscura 信任 MITM CA，并透传配置里的环境变量。
func buildEnv(cfg *config.Config, bundlePath string) []string {
	drop := map[string]bool{
		"HTTP_PROXY": true, "HTTPS_PROXY": true, "ALL_PROXY": true,
		"http_proxy": true, "https_proxy": true, "all_proxy": true,
		"NO_PROXY": true, "no_proxy": true,
		"SSL_CERT_FILE": true, "SSL_CERT_DIR": true,
	}
	env := make([]string, 0, len(os.Environ())+len(cfg.Env)+2)
	for _, e := range os.Environ() {
		k := strings.SplitN(e, "=", 2)[0]
		if drop[k] {
			continue
		}
		env = append(env, e)
	}
	env = append(env, "SSL_CERT_FILE="+bundlePath)
	// 只读采集：保留 stealth 的 TLS/UA 指纹，但关闭 tracker 拦截（不篡改流量）。
	if _, ok := cfg.Env["OBSCURA_BLOCK_TRACKERS"]; !ok {
		env = append(env, "OBSCURA_BLOCK_TRACKERS=0")
	}
	for k, v := range cfg.Env {
		env = append(env, k+"="+v)
	}
	return env
}

// sendBrowserClose 通过 CDP 发 Browser.close（幂等，失败不影响后续强杀）。
func sendBrowserClose(wsURL string) error {
	if wsURL == "" {
		return fmt.Errorf("empty ws url")
	}
	d := websocket.Dialer{
		HandshakeTimeout: 2 * time.Second,
		Proxy:            func(*http.Request) (*url.URL, error) { return nil, nil },
	}
	c, _, err := d.Dial(wsURL, nil)
	if err != nil {
		return err
	}
	defer c.Close()
	_ = c.SetWriteDeadline(time.Now().Add(2 * time.Second))
	return c.WriteJSON(map[string]any{"id": 1, "method": "Browser.close"})
}

func freePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}
