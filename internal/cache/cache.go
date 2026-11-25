// Package cache implements a sharded, concurrency-safe, in-memory key/value
// store with a fixed capacity.
//
// # Design
//
// Sharding: keys are spread across N independent shards (N is a power of
// two). Each shard has its own sync.RWMutex, so goroutines touching different
// shards never wait for each other. With many shards, lock contention stays
// low even when every CPU core is serving requests.
//
// Read-friendly locking: Get only takes the shard's *read* lock, so any
// number of readers can run in parallel on the same shard. That works because
// the eviction policy below never needs to reorder data on a read.
//
// Eviction (CLOCK / "second chance"): a shard's entries live in a ring of
// slots. Every slot has a "referenced" bit that Get sets atomically. When the
// shard is full, a "hand" sweeps the ring: a slot with the bit set gets a
// second chance (bit cleared, hand moves on); the first slot with the bit
// clear is evicted. Hot keys keep getting their bit set and survive, so the
// result closely approximates LRU without LRU's write-on-every-read cost.
//
// Hashing: keys are hashed with hash/maphash, which is fast, allocation-free
// and seeded randomly per process. The random seed means an attacker cannot
// precompute keys that all land in one shard (hash-flooding).
package cache

import (
	"errors"
	"hash/maphash"
	"sync"
	"sync/atomic"
)

// DefaultShards is the shard count used when the caller passes 0.
// 256 shards keep per-shard contention low on machines with many cores while
// costing only a few KB of bookkeeping.
const DefaultShards = 256

// ErrInvalidCapacity is returned by New when capacity is not positive.
var ErrInvalidCapacity = errors.New("cache: capacity must be a positive integer")

// Cache is a sharded key/value store. The zero value is not usable; create
// one with New. All methods are safe for concurrent use.
type Cache struct {
	shards []*shard
	mask   uint64 // len(shards)-1; `hash & mask` picks a shard (cheaper than %)
	seed   maphash.Seed
}

// Stats is a point-in-time snapshot of cache usage.
type Stats struct {
	Entries   int    `json:"entries"`
	Capacity  int    `json:"capacity"`
	Shards    int    `json:"shards"`
	Hits      uint64 `json:"hits"`
	Misses    uint64 `json:"misses"`
	Evictions uint64 `json:"evictions"`
}

// New creates a cache holding at most `capacity` entries in total, split
// across `shards` shards. shards is rounded up to a power of two; 0 selects
// DefaultShards. If capacity is smaller than the shard count, fewer shards are
// used so that every shard can hold at least one entry.
func New(capacity, shards int) (*Cache, error) {
	if capacity < 1 {
		return nil, ErrInvalidCapacity
	}
	if shards < 1 {
		shards = DefaultShards
	}

	n := nextPowerOfTwo(shards)
	for n > capacity {
		n /= 2
	}

	c := &Cache{
		shards: make([]*shard, n),
		mask:   uint64(n - 1), // #nosec G115 -- n is always >= 1
		seed:   maphash.MakeSeed(),
	}

	// Spread capacity exactly: the first `capacity % n` shards get one extra
	// slot, so the total is exactly `capacity`.
	base, extra := capacity/n, capacity%n
	for i := range c.shards {
		shardCap := base
		if i < extra {
			shardCap++
		}
		c.shards[i] = newShard(shardCap)
	}
	return c, nil
}

// Get returns the value stored for key and whether it was found.
func (c *Cache) Get(key string) (string, bool) {
	return c.shardFor(key).get(key)
}

// Set stores value under key, evicting another entry if the key's shard is
// full. It reports whether the key was newly created (false = updated).
func (c *Cache) Set(key, value string) bool {
	return c.shardFor(key).set(key, value)
}

// Len returns the number of entries currently stored.
func (c *Cache) Len() int {
	total := 0
	for _, s := range c.shards {
		s.mu.RLock()
		total += len(s.index)
		s.mu.RUnlock()
	}
	return total
}

