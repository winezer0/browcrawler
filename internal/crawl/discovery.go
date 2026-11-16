package crawl

import "encoding/json"

// writeDiscovery deduplicates evidence edges, independently of navigation.
// Different parents, methods or discovery mechanisms must remain queryable.
func (c *Crawler) writeDiscovery(d Discovery) (int, error) {
	key, err := json.Marshal([]string{d.URL, d.Method, d.Source, d.From})
	if err != nil {
		return 0, err
	}
	if c.discoverySeen == nil {
		c.discoverySeen = map[string]bool{}
	}
	if c.discoverySeen[string(key)] {
		return 0, nil
	}
	if err := c.enc.Encode(d); err != nil {
		return 0, err
	}
	c.discoverySeen[string(key)] = true
	return 1, nil
}
