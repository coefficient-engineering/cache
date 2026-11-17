package cache

import (
	"slices"
	"sync"
)

type tagIndex struct {
	mu    sync.RWMutex
	index map[string]map[string]struct{}
}

func (ti *tagIndex) add(prefixedKey string, tags []string) {
	if len(tags) == 0 {
		return
	}
	ti.mu.Lock()
	defer ti.mu.Unlock()
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
	ti.mu.Lock()
	defer ti.mu.Unlock()
	for _, tag := range tags {
		if ti.index[tag] != nil {
			delete(ti.index[tag], prefixedKey)
			if len(ti.index[tag]) == 0 {
				delete(ti.index, tag)
			}
		}
	}
}

func (ti *tagIndex) keysForTag(tag string) []string {
	ti.mu.RLock()
	defer ti.mu.RUnlock()
	if ti.index == nil {
		return nil
	}
	keys := make([]string, 0, len(ti.index[tag]))
	for k := range ti.index[tag] {
		keys = append(keys, k)
	}
	return keys
}

func (ti *tagIndex) clear() {
	ti.mu.Lock()
	defer ti.mu.Unlock()
	ti.index = nil
}

// evictL1Tags uses this node's index, never the sender's knowledge of keys.
func (c *cache) evictL1Tags(tags []string) {
	for _, tag := range tags {
		for _, pk := range c.tagIdx.keysForTag(tag) {
			if entry, ok := c.l1Entry(pk); ok && slices.Contains(entry.tags, tag) {
				c.evictL1(pk)
			}
		}
	}
}
