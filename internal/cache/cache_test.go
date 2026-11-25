package cache

import (
	"errors"
	"fmt"
	"strconv"
	"sync"
	"testing"
)

// newTestCache is a helper that fails the test immediately on error.
func newTestCache(t *testing.T, capacity, shards int) *Cache {
	t.Helper()
	c, err := New(capacity, shards)
	if err != nil {
		t.Fatalf("New(%d, %d): %v", capacity, shards, err)
	}
	return c
}

func TestSetAndGet(t *testing.T) {
	c := newTestCache(t, 100, 4)

	if created := c.Set("name", "kv"); !created {
		t.Fatal("first Set should report created=true")
	}
	got, ok := c.Get("name")
	if !ok || got != "kv" {
		t.Fatalf("Get(name) = %q, %v; want \"kv\", true", got, ok)
	}
}

func TestGetMissing(t *testing.T) {
	c := newTestCache(t, 100, 4)
	if _, ok := c.Get("missing"); ok {
		t.Fatal("expected miss for unknown key")
	}
}

func TestUpdateExistingKey(t *testing.T) {
	c := newTestCache(t, 100, 4)
	c.Set("k", "v1")
	if created := c.Set("k", "v2"); created {
		t.Fatal("second Set should report created=false")
	}
	if got, _ := c.Get("k"); got != "v2" {
		t.Fatalf("Get(k) = %q; want v2", got)
	}
	if c.Len() != 1 {
		t.Fatalf("Len() = %d; want 1", c.Len())
	}
}

func TestEmptyValueIsAHit(t *testing.T) {
	c := newTestCache(t, 10, 1)
	c.Set("empty", "")
	if got, ok := c.Get("empty"); !ok || got != "" {
		t.Fatalf("Get(empty) = %q, %v; want \"\", true", got, ok)
	}
}

func TestNewValidatesCapacity(t *testing.T) {
	for _, capacity := range []int{0, -1} {
		if _, err := New(capacity, 4); !errors.Is(err, ErrInvalidCapacity) {
			t.Fatalf("New(%d) error = %v; want ErrInvalidCapacity", capacity, err)
		}
	}
}

func TestShardCountIsPowerOfTwoAndFitsCapacity(t *testing.T) {
	tests := []struct {
		capacity, shards, wantShards int
	}{
		{capacity: 1000, shards: 0, wantShards: DefaultShards},
		{capacity: 1000, shards: 5, wantShards: 8},
		{capacity: 1000, shards: 16, wantShards: 16},
		{capacity: 3, shards: 16, wantShards: 2}, // shrinks so no shard is empty
		{capacity: 1, shards: 16, wantShards: 1},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("cap=%d,shards=%d", tt.capacity, tt.shards), func(t *testing.T) {
			c := newTestCache(t, tt.capacity, tt.shards)
			st := c.Stats()
			if st.Shards != tt.wantShards {
				t.Fatalf("Shards = %d; want %d", st.Shards, tt.wantShards)
			}
			if st.Capacity != tt.capacity {
				t.Fatalf("total Capacity = %d; want exactly %d", st.Capacity, tt.capacity)
			}
		})
	}
}

func TestNeverExceedsCapacity(t *testing.T) {
	const capacity = 500
	c := newTestCache(t, capacity, 8)
	for i := range 10_000 {
		c.Set("key-"+strconv.Itoa(i), "v")
	}
	if n := c.Len(); n > capacity {
		t.Fatalf("Len() = %d; must not exceed capacity %d", n, capacity)
	}
	if st := c.Stats(); st.Evictions == 0 {
		t.Fatal("expected evictions once the cache was full")
	}
}

// With a single shard the clock behaviour is fully deterministic, so we can
// check exactly which key is evicted.
func TestClockGivesRecentlyReadKeysASecondChance(t *testing.T) {
	c := newTestCache(t, 3, 1)
	c.Set("a", "1")
	c.Set("b", "2")
	c.Set("c", "3")

	// Inserting "d" sweeps the ring: all three start referenced, so the
	// hand clears a, b, c and evicts "a" on its second pass.
	c.Set("d", "4")
	if _, ok := c.Get("a"); ok {
		t.Fatal("expected a to be evicted first")
	}

	// Now b and c have their bits cleared. Reading b marks it as used, so
	// the next eviction must skip b and take c instead.
	c.Get("b")
	c.Set("e", "5")

	if _, ok := c.Get("b"); !ok {
		t.Fatal("b was read recently and should have survived")
	}
	if _, ok := c.Get("c"); ok {
		t.Fatal("c was not used and should have been evicted")
	}
	for _, k := range []string{"d", "e"} {
		if _, ok := c.Get(k); !ok {
			t.Fatalf("%s should be present", k)
		}
	}
}

func TestStatsCountsHitsMissesEvictions(t *testing.T) {
	c := newTestCache(t, 1, 1)
	c.Set("a", "1")
	c.Get("a")       // hit
	c.Get("missing") // miss
	c.Set("b", "2")  // evicts a

	st := c.Stats()
	want := Stats{Entries: 1, Capacity: 1, Shards: 1, Hits: 1, Misses: 1, Evictions: 1}
	if st != want {
		t.Fatalf("Stats() = %+v; want %+v", st, want)
	}
}

// Run with `go test -race` to prove there are no data races when readers and
// writers hammer the same keys from many goroutines.
func TestConcurrentAccess(t *testing.T) {
	c := newTestCache(t, 1_000, 8)
	const goroutines, ops = 32, 2_000

	var wg sync.WaitGroup
	for g := range goroutines {
		wg.Go(func() {
			for i := range ops {
				key := "k" + strconv.Itoa((g*ops+i)%1_500) // more keys than capacity
				if i%4 == 0 {
					c.Set(key, key)
				} else if v, ok := c.Get(key); ok && v != key {
					t.Errorf("Get(%s) = %s; values must never mix between keys", key, v)
				}
			}
		})
	}
	wg.Wait()

	if n := c.Len(); n > 1_000 {
		t.Fatalf("Len() = %d; exceeded capacity under concurrency", n)
	}
}

func TestNextPowerOfTwo(t *testing.T) {
	cases := map[int]int{1: 1, 2: 2, 3: 4, 5: 8, 64: 64, 65: 128}
	for in, want := range cases {
		if got := nextPowerOfTwo(in); got != want {
			t.Errorf("nextPowerOfTwo(%d) = %d; want %d", in, got, want)
		}
	}
}
