// Copied from the Detection Pack (commit 8f8b3a7) under the Apache License
// 2.0, and modified. See k8s-watcher/NOTICE for the changes.

// Package dedup keeps a short-window cache of recently-emitted event Results
// to suppress duplicate emissions when the same logical event re-fires.
//
// Kubernetes events flap. A pod that crashes once may produce dozens of
// BackOff events as the kubelet retries. The cache key (kind, namespace,
// name, reason) collapses those into one Result per dedup window.
package dedup

import (
	"sort"
	"sync"
	"time"
)

// Key uniquely identifies an event-class for dedup purposes.
type Key struct {
	InvolvedKind      string
	InvolvedNamespace string
	InvolvedName      string
	Reason            string
}

// String returns a stable string representation of the key. Useful for
// logging and as a map key when full Key value isn't ergonomic.
func (k Key) String() string {
	return k.InvolvedKind + "/" + k.InvolvedNamespace + "/" + k.InvolvedName + "/" + k.Reason
}

// Cache is a concurrent-safe map from dedup Key → last-emission timestamp.
// Operations are O(1) for the hot path; SweepStale is O(n) over entries
// older than the window.
type Cache struct {
	mu     sync.Mutex
	seen   map[Key]time.Time
	window time.Duration
	now    func() time.Time
}

// New returns a Cache that suppresses re-emissions within `window` of a
// prior emission for the same key.
func New(window time.Duration) *Cache {
	return &Cache{
		seen:   make(map[Key]time.Time),
		window: window,
		now:    time.Now,
	}
}

// ShouldEmit returns true if the caller should emit a Result for this key —
// i.e., either we've never seen it or the prior emission is older than the
// dedup window. Stamps the key as freshly emitted as a side effect (so a
// second call within the same window returns false).
func (c *Cache) ShouldEmit(k Key) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	last, seen := c.seen[k]
	if seen && now.Sub(last) < c.window {
		return false
	}
	c.seen[k] = now
	return true
}

// SweepStale removes entries older than the window. Returns the keys removed.
// Caller is responsible for any side-effects (none today; Results persist
// independently of dedup-cache state).
func (c *Cache) SweepStale() []Key {
	c.mu.Lock()
	defer c.mu.Unlock()
	cutoff := c.now().Add(-c.window)
	var removed []Key
	for k, last := range c.seen {
		if last.Before(cutoff) {
			removed = append(removed, k)
			delete(c.seen, k)
		}
	}
	sort.Slice(removed, func(i, j int) bool { return removed[i].String() < removed[j].String() })
	return removed
}
