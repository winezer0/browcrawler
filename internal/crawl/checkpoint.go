package crawl

import (
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
)

// Checkpoint 是断点续扫的持久化结构：恢复后 BFS 从中断点继续，
// seen 集合保证已爬页面不重复导航。
type Checkpoint struct {
	Seed string   `json:"seed"`
	Seen []string `json:"seen"`
	Rows []struct {
		URL   string `json:"url"`
		Depth int    `json:"depth"`
	} `json:"queue"`
}

func checkpointKey(seed string) string {
	sum := sha1hex(seed)
	return sum[:16]
}

// CheckpointPathFor 返回某 seed 的默认断点文件路径（work/checkpoints/<hash>.json）。
func CheckpointPathFor(dir, seed string) string {
	return filepath.Join(dir, checkpointKey(seed)+".json")
}

// SaveCheckpoint 原子写断点（tmp + rename），失败不中断爬取。
func SaveCheckpoint(path, seed string, seen map[string]bool, queue []item) error {
	cp := Checkpoint{Seed: seed}
	for key := range seen {
		cp.Seen = append(cp.Seen, key)
	}
	for _, it := range queue {
		cp.Rows = append(cp.Rows, struct {
			URL   string `json:"url"`
			Depth int    `json:"depth"`
		}{URL: it.URL, Depth: it.Depth})
	}
	data, err := json.Marshal(cp)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// LoadCheckpoint 读取断点；文件不存在返回 nil 且无错误（首次运行）。
func LoadCheckpoint(path, seed string) (*Checkpoint, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var cp Checkpoint
	if err := json.Unmarshal(data, &cp); err != nil {
		return nil, fmt.Errorf("parse checkpoint: %w", err)
	}
	if cp.Seed != seed {
		return nil, fmt.Errorf("checkpoint seed mismatch: %s != %s", cp.Seed, seed)
	}
	return &cp, nil
}

// restoreCheckpoint 把断点还原到 BFS 状态；仅当 URL 的 host 与 seed 一致时信任。
func restoreCheckpoint(cp *Checkpoint, seen map[string]bool, queue *[]item) {
	seenInFile := make(map[string]bool, len(cp.Seen))
	for _, key := range cp.Seen {
		if parsed, err := url.Parse(key); err == nil && parsed.Host != "" {
			seen[key] = true
			seenInFile[key] = true
		}
	}
	for _, row := range cp.Rows {
		if parsed, err := url.Parse(row.URL); err == nil && parsed.Host != "" && !seenInFile[row.URL] {
			*queue = append(*queue, item{URL: row.URL, Depth: row.Depth})
		}
	}
}

func sha1hex(s string) string {
	sum := sha1.Sum([]byte(s))
	return fmt.Sprintf("%x", sum)
}
