package metadata

import (
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestTTLCache_SetAndGet(t *testing.T) {
	c := newTTLCache(time.Minute)
	c.set("key1", "value1")
	v, ok := c.get("key1")
	if !ok {
		t.Fatal("expected cache hit")
	}
	if v.(string) != "value1" {
		t.Errorf("want 'value1', got %q", v)
	}
}

func TestTTLCache_Miss(t *testing.T) {
	c := newTTLCache(time.Minute)
	_, ok := c.get("missing")
	if ok {
		t.Error("expected cache miss for unknown key")
	}
}

func TestTTLCache_Expiry(t *testing.T) {
	c := newTTLCache(time.Nanosecond)
	c.set("k", "v")
	time.Sleep(2 * time.Millisecond)
	_, ok := c.get("k")
	if ok {
		t.Error("expected cache miss after TTL expiry")
	}
}

func TestTTLCache_Cleanup(t *testing.T) {
	c := newTTLCache(time.Nanosecond)
	c.set("a", 1)
	c.set("b", 2)
	time.Sleep(2 * time.Millisecond)
	c.cleanup()

	c.state.mu.RLock()
	n := len(c.state.items)
	c.state.mu.RUnlock()
	if n != 0 {
		t.Errorf("expected 0 items after cleanup, got %d", n)
	}
}

// TestTTLCache_EvictsByCount fills the cache past its cap and asserts the
// entry count never exceeds the cap. Backstops finding 11: between the
// hourly janitor sweeps a hot fan-out could grow the items map without
// bound.
func TestTTLCache_EvictsByCount(t *testing.T) {
	const cap = 4
	c := newTTLCacheWithCap(time.Hour, cap)
	for i := 0; i < cap*3; i++ {
		c.set(strconv.Itoa(i), i)
	}
	c.state.mu.RLock()
	n := len(c.state.items)
	c.state.mu.RUnlock()
	if n > cap {
		t.Errorf("len(items) = %d, want <= %d", n, cap)
	}
}

// TestTTLCache_EvictionPolicyPicksEarliestExpiry stages entries with
// staggered TTLs, fills to cap with a brand-new entry, and asserts the
// overflow eviction targets the entry that would have expired soonest,
// not the most-recently-added.
func TestTTLCache_EvictionPolicyPicksEarliestExpiry(t *testing.T) {
	c := newTTLCacheWithCap(time.Hour, 3)
	// Manually plant entries with controlled expiresAt values so the
	// test doesn't race the wall clock.
	now := time.Now()
	c.state.mu.Lock()
	c.state.items["soonest"] = cacheItem{value: "s", expiresAt: now.Add(10 * time.Millisecond)}
	c.state.items["middle"] = cacheItem{value: "m", expiresAt: now.Add(1 * time.Hour)}
	c.state.items["latest"] = cacheItem{value: "l", expiresAt: now.Add(2 * time.Hour)}
	c.state.mu.Unlock()

	// This set overflows the cap. The earliest-expiry entry should go.
	c.set("fresh", "f")

	c.state.mu.RLock()
	_, soonestPresent := c.state.items["soonest"]
	_, middlePresent := c.state.items["middle"]
	_, latestPresent := c.state.items["latest"]
	_, freshPresent := c.state.items["fresh"]
	c.state.mu.RUnlock()

	if soonestPresent {
		t.Error("expected earliest-expiring entry 'soonest' to be evicted")
	}
	if !middlePresent {
		t.Error("expected 'middle' to survive eviction")
	}
	if !latestPresent {
		t.Error("expected 'latest' to survive eviction")
	}
	if !freshPresent {
		t.Error("expected just-inserted 'fresh' to survive eviction")
	}
}

// TestTTLCache_TTLStillEvicts pins the original TTL semantics: even with
// the cap policy added, a past-TTL get is still a miss. Catches the
// regression of skipping the time.After check after a successful map
// lookup.
func TestTTLCache_TTLStillEvicts(t *testing.T) {
	c := newTTLCacheWithCap(time.Nanosecond, 100)
	c.set("k", "v")
	time.Sleep(2 * time.Millisecond)
	if _, ok := c.get("k"); ok {
		t.Error("expected cache miss after TTL expiry under capped cache")
	}
}

// TestTTLCache_EvictionRaceSafe pounds the cache from multiple goroutines
// at saturation so go test -race can flag any read-write overlap on the
// eviction path. evictEarliestLocked iterates the map while holding the
// write lock, but a regression that downgraded the lock or reordered the
// branch would surface here.
func TestTTLCache_EvictionRaceSafe(t *testing.T) {
	c := newTTLCacheWithCap(time.Minute, 8)
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				key := "w" + strconv.Itoa(worker) + "-" + strconv.Itoa(i)
				c.set(key, i)
				_, _ = c.get(key)
			}
		}(w)
	}
	wg.Wait()
	c.state.mu.RLock()
	n := len(c.state.items)
	c.state.mu.RUnlock()
	if n > 8 {
		t.Errorf("len(items) = %d after concurrent writes, want <= 8", n)
	}
}

