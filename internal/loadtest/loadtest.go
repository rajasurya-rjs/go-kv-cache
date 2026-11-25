// Package loadtest is a small HTTP load generator for the cache API.
//
// N workers each keep one keep-alive connection busy for a fixed duration,
// sending a configurable mix of GET /get and POST /put requests over a fixed
// key space. Every request's latency is recorded so the result can report
// percentiles (p50/p90/p99), not just an average that hides slow outliers.
package loadtest

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/url"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/valyala/fasthttp"
)

// Options configures a load test run.
type Options struct {
	URL         string        // base URL, e.g. http://127.0.0.1:7171
	Concurrency int           // number of parallel workers / connections
	Duration    time.Duration // how long to generate load
	Keys        int           // size of the key space
	ReadPercent int           // 0-100; the rest of the requests are writes
	Prefill     bool          // insert every key once before measuring
}

// Result summarizes a run.
type Result struct {
	Requests uint64
	Errors   uint64 // transport failures and non-200 responses
	Elapsed  time.Duration
	P50      time.Duration
	P90      time.Duration
	P99      time.Duration
	Max      time.Duration
}

// RPS returns the achieved requests per second.
func (r Result) RPS() float64 {
	if r.Elapsed <= 0 {
		return 0
	}
	return float64(r.Requests) / r.Elapsed.Seconds()
}

// String formats the result as a one-line summary.
func (r Result) String() string {
	return fmt.Sprintf("%.0f req/s  requests=%d errors=%d  p50=%s p90=%s p99=%s max=%s",
		r.RPS(), r.Requests, r.Errors, r.P50, r.P90, r.P99, r.Max)
}

// Run executes the load test and blocks until it finishes or ctx is done.
func Run(ctx context.Context, opts Options) (Result, error) {
	if opts.Concurrency < 1 || opts.Keys < 1 || opts.Duration <= 0 {
		return Result{}, errors.New("loadtest: concurrency, keys and duration must be positive")
	}
	if opts.ReadPercent < 0 || opts.ReadPercent > 100 {
		return Result{}, errors.New("loadtest: read percent must be between 0 and 100")
	}
	u, err := url.Parse(opts.URL)
	if err != nil || u.Host == "" {
		return Result{}, fmt.Errorf("loadtest: invalid URL %q", opts.URL)
	}

	// HostClient talks to one host and pools connections to it.
	client := &fasthttp.HostClient{
		Addr:     u.Host,
		MaxConns: opts.Concurrency,
	}
	base := u.Scheme + "://" + u.Host

	if opts.Prefill {
		if err := prefill(client, base, opts.Keys); err != nil {
			return Result{}, err
		}
	}

	ctx, cancel := context.WithTimeout(ctx, opts.Duration)
	defer cancel()

	var errorCount atomic.Uint64
	latencies := make([][]time.Duration, opts.Concurrency)
	var wg sync.WaitGroup
	start := time.Now()

	for w := range opts.Concurrency {
		wg.Go(func() {
			latencies[w] = worker(ctx, client, base, opts, uint64(w), &errorCount) // #nosec G115 -- w >= 0
		})
	}
	wg.Wait()
	elapsed := time.Since(start)

	return summarize(latencies, errorCount.Load(), elapsed), nil
}

// worker sends requests until ctx is done and returns their latencies.
func worker(ctx context.Context, client *fasthttp.HostClient, base string, opts Options,
	id uint64, errorCount *atomic.Uint64) []time.Duration {
	// A per-worker random source avoids lock contention on a shared one.
	rng := rand.New(rand.NewPCG(id, uint64(time.Now().UnixNano()))) // #nosec G404 G115 -- load test keys, not security

	// Request/response objects are reused for every request this worker
	// sends, so the generator itself allocates almost nothing.
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	samples := make([]time.Duration, 0, 4096)
	for ctx.Err() == nil {
		key := "key:" + strconv.Itoa(rng.IntN(opts.Keys))
		req.Reset()
		if rng.IntN(100) < opts.ReadPercent {
			req.SetRequestURI(base + "/get?key=" + url.QueryEscape(key))
		} else {
			req.SetRequestURI(base + "/put")
			req.Header.SetMethod(fasthttp.MethodPost)
			req.Header.SetContentType("application/json")
			req.SetBodyString(`{"key":"` + key + `","value":"value-` + strconv.Itoa(rng.IntN(1_000_000)) + `"}`)
		}

		t0 := time.Now()
		err := client.Do(req, resp)
		samples = append(samples, time.Since(t0))
		if err != nil || resp.StatusCode() != fasthttp.StatusOK {
			errorCount.Add(1)
		}
	}
	return samples
}

// prefill inserts every key once so reads measure hits, not misses.
func prefill(client *fasthttp.HostClient, base string, keys int) error {
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	for i := range keys {
		req.Reset()
		req.SetRequestURI(base + "/put")
		req.Header.SetMethod(fasthttp.MethodPost)
		req.SetBodyString(`{"key":"key:` + strconv.Itoa(i) + `","value":"value"}`)
		if err := client.Do(req, resp); err != nil {
			return fmt.Errorf("loadtest: prefill: %w", err)
		}
		if resp.StatusCode() != fasthttp.StatusOK {
			return fmt.Errorf("loadtest: prefill: status %d", resp.StatusCode())
		}
	}
	return nil
}

// summarize merges per-worker samples and computes percentiles.
func summarize(perWorker [][]time.Duration, errorCount uint64, elapsed time.Duration) Result {
	var all []time.Duration
	for _, s := range perWorker {
		all = append(all, s...)
	}
	r := Result{Requests: uint64(len(all)), Errors: errorCount, Elapsed: elapsed}
	if len(all) == 0 {
		return r
	}
	slices.Sort(all)
	r.P50 = percentile(all, 50)
	r.P90 = percentile(all, 90)
	r.P99 = percentile(all, 99)
	r.Max = all[len(all)-1]
	return r
}

// percentile returns the p-th percentile of sorted samples (nearest rank).
func percentile(sorted []time.Duration, p int) time.Duration {
	idx := (len(sorted)*p + 99) / 100 // ceil(len*p/100)
	if idx < 1 {
		idx = 1
	}
	return sorted[idx-1]
}
