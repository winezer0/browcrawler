// Package metrics 提供极简的进程内指标收集，不引入第三方依赖。
// 编排层通过 Recorder 接口注入，未启用时用 Nop，避免到处判断 nil。
package metrics

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

type Recorder interface {
	Inc(name string, n int64)
	Snapshot() map[string]int64
	Close() error
}

type nop struct{}

func (nop) Inc(string, int64)          {}
func (nop) Snapshot() map[string]int64 { return map[string]int64{} }
func (nop) Close() error               { return nil }

// Nop 返回空实现。
func Nop() Recorder { return nop{} }

type Collector struct {
	mu       sync.Mutex
	counters map[string]int64
	file     *os.File
	enc      *json.Encoder
	label    string
	start    time.Time
}

// New 创建指标收集器；outFile 为空时只保留内存快照。
func New(label, outFile string) (*Collector, error) {
	c := &Collector{counters: map[string]int64{}, label: label, start: time.Now()}
	if outFile != "" {
		if err := os.MkdirAll(filepath.Dir(outFile), 0o755); err != nil {
			return nil, err
		}
		f, err := os.Create(outFile)
		if err != nil {
			return nil, err
		}
		c.file, c.enc = f, json.NewEncoder(f)
	}
	return c, nil
}

func (c *Collector) Inc(name string, n int64) {
	c.mu.Lock()
	c.counters[name] += n
	c.mu.Unlock()
}

func (c *Collector) Snapshot() map[string]int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]int64, len(c.counters))
	for k, v := range c.counters {
		out[k] = v
	}
	return out
}

// Close 落一行汇总（按 key 排序，输出稳定），并关闭文件。
func (c *Collector) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.file == nil {
		return nil
	}
	snap := map[string]any{
		"label": c.label, "at": time.Now().Format(time.RFC3339Nano),
		"uptime_ms": time.Since(c.start).Milliseconds(),
	}
	keys := make([]string, 0, len(c.counters))
	for k := range c.counters {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		snap[k] = c.counters[k]
	}
	err := c.enc.Encode(snap)
	if e := c.file.Close(); err == nil {
		err = e
	}
	c.file = nil
	return err
}
