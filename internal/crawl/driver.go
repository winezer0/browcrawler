// Package crawl 实现 BFS 调度、DOM 链接提取、JS 接口提取（assetcrawler extractor）与 rod 页面驱动。
package crawl

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"

	"browcrawler/internal/storage"
)

// PageDriver 把浏览器页面能力抽象出来，后续可替换 Chrome/自研 CDP 客户端。
type PageDriver interface {
	Connect(ctx context.Context, wsURL string) error
	Navigate(ctx context.Context, url string) error
	HTML(ctx context.Context) (string, error)
	Eval(ctx context.Context, expr string) (string, error)
	Close() error
}

type RodDriver struct {
	browser    *rod.Browser
	page       *rod.Page
	wsURL      string
	settle     time.Duration
	navTimeout time.Duration
}

func NewRodDriver(settle, navTimeout time.Duration) *RodDriver {
	return &RodDriver{settle: settle, navTimeout: navTimeout}
}

func (d *RodDriver) Connect(ctx context.Context, wsURL string) error {
	d.wsURL = wsURL
	if err := d.connect(ctx); err != nil {
		return err
	}
	return nil
}

// connect 建立或重建 CDP 连接与工作标签页。
func (d *RodDriver) connect(ctx context.Context) error {
	b := rod.New().ControlURL(d.wsURL).Context(ctx)
	if err := b.Connect(); err != nil {
		return fmt.Errorf("rod connect %s: %w", d.wsURL, err)
	}
	p, err := b.Page(proto.TargetCreateTarget{URL: "about:blank"})
	if err != nil {
		_ = b.Close()
		return fmt.Errorf("rod create page: %w", err)
	}
	d.browser, d.page = b, p
	return nil
}

// isConnErr 判断是否为 CDP 连接级故障（Obscura worker 长会话老化回收、
// 网络闪断等）。真实站点 depth=2 实测第 74 页后 WS 断开导致后续全失败，
// 驱动层必须能自愈重连，而不是把整批页面标记为失败。
func (d *RodDriver) isConnErr(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	for _, k := range []string{
		"use of closed network connection", "connection refused",
		"broken pipe", "eof", "websocket: close sent", "unexpected eof",
	} {
		if strings.Contains(s, k) {
			return true
		}
	}
	return false
}

// withRetry 先执行操作；若失败且属连接级错误，则重连 CDP 一次后重试。
// 限一次重试：重连后再失败说明浏览器确实不可用，交由上层（池巡检/任务失败）处理。
func (d *RodDriver) withRetry(ctx context.Context, op func(p *rod.Page) error) error {
	p, cancel := d.scoped(ctx)
	err := op(p)
	cancel()
	if err == nil || !d.isConnErr(err) || ctx.Err() != nil {
		return err
	}
	if rerr := d.connect(ctx); rerr != nil {
		return err // 保留原始错误，重连失败说明问题更深层
	}
	p2, cancel2 := d.scoped(ctx)
	defer cancel2()
	return op(p2)
}

// scoped 把每次页面操作绑定到调用方 ctx 并叠加单次导航超时。
// 这样 Ctrl+C / task 超时能立即中止卡住的 CDP 调用，而不是一路阻塞到进程被杀。
func (d *RodDriver) scoped(ctx context.Context) (*rod.Page, context.CancelFunc) {
	if d.navTimeout <= 0 {
		return d.page.Context(ctx), func() {}
	}
	c, cancel := context.WithTimeout(ctx, d.navTimeout)
	return d.page.Context(c), cancel
}

func (d *RodDriver) Navigate(ctx context.Context, url string) error {
	return d.withRetry(ctx, func(p *rod.Page) error {
		if err := p.Navigate(url); err != nil {
			return err
		}
		// 部分页面没有 load 事件（如纯 JSON/XHR 响应），WaitLoad 失败不视为致命错误，
		// 但超时仍由同一个 ctx 控制，不会无限等待。
		_ = p.WaitLoad()
		if d.settle > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(d.settle): // 等渲染后的懒加载 fetch/XHR 触发
			}
		}
		return nil
	})
}

func (d *RodDriver) HTML(ctx context.Context) (string, error) {
	var out string
	err := d.withRetry(ctx, func(p *rod.Page) error {
		s, e := p.HTML()
		out = s
		return e
	})
	return out, err
}

func (d *RodDriver) Eval(ctx context.Context, expr string) (string, error) {
	var out string
	err := d.withRetry(ctx, func(p *rod.Page) error {
		obj, e := p.Eval(expr)
		if e != nil {
			return e
		}
		out = fmt.Sprintf("%v", obj.Value)
		return nil
	})
	return out, err
}

func (d *RodDriver) Close() error {
	if d.browser == nil {
		return nil
	}
	// Browser.Close 会向 CDP 发 Browser.close；Obscura 只断 WS 不退进程，
	// 真正的退出由 browserrunner 的 CTRL_BREAK 完成。
	err := d.browser.Close()
	d.browser = nil
	return err
}

