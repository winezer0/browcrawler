package crawl

import (
	"io"
	"log"
	"testing"
	"time"

	"github.com/winezer0/requestfilter"

	"github.com/winezer0/browcrawler/internal/metrics"
)

// EntryTTL 透传验证：条目过期后同形状重新放行（容量自然回收）。
func TestDedupSessionTTLOnlyExpiredEntries(t *testing.T) {
	small, err := requestfilter.NewSession(requestfilter.Options{
		DedupStrategy: requestfilter.DedupStrategyCoverageParameters,
		EntryTTL:      50 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer small.Close()
	d := &DedupSession{session: small, logger: log.New(io.Discard, "", 0), metrics: metrics.Nop()}

	if allowed, res := d.Reserve("http://example.test/x"); !allowed {
		t.Fatal("first must be allowed")
	} else {
		d.Commit(res)
	}
	if allowed, _ := d.Reserve("http://example.test/x"); allowed {
		t.Fatal("TTL 内同形状应被过滤")
	}
	time.Sleep(60 * time.Millisecond) // 条目过期
	if allowed, _ := d.Reserve("http://example.test/x"); !allowed {
		t.Fatal("TTL 过期后同形状应重新放行（容量回收语义）")
	}
}
