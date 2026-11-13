package metrics

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestCollectorIncSnapshotConcurrent(t *testing.T) {
	c, err := New("test", "")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 250; j++ {
				c.Inc("a", 1)
				c.Inc("b", 2)
			}
		}()
	}
	wg.Wait()
	snap := c.Snapshot()
	if snap["a"] != 2000 || snap["b"] != 4000 {
		t.Fatalf("并发计数错误: %v", snap)
	}
}

func TestCollectorCloseWritesSortedSingleLine(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "m.jsonl")
	c, err := New("browcrawler", p)
	if err != nil {
		t.Fatal(err)
	}
	c.Inc("zeta", 1)
	c.Inc("alpha", 3)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var snap map[string]any
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatalf("落盘不是合法 JSON: %v", err)
	}
	if snap["alpha"].(float64) != 3 || snap["zeta"].(float64) != 1 {
		t.Fatalf("计数错误: %v", snap)
	}
	s := string(data)
	if ai, zi := strings.Index(s, `"alpha"`), strings.Index(s, `"zeta"`); ai > zi {
		t.Fatal("key 未按排序输出")
	}
	// Close 后再次 Inc/Close 应安全（不 panic、不写新行）
	c.Inc("after", 1)
	if err := c.Close(); err != nil {
		t.Fatalf("double close: %v", err)
	}
}

func TestNewCreatesMissingDir(t *testing.T) {
	p := filepath.Join(t.TempDir(), "deep", "nested", "m.jsonl")
	c, err := New("t", p)
	if err != nil {
		t.Fatal(err)
	}
	c.Inc("x", 1)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatal(err)
	}
}

func TestNop(t *testing.T) {
	n := Nop()
	n.Inc("x", 1)
	if len(n.Snapshot()) != 0 {
		t.Fatal("Nop 不应有快照")
	}
	if err := n.Close(); err != nil {
		t.Fatal(err)
	}
}
