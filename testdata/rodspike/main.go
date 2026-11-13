// spike: 验证 rod 能否驱动 Obscura 的 CDP（不依赖代理/MITM）
package main

import (
	"fmt"
	"os"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

func main() {
	ws := os.Args[1]
	url := os.Args[2]

	b := rod.New().ControlURL(ws)
	t0 := time.Now()
	if err := b.Connect(); err != nil {
		fmt.Println("CONNECT_FAIL:", err)
		os.Exit(1)
	}
	fmt.Printf("CONNECT_OK %dms\n", time.Since(t0).Milliseconds())
	defer b.Close()

	p, err := b.Page(proto.TargetCreateTarget{URL: "about:blank"})
	if err != nil {
		fmt.Println("PAGE_FAIL:", err)
		os.Exit(1)
	}
	fmt.Println("PAGE_OK")

	if err := p.Navigate(url); err != nil {
		fmt.Println("NAV_FAIL:", err)
		os.Exit(1)
	}
	if err := p.WaitLoad(); err != nil {
		fmt.Println("WAITLOAD_FAIL:", err)
		os.Exit(1)
	}
	time.Sleep(800 * time.Millisecond) // 等懒加载 fetch 触发
	h, err := p.HTML()
	if err != nil {
		fmt.Println("HTML_FAIL:", err)
		os.Exit(1)
	}
	fmt.Printf("HTML_OK len=%d contains_h1=%v elapsed=%dms\n", len(h), len(h) > 0, time.Since(t0).Milliseconds())
	fmt.Println("HTML_HEAD:", h[:min(len(h), 200)])
}
