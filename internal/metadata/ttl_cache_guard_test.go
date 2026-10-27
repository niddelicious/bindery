package metadata

import (
	"testing"
	"time"
)

// TestTTLCache_EvictionNeverTakesTheJustInsertedKey pins the guard in
// evictEarliestLocked: even when the key just written has the earliest expiry
// of all, it is the other entry that goes.
func TestTTLCache_EvictionNeverTakesTheJustInsertedKey(t *testing.T) {
	c := newTTLCacheWithCap(time.Hour, 10)
	now := time.Now()
	c.state.mu.Lock()
	c.state.items["new"] = cacheItem{value: 1, expiresAt: now.Add(time.Minute)}
	c.state.items["old"] = cacheItem{value: 2, expiresAt: now.Add(time.Hour)}
	seen := c.state.evictEarliestLocked("new")
	_, newLeft := c.state.items["new"]
	_, oldLeft := c.state.items["old"]
	c.state.mu.Unlock()
	if seen != 1 || !newLeft || oldLeft {
		t.Errorf("seen=%d newLeft=%v oldLeft=%v, want 1 true false", seen, newLeft, oldLeft)
	}
}