// CollectForms 收集当前页全部表单元数据（实现 FormSubmitter，见 forms.go）。
func (d *RodDriver) CollectForms(ctx context.Context) ([]FormInfo, error) {
	var forms []FormInfo
	err := d.withRetry(ctx, func(p *rod.Page) error {
		obj, e := p.Eval(collectFormsJS)
		if e != nil {
			return e
		}
		if e := obj.Value.Unmarshal(&forms); e != nil {
			return fmt.Errorf("parse forms: %w", e)
		}
		return nil
	})
	return forms, err
}

// FillAndSubmit 按 index 填充并提交一张表单，返回浏览器端的提交方式。
// JS 与参数分开传递（rod 会以 (fn).apply(this, args) 调用），无字符串拼接注入面。
// 注意：提交成功会触发导航，执行上下文销毁报错视为提交已生效。
func (d *RodDriver) FillAndSubmit(ctx context.Context, index int, values map[string]string) (string, error) {
	var out string
	err := d.withRetry(ctx, func(p *rod.Page) error {
		obj, e := p.Eval(fillSubmitJS, index, values)
		if e != nil {
			return e
		}
		out = obj.Value.Str()
		return nil
	})
	if err != nil && isContextDestroyed(err) {
		return "navigated", nil
	}
	return out, err
}

// WSEvent 是一次 WebSocket 协议事件（帧/建连/关闭），由驱动从 CDP 转出。
type WSEvent struct {
	Kind      string // created|handshake|frame|closed|error
	ReqID     string
	URL       string // created/handshake 事件的 ws(s) 建连地址
	Opcode    string // frame: text|binary|continue|close|ping|pong
	Direction string // frame: sent|received
	Seq       int    // frame: 会话内帧序号（从 1 起）
	Payload   []byte // frame
	Code      int    // closed
	Reason    string // closed
	Err       string // error
}

// WSCapturer 是支持 WebSocket 帧捕获的驱动能力（RodDriver 实现，CDP 事件
// 与代理链路无关，Obscura/Chrome 双后端通用）。
type WSCapturer interface {
	EnableWSCapture(ctx context.Context, onEvent func(WSEvent)) error
}

// EnableWSCapture 实现 WSCapturer：启用 Network 域并订阅全部 WS 事件，
// 转换后逐条回调 onEvent（由调用方写入录制层）。订阅随 page 生命周期结束。
func (d *RodDriver) EnableWSCapture(ctx context.Context, onEvent func(WSEvent)) error {
	// 注意：不能用 scoped()——其 navTimeout+cancel 会在本函数返回时杀掉
	// 订阅上下文，事件流随之断开。订阅生命周期 = 传入 ctx（Run 生命周期）。
	p := d.page.Context(ctx)
	if err := (proto.NetworkEnable{}).Call(p); err != nil {
		return err
	}
	seqs := map[string]int{} // requestID -> 帧序号（回调在 rod 事件循环内串行执行，无需加锁）
	// rod 语义坑：EachEvent 返回的 wait() 才开始消费事件流并分发回调
	//（阻塞直到某回调返回 true）。go wait() 即持久订阅。
	wait := p.EachEvent(
		func(e *proto.NetworkWebSocketCreated) {
			onEvent(WSEvent{Kind: "created", ReqID: string(e.RequestID), URL: e.URL})
		},
		func(e *proto.NetworkWebSocketHandshakeResponseReceived) {
			if e.Response != nil {
				onEvent(WSEvent{Kind: "handshake", ReqID: string(e.RequestID)})
			}
		},
		func(e *proto.NetworkWebSocketFrameSent) {
			onEvent(d.wsFrameEvent(string(e.RequestID), e.Response, "sent", seqs))
		},
		func(e *proto.NetworkWebSocketFrameReceived) {
			onEvent(d.wsFrameEvent(string(e.RequestID), e.Response, "received", seqs))
		},
		func(e *proto.NetworkWebSocketClosed) {
			onEvent(WSEvent{Kind: "closed", ReqID: string(e.RequestID)})
		},
		func(e *proto.NetworkWebSocketFrameError) {
			onEvent(WSEvent{Kind: "error", ReqID: string(e.RequestID), Err: e.ErrorMessage})
		},
	)
	go wait()
	return nil
}

// wsFrameEvent 把 CDP 帧事件转换为 WSEvent（opcode 数值转可读名 + 会话内序号）。
func (d *RodDriver) wsFrameEvent(reqID string, f *proto.NetworkWebSocketFrame, dir string, seqs map[string]int) WSEvent {
	seqs[reqID]++
	ev := WSEvent{Kind: "frame", ReqID: reqID, Direction: dir}
	if f != nil {
		ev.Opcode = storage.WSOpcodeName(f.Opcode)
		ev.Payload = []byte(f.PayloadData)
	}
	ev.Seq = seqs[reqID]
	return ev
}
