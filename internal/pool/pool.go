// Package pool 实现设计文档 §4.2 的 SessionPool：并发上限、复用、健康巡检与统一回收。
//
// 并发模型：sem 代表「同时被任务持有的会话许可」，容量 = max_session。
// 只有拿到许可且 idle 为空时才会新建会话；Release 时把健康会话放回 idle 或销毁。
// 因此 all（idle + inUse）恒 <= max_session，不会突破配置上限。
package pool

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/winezer0/browcrawler/internal/ca"
	"github.com/winezer0/browcrawler/internal/config"
	"github.com/winezer0/browcrawler/internal/metrics"
	"github.com/winezer0/browcrawler/internal/session"
)

type Options struct {
	Cfg         *config.Config
	Logger      *log.Logger
	SessionRoot string                 // work/sessions
	Output      session.OutputPaths    // 基础输出路径；max>1 时按 sessionID 加后缀
	Browser     session.BrowserFactory // 由后端选择层注入
	ScopeFilter func(string) bool      // URL 级作用域判定（透传 session/recorder）
	Metrics     metrics.Recorder
	Now         func() time.Time // 可注入时钟，便于单元测试
}

type entry struct {
	sess     *session.Session
	uses     int
	lastUsed time.Time
	inUse    bool
}

type Pool struct {
	o       Options
	now     func() time.Time
	max     int
	sem     chan struct{}
	mu      sync.Mutex
	idle    []*entry
	all     map[string]*entry
	closed  bool
	stopCh  chan struct{}
	wg      sync.WaitGroup
	once    sync.Once
	seq     atomic.Int64
	metrics metrics.Recorder
}

func New(o Options) (*Pool, error) {
	if o.Cfg == nil || o.Logger == nil || o.Browser == nil {
		return nil, fmt.Errorf("pool.New: Cfg/Logger/Browser 不能为空")
	}
	if o.Metrics == nil {
		o.Metrics = metrics.Nop()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.SessionRoot == "" {
		o.SessionRoot = filepath.Join("work", "sessions")
	}
	p := &Pool{
		o: o, now: o.Now, max: o.Cfg.MaxSession, sem: make(chan struct{}, o.Cfg.MaxSession),
		all: map[string]*entry{}, stopCh: make(chan struct{}), metrics: o.Metrics,
	}
	p.wg.Add(1)
	go p.healthLoop()
	return p, nil
}

// Acquire 取一个已就绪且健康的会话；必要时新建。ctx 取消时立即返回并归还许可。
func (p *Pool) Acquire(ctx context.Context) (*session.Session, error) {
	select {
	case p.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	releaseSlot := func() { <-p.sem }

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		releaseSlot()
		return nil, fmt.Errorf("pool is closed")
	}
	for len(p.idle) > 0 {
		e := p.idle[len(p.idle)-1]
		p.idle = p.idle[:len(p.idle)-1]
		if p.expired(e) || !e.sess.Healthy() {
			p.discardLocked(e)
			continue
		}
		e.inUse = true
		p.mu.Unlock()
		p.metrics.Inc("pool.reused", 1)
		p.metrics.Inc("pool.acquired", 1)
		return e.sess, nil
	}
	p.mu.Unlock()

	s, err := p.newSession(ctx)
	if err != nil {
		releaseSlot()
		return nil, err
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		releaseSlot()
		_ = s.Shutdown(context.Background())
		return nil, fmt.Errorf("pool is closed")
	}
	p.all[s.ID] = &entry{sess: s, inUse: true, lastUsed: p.now()}
	p.mu.Unlock()
	p.metrics.Inc("pool.created", 1)
	p.metrics.Inc("pool.acquired", 1)
	return s, nil
}

func (p *Pool) newSession(ctx context.Context) (*session.Session, error) {
	id := fmt.Sprintf("s-%s-%03d", p.now().Format("20060102-150405.000"), p.seq.Add(1))
	dir := filepath.Join(p.o.SessionRoot, id)
	provider := ca.NewProvider(ca.Options{
		CrtPath: p.o.Cfg.Mitm.CACrt, KeyPath: p.o.Cfg.Mitm.CAKey, Dir: dir,
		PublicRootsFile: p.publicRootsFile(), PerSession: p.o.Cfg.Mitm.PerSessionCA,
	})
	s, err := session.New(session.Options{
		ID: id, Dir: dir, Cfg: p.o.Cfg, Logger: p.o.Logger,
		CA: provider, Browser: p.o.Browser, Output: p.paths(id), ScopeFilter: p.o.ScopeFilter,
	})
	if err != nil {
		return nil, fmt.Errorf("create session %s: %w", id, err)
	}
	if err := s.Start(ctx); err != nil {
		_ = s.Shutdown(context.Background())
		return nil, fmt.Errorf("start session %s: %w", id, err)
	}
	return s, nil
}

func (p *Pool) publicRootsFile() string {
	if p.o.Cfg.Mitm.MergePublicRoots {
		return p.o.Cfg.Mitm.PublicRootsFile
	}
	return ""
}

// paths 让多会话输出互不冲突；单会话模式保持原路径，兼容既有验收与产物布局。
func (p *Pool) paths(id string) session.OutputPaths {
	o := p.o.Output
	if p.max <= 1 {
		return o
	}
	return session.OutputPaths{
		Traffic:          withSuffix(o.Traffic, id),
		Discovered:       withSuffix(o.Discovered, id),
		HAR:              withSuffix(o.HAR, id),
		Requests:         withSuffix(o.Requests, id),
		Report:           withSuffix(o.Report, id),
		RequestsFiltered: withSuffix(o.RequestsFiltered, id),
		OutScope:         withSuffix(o.OutScope, id),
		SaveJSDir:        o.SaveJSDir,
	}
}

func withSuffix(path, id string) string {
	if path == "" {
		return ""
	}
	ext := filepath.Ext(path)
	return strings.TrimSuffix(path, ext) + "-" + id + ext
}

// Release 归还会话：健康且未达复用上限则回池，否则立即回收。
func (p *Pool) Release(s *session.Session) {
	if s == nil {
		return
	}
	defer func() { <-p.sem }()

	p.mu.Lock()
	e := p.all[s.ID]
	if e == nil {
		p.mu.Unlock()
		return
	}
	e.inUse = false
	e.uses++
	reuseLimit := p.o.Cfg.Pool.ReuseLimit
	if p.closed || !s.Healthy() || (reuseLimit > 0 && e.uses >= reuseLimit) {
		p.discardLocked(e)
		p.mu.Unlock()
		p.shutdown(s)
		p.metrics.Inc("pool.discarded", 1)
		return
	}
	e.lastUsed = p.now()
	p.idle = append(p.idle, e)
	p.mu.Unlock()
	p.metrics.Inc("pool.released", 1)
}

// discardLocked 从索引中移除，调用方负责在锁外 Shutdown。
func (p *Pool) discardLocked(e *entry) {
	delete(p.all, e.sess.ID)
	e.sess.MarkUnhealthy()
	go func() {
		p.shutdown(e.sess)
		p.metrics.Inc("pool.discarded", 1)
	}()
}

func (p *Pool) shutdown(s *session.Session) {
	ctx, cancel := context.WithTimeout(context.Background(), p.o.Cfg.Timeout.Shutdown())
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		p.o.Logger.Printf("[pool] shutdown %s: %v", s.ID, err)
	}
}