// TestTTLCache_JanitorExitsOnGC verifies the runtime.AddCleanup hook
// closes the done channel so the janitor goroutine doesn't leak when the
// cache value is collected. Without this, every test that constructs an
// Aggregator (which embeds a ttlCache) used to leak one goroutine each,
// which compounded into the internal/api 10-min CI timeout (#73).
func TestTTLCache_JanitorExitsOnGC(t *testing.T) {
	// Capture the done channel before we lose the cache reference.
	c := newTTLCache(time.Hour)
	done := c.done

	// Drop the only reference and force GC + cleanup execution.
	c = nil
	runtime.GC()
	runtime.GC() // a second pass ensures AddCleanup callbacks have fired

	select {
	case <-done:
		// expected: done closed by the cleanup callback
	case <-time.After(2 * time.Second):
		t.Fatal("done channel was not closed within 2s of GC; janitor would leak")
	}
}

// fillCache plants n entries with strictly increasing expiry (entry i expires
// i seconds after base), bypassing set so the test controls expiresAt.
func fillCache(c *ttlCache, n int, base time.Time) {
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	for i := 0; i < n; i++ {
		c.state.items[strconv.Itoa(i)] = cacheItem{value: i, expiresAt: base.Add(time.Duration(i) * time.Second)}
	}
}

// TestTTLCache_EvictionInspectsBoundedSample is the lock hold time bound: one
// overflowing set under the write lock inspects at most evictSampleSize
// entries however large the cache is. The old policy inspected every entry,
// so on a 100k entry cache this reported about 100000.
func TestTTLCache_EvictionInspectsBoundedSample(t *testing.T) {
	const n = 100000
	c := newTTLCacheWithCap(time.Hour, n)
	fillCache(c, n, time.Now().Add(time.Hour))

	c.state.mu.Lock()
	seen := c.state.evictEarliestLocked("not-present")
	size := len(c.state.items)
	c.state.mu.Unlock()

	if seen > 64 {
		t.Errorf("eviction inspected %d entries, want at most 64 on a %d entry cache", seen, n)
	}
	if size != n-1 {
		t.Errorf("eviction removed %d entries, want exactly 1", n-size)
	}
}

// TestTTLCache_OverflowSetStaysAtCapAndKeepsWrite drives the real set path on a
// large cache: the count stays at the cap and the new key survives.
func TestTTLCache_OverflowSetStaysAtCapAndKeepsWrite(t *testing.T) {
	const n = 20000
	c := newTTLCacheWithCap(time.Hour, n)
	fillCache(c, n, time.Now().Add(time.Hour))

	for i := 0; i < 500; i++ {
		key := "new-" + strconv.Itoa(i)
		c.set(key, i)
		if _, ok := c.get(key); !ok {
			t.Fatalf("freshly set %q was evicted by its own write", key)
		}
	}
	c.state.mu.RLock()
	size := len(c.state.items)
	c.state.mu.RUnlock()
	if size != n {
		t.Errorf("len(items) = %d, want the cap %d", size, n)
	}
}

// TestTTLCache_SampledVictimIsAmongEarlierExpiring checks the approximation
// is a useful one: the victim of a sampled eviction comes from the earlier
// expiring half of a large cache (failure odds about 2e-10 per run).
func TestTTLCache_SampledVictimIsAmongEarlierExpiring(t *testing.T) {
	const n = 10000
	c := newTTLCacheWithCap(time.Hour, n)
	fillCache(c, n, time.Now().Add(time.Hour))

	c.state.mu.Lock()
	c.state.evictEarliestLocked("not-present")
	var victim = -1
	for i := 0; i < n; i++ {
		if _, ok := c.state.items[strconv.Itoa(i)]; !ok {
			victim = i
			break
		}
	}
	c.state.mu.Unlock()

	if victim < 0 {
		t.Fatal("no entry was evicted")
	}
	if victim >= n/2 {
		t.Errorf("victim %d came from the later expiring half of %d entries", victim, n)
	}
}

// BenchmarkTTLCache_SetOverflow measures the cost of a set that has to evict,
// on a full 10000 entry cache (the production cap) and on a 100000 entry one.
// The old full scan grew linearly with the cache, the sample does not.
func BenchmarkTTLCache_SetOverflow(b *testing.B) {
	for _, n := range []int{10000, 100000} {
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			c := newTTLCacheWithCap(time.Hour, n)
			fillCache(c, n, time.Now().Add(time.Hour))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				c.set("bench-"+strconv.Itoa(i), i)
			}
		})
	}
}
