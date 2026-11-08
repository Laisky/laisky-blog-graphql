package auth

import (
	"container/list"
	"sync"
	"time"
)

// ttlCache is a bounded LRU map whose entries also expire individually.
// Keys are API key hashes, never raw keys.
type ttlCache[V any] struct {
	mu    sync.Mutex
	max   int
	order *list.List
	items map[string]*list.Element
}

// ttlEntry is one cached value with its absolute expiry.
type ttlEntry[V any] struct {
	key       string
	value     V
	expiresAt time.Time
}

// newTTLCache returns a cache holding at most maxEntries live entries.
func newTTLCache[V any](maxEntries int) *ttlCache[V] {
	return &ttlCache[V]{max: max(1, maxEntries), order: list.New(), items: make(map[string]*list.Element)}
}

// get returns a live value and marks it recently used; expired entries are dropped.
func (c *ttlCache[V]) get(key string, now time.Time) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	var zero V
	element, ok := c.items[key]
	if !ok {
		return zero, false
	}
	entry := element.Value.(*ttlEntry[V]) //nolint:forcetypeassert // the cache stores only *ttlEntry[V]
	if !now.Before(entry.expiresAt) {
		c.order.Remove(element)
		delete(c.items, key)
		return zero, false
	}
	c.order.MoveToFront(element)
	return entry.value, true
}

// put stores a value until expiresAt, evicting the least recently used entries
// once the cardinality bound is reached.
func (c *ttlCache[V]) put(key string, value V, expiresAt time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if element, ok := c.items[key]; ok {
		entry := element.Value.(*ttlEntry[V]) //nolint:forcetypeassert // the cache stores only *ttlEntry[V]
		entry.value, entry.expiresAt = value, expiresAt
		c.order.MoveToFront(element)
		return
	}
	c.items[key] = c.order.PushFront(&ttlEntry[V]{key: key, value: value, expiresAt: expiresAt})
	for c.order.Len() > c.max {
		oldest := c.order.Back()
		c.order.Remove(oldest)
		delete(c.items, oldest.Value.(*ttlEntry[V]).key) //nolint:forcetypeassert // the cache stores only *ttlEntry[V]
	}
}

// remove deletes a key if present.
func (c *ttlCache[V]) remove(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if element, ok := c.items[key]; ok {
		c.order.Remove(element)
		delete(c.items, key)
	}
}
