package crawl

import (
	"io"
	"log"
	"testing"

	"github.com/winezer0/requestfilter"

	"github.com/winezer0/browcrawler/internal/metrics"
)

// 容量拒断必须 fail-open：MaxEntries 耗尽后 Reserve 返回 ActionReject，
// 若按 Drop 跳过会导致大站爬取静默停摆（审计发现的真缺陷）。
func TestDedupSessionRejectIsFailOpen(t *testing.T) {
	small, err := requestfilter.NewSession(requestfilter.Options{
		DedupStrategy:        requestfilter.DedupStrategyCoverageParameters,
		PathSegmentThreshold: 0,
		MaxEntries:           1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer small.Close()

	d := &DedupSession{session: small, logger: log.New(io.Discard, "", 0), metrics: metrics.Nop()}
	// MaxEntries=1：第一条 Reserve 需要 2 个 state entries（coverage 组 + 预留），
	// 直接触发 ActionReject——fail-open 必须放行。
	allowed, reservation := d.Reserve("http://example.test/a")
	if !allowed {
		t.Fatal("capacity reject must fail-open (allow navigation)")
	}
	if reservation != nil {
		t.Fatal("reject 路径不应产生预留（否则 Commit 会失败）")
	}
	// 允许后无预留可提交：Commit(nil) 必须是安全空操作。
	d.Commit(reservation)
	d.Release(reservation)
}

// Drop（形状已覆盖）仍然跳过：fail-open 只针对容量，不放弃去重本身。
func TestDedupSessionDropStillSkips(t *testing.T) {
	small, err := requestfilter.NewSession(requestfilter.Options{
		DedupStrategy: requestfilter.DedupStrategyCoverageParameters,
		MaxEntries:    100,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer small.Close()

	d := &DedupSession{session: small, logger: log.New(io.Discard, "", 0), metrics: metrics.Nop()}
	if allowed, res := d.Reserve("http://example.test/x?id=1"); !allowed {
		t.Fatal("first request must be allowed")
	} else {
		d.Commit(res)
	}
	if allowed, _ := d.Reserve("http://example.test/x?id=2"); allowed {
		// CoverageParameters 忽略参数值：同形状第二请求应被跳过。
		t.Fatal("same-shape request must be filtered")
	}
}

// nil 安全：禁用去重（threshold<=0 → session 为 nil）时全部放行。
func TestDedupSessionNilSafe(t *testing.T) {
	var d *DedupSession
	if allowed, res := d.Reserve("http://example.test/any"); !allowed || res != nil {
		t.Fatal("nil session must fail-open without reservation")
	}
	d.Commit(nil)
	d.Release(nil)
	d.Close()
}
