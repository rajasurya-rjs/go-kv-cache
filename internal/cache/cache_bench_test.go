package cache

import (
	"strconv"
	"testing"
)

// Benchmarks: run with `make bench` or `go test -bench=. -benchmem ./internal/cache`.
// The *Parallel variants use every CPU core, which is where sharding pays off.

const benchKeys = 100_000

func benchCache(b *testing.B) (*Cache, []string) {
	b.Helper()
	c, err := New(benchKeys, DefaultShards)
	if err != nil {
		b.Fatal(err)
	}
	keys := make([]string, benchKeys)
	for i := range keys {
		keys[i] = "key:" + strconv.Itoa(i)
		c.Set(keys[i], "value")
	}
	return c, keys
}

func BenchmarkGet(b *testing.B) {
	c, keys := benchCache(b)
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		c.Get(keys[i%benchKeys])
		i++
	}
}

func BenchmarkSet(b *testing.B) {
	c, keys := benchCache(b)
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		c.Set(keys[i%benchKeys], "updated")
		i++
	}
}

func BenchmarkGetParallel(b *testing.B) {
	c, keys := benchCache(b)
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			c.Get(keys[i%benchKeys])
			i++
		}
	})
}

// 90% reads / 10% writes, a typical cache workload.
func BenchmarkMixedParallel(b *testing.B) {
	c, keys := benchCache(b)
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			key := keys[i%benchKeys]
			if i%10 == 0 {
				c.Set(key, "updated")
			} else {
				c.Get(key)
			}
			i++
		}
	})
}