func (p *Pool) expired(e *entry) bool {
	maxIdle := p.o.Cfg.Pool.MaxIdle()
	return maxIdle > 0 && p.now().Sub(e.lastUsed) > maxIdle
}

func (p *Pool) healthLoop() {
	defer p.wg.Done()
	ticker := time.NewTicker(p.o.Cfg.Pool.HealthInterval())
	defer ticker.Stop()
	for {
		select {
		case <-p.stopCh:
			return
		case <-ticker.C:
			p.sweep()
		}
	}
}

// sweep 巡检：idle 中不健康 / 空闲超时的会话直接回收；使用中的只打标记，
// 由 Release 负责回收，避免任务跑到一半被拆掉浏览器。
func (p *Pool) sweep() {
	p.mu.Lock()
	keep := p.idle[:0]
	for _, e := range p.idle {
		if p.expired(e) || !e.sess.Healthy() {
			p.discardLocked(e)
			continue
		}
		keep = append(keep, e)
	}
	p.idle = keep
	for _, e := range p.all {
		if e.inUse && !e.sess.Healthy() {
			e.sess.MarkUnhealthy()
			p.metrics.Inc("pool.unhealthy_detected", 1)
		}
	}
	p.mu.Unlock()
}

// CloseAll 停止巡检并回收全部会话（含仍在使用的），幂等。
func (p *Pool) CloseAll(ctx context.Context) error {
	var err error
	p.once.Do(func() {
		close(p.stopCh)
		p.wg.Wait()
		p.mu.Lock()
		p.closed = true
		entries := make([]*entry, 0, len(p.all))
		for _, e := range p.all {
			entries = append(entries, e)
		}
		p.all = map[string]*entry{}
		p.idle = nil
		p.mu.Unlock()
		for _, e := range entries {
			e.sess.MarkUnhealthy()
			if e := e.sess.Shutdown(ctx); e != nil && err == nil {
				err = e
			}
		}
		p.metrics.Inc("pool.closed", 1)
	})
	return err
}

// Stats 返回当前池状态快照，供日志与指标使用。
func (p *Pool) Stats() map[string]int {
	p.mu.Lock()
	defer p.mu.Unlock()
	inUse := 0
	for _, e := range p.all {
		if e.inUse {
			inUse++
		}
	}
	return map[string]int{"max": p.max, "total": len(p.all), "idle": len(p.idle), "in_use": inUse}
}