// Stats returns aggregated counters across all shards.
func (c *Cache) Stats() Stats {
	st := Stats{Shards: len(c.shards)}
	for _, s := range c.shards {
		s.mu.RLock()
		st.Entries += len(s.index)
		st.Capacity += s.capacity
		s.mu.RUnlock()
		st.Hits += s.hits.Load()
		st.Misses += s.misses.Load()
		st.Evictions += s.evictions.Load()
	}
	return st
}

// shardFor maps a key to its shard.
func (c *Cache) shardFor(key string) *shard {
	return c.shards[maphash.String(c.seed, key)&c.mask]
}

// entry is one slot in a shard's ring.
type entry struct {
	key   string
	value string
	// referenced is 1 if the entry was used since the clock hand last passed
	// it. It is read/written with sync/atomic so Get can set it while holding
	// only the read lock.
	referenced uint32
}

// shard is one independently locked partition of the cache.
type shard struct {
	mu       sync.RWMutex
	index    map[string]int // key -> position in slots
	slots    []entry        // ring buffer; grows up to capacity, then reused
	hand     int            // next slot the clock hand will inspect
	capacity int

	// Counters are atomic so they can be bumped without the write lock.
	hits      atomic.Uint64
	misses    atomic.Uint64
	evictions atomic.Uint64

	// Padding keeps neighbouring shards' hot fields on different CPU cache
	// lines, avoiding "false sharing" between cores.
	_ [64]byte
}

func newShard(capacity int) *shard {
	// Pre-size the map modestly: allocating the full capacity up front would
	// reserve memory that a lightly used cache never needs.
	hint := min(capacity, 1024)
	return &shard{
		index:    make(map[string]int, hint),
		slots:    make([]entry, 0, hint),
		capacity: capacity,
	}
}

func (s *shard) get(key string) (string, bool) {
	s.mu.RLock()
	i, ok := s.index[key]
	if !ok {
		s.mu.RUnlock()
		s.misses.Add(1)
		return "", false
	}

	e := &s.slots[i]
	// Only write when the bit is clear: an unconditional store would make
	// every reader dirty the same CPU cache line, even for already-hot keys.
	if atomic.LoadUint32(&e.referenced) == 0 {
		atomic.StoreUint32(&e.referenced, 1)
	}
	value := e.value
	s.mu.RUnlock()

	s.hits.Add(1)
	return value, true
}

func (s *shard) set(key, value string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Update in place.
	if i, ok := s.index[key]; ok {
		s.slots[i].value = value
		atomic.StoreUint32(&s.slots[i].referenced, 1)
		return false
	}

	// Free space left: append a new slot. New entries start "referenced" so
	// they survive at least one sweep of the clock hand.
	if len(s.slots) < s.capacity {
		s.slots = append(s.slots, entry{key: key, value: value, referenced: 1})
		s.index[key] = len(s.slots) - 1
		return true
	}

	// Full: reuse the slot chosen by the clock algorithm.
	victim := s.evict()
	delete(s.index, s.slots[victim].key)
	s.slots[victim] = entry{key: key, value: value, referenced: 1}
	s.index[key] = victim
	s.evictions.Add(1)
	return true
}

// evict advances the clock hand until it finds a slot whose referenced bit is
// clear, clearing bits along the way. It always terminates within two passes
// over the ring because every bit it skips is cleared. Caller holds s.mu.
func (s *shard) evict() int {
	for {
		i := s.hand
		s.hand = (s.hand + 1) % len(s.slots)

		if atomic.LoadUint32(&s.slots[i].referenced) == 0 {
			return i
		}
		atomic.StoreUint32(&s.slots[i].referenced, 0)
	}
}

// nextPowerOfTwo returns the smallest power of two >= n (n >= 1).
func nextPowerOfTwo(n int) int {
	p := 1
	for p < n {
		p <<= 1
	}
	return p
}
