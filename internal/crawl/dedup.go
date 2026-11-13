package crawl

import (
	"log"
	"time"

	"github.com/winezer0/requestfilter"

	"browcrawler/internal/metrics"
)

// DedupSession 封装 requestfilter 的流式去重会话（形状覆盖 + 动态路径段泛化）。
// BFS 入队/导航前 Reserve，导航成功 Commit、失败 Release（与驱动自愈重试衔接）。
// 一个 App 级会话跨任务共享：认证组隔离内建，目标站相同的路径学习互相受益。
type DedupSession struct {
	session *requestfilter.Session
	logger  *log.Logger
	metrics metrics.Recorder
}

// NewDedupSession threshold<=0 时返回 nil（禁用）。
func NewDedupSession(pathSegmentThreshold int, logger *log.Logger) (*DedupSession, error) {
	return NewDedupSessionWithMetrics(pathSegmentThreshold, 0, logger, metrics.Nop())
}

// NewDedupSessionWithMetrics 允许注入指标收集器与条目 TTL
// （TTL>0 时旧形状自动淘汰，容量自然回收，避免超大站触发容量拒断）。
func NewDedupSessionWithMetrics(pathSegmentThreshold int, ttlSeconds int, logger *log.Logger, m metrics.Recorder) (*DedupSession, error) {
	if pathSegmentThreshold <= 0 {
		return nil, nil
	}
	session, err := requestfilter.NewSession(requestfilter.Options{
		DedupStrategy:        requestfilter.DedupStrategyCoverageParameters,
		PathSegmentThreshold: pathSegmentThreshold,
		EntryTTL:             time.Duration(ttlSeconds) * time.Second,
	})
	if err != nil {
		return nil, err
	}
	return &DedupSession{session: session, logger: logger, metrics: m}, nil
}

// Reserve 判定 URL 是否应导航。返回 allowed=false 时调用方应跳过（形状覆盖或泛化重复）。
func (d *DedupSession) Reserve(url string) (allowed bool, reservation *requestfilter.Reservation) {
	if d == nil || d.session == nil {
		return true, nil
	}
	decision, reserved, err := d.session.Reserve(requestfilter.Request{
		Method: "GET", URL: url, BodyCaptured: true,
	})
	if err != nil {
		// 过滤器故障时不阻塞爬取（fail-open），由精确去重兜底。
		d.logger.Printf("[dedup] reserve failed (fail-open): %s: %v", url, err)
		return true, nil
	}
	switch decision.Action {
	case requestfilter.ActionAllow:
		return true, &reserved
	case requestfilter.ActionRetry:
		// 同形状请求在途：跳过本轮，稍后由其他来源再触发。
		d.logger.Printf("[dedup] in-flight, skip: %s", url)
		return false, nil
	case requestfilter.ActionReject:
		// 容量满（MaxEntries）：fail-open 放行导航——拒绝爬取比形状重复更糟，
		// 且大站爬取会静默停摆。无预留可提交（Commit(nil) 为安全空操作）。
		d.logger.Printf("[dedup] session capacity reached, fail-open: %s", url)
		d.metrics.Inc("crawl.dedup_rejected", 1)
		return true, nil
	default: // Drop：形状已覆盖
		return false, nil
	}
}

// Commit 导航成功后确认覆盖状态。
func (d *DedupSession) Commit(reservation *requestfilter.Reservation) {
	if d == nil || d.session == nil || reservation == nil {
		return
	}
	if err := d.session.Commit(*reservation); err != nil {
		d.logger.Printf("[dedup] commit failed: %v", err)
	}
}

// Release 导航失败后释放预留，便于（自愈重连后）重试同一 URL。
func (d *DedupSession) Release(reservation *requestfilter.Reservation) {
	if d == nil || d.session == nil || reservation == nil {
		return
	}
	_ = d.session.Release(*reservation)
}

func (d *DedupSession) Close() {
	if d == nil || d.session == nil {
		return
	}
	d.session.Close()
}
