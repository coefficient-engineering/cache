package cache

import (
	"slices"
)

// tagIndex and L1 mutations are guarded together by cache.l1Mu.
type tagIndex struct {
	index map[string]map[string]struct{}
}

func (ti *tagIndex) add(prefixedKey string, tags []string) {
	if len(tags) == 0 {
		return
	}
	for _, tag := range tags {
		if ti.index == nil {
			ti.index = make(map[string]map[string]struct{})
		}
		if ti.index[tag] == nil {
			ti.index[tag] = make(map[string]struct{})
		}
		ti.index[tag][prefixedKey] = struct{}{}
	}
}

func (ti *tagIndex) remove(prefixedKey string, tags []string) {
	if len(tags) == 0 {
		return
	}
	for _, tag := range tags {
		if ti.index[tag] != nil {
			delete(ti.index[tag], prefixedKey)
			if len(ti.index[tag]) == 0 {
				delete(ti.index, tag)
			}
		}
	}
}

func (ti *tagIndex) clear() {
	ti.index = nil
}

// evictL1Tags uses this node's index, never the sender's knowledge of keys.
func (c *cache) evictL1Tags(tags []string) {
	c.l1Mu.Lock()
	defer c.l1Mu.Unlock()
	for _, tag := range tags {
		for pk := range c.tagIdx.index[tag] {
			if entry, ok := c.l1Entry(pk); ok && slices.Contains(entry.tags, tag) {
				c.evictL1Locked(pk)
			}
		}
	}
}

func (c *cache) clearL1() {
	c.l1Mu.Lock()
	defer c.l1Mu.Unlock()
	c.l1.Clear()
	c.tagIdx.clear()
}
