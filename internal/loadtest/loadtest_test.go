package loadtest

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/valyala/fasthttp"

	"go-kv-cache/internal/api"
	"go-kv-cache/internal/cache"
	"go-kv-cache/internal/config"
)

// startServer runs the real API on a random local port for the test.
func startServer(t *testing.T) string {
	t.Helper()
	cfg, err := config.Load(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	c, err := cache.New(10_000, 16)
	if err != nil {
		t.Fatal(err)
	}
	srv := api.NewServer(cfg, c)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Shutdown() })
	return "http://" + ln.Addr().String()
}

func TestRunAgainstRealServer(t *testing.T) {
	url := startServer(t)
	res, err := Run(context.Background(), Options{
		URL: url, Concurrency: 4, Duration: 300 * time.Millisecond,
		Keys: 100, ReadPercent: 80, Prefill: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Requests == 0 {
		t.Fatal("expected some requests")
	}
	if res.Errors != 0 {
		t.Fatalf("expected no errors, got %d", res.Errors)
	}
	if res.P50 > res.P99 || res.P99 > res.Max {
		t.Fatalf("percentiles out of order: %s", res)
	}
	if res.RPS() <= 0 || res.String() == "" {
		t.Fatalf("bad summary: %s", res)
	}
}

func TestRunCountsErrors(t *testing.T) {
	// Point at a port with nothing listening: every request fails.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	_ = ln.Close()

	res, err := Run(context.Background(), Options{
		URL: "http://" + addr, Concurrency: 1, Duration: 100 * time.Millisecond, Keys: 10, ReadPercent: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Errors == 0 || res.Errors != res.Requests {
		t.Fatalf("every request should fail: %s", res)
	}
}

func TestRunValidatesOptions(t *testing.T) {
	bad := []Options{
		{URL: "http://x", Concurrency: 0, Duration: time.Second, Keys: 1},
		{URL: "http://x", Concurrency: 1, Duration: 0, Keys: 1},
		{URL: "http://x", Concurrency: 1, Duration: time.Second, Keys: 0},
		{URL: "http://x", Concurrency: 1, Duration: time.Second, Keys: 1, ReadPercent: 101},
		{URL: "not a url", Concurrency: 1, Duration: time.Second, Keys: 1},
	}
	for _, o := range bad {
		if _, err := Run(context.Background(), o); err == nil {
			t.Errorf("Run(%+v) should fail", o)
		}
	}
}

func TestPrefillFailsOnErrorStatus(t *testing.T) {
	// A server that rejects every request makes prefill fail fast.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &fasthttp.Server{Handler: func(ctx *fasthttp.RequestCtx) { ctx.SetStatusCode(500) }}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Shutdown() }()

	_, err = Run(context.Background(), Options{
		URL: "http://" + ln.Addr().String(), Concurrency: 1, Duration: time.Second, Keys: 1, Prefill: true,
	})
	if err == nil {
		t.Fatal("expected prefill error")
	}
}

func TestPercentile(t *testing.T) {
	samples := []time.Duration{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	if got := percentile(samples, 50); got != 5 {
		t.Errorf("p50 = %d; want 5", got)
	}
	if got := percentile(samples, 99); got != 10 {
		t.Errorf("p99 = %d; want 10", got)
	}
	if got := percentile(samples, 0); got != 1 {
		t.Errorf("p0 = %d; want 1", got)
	}
}

func TestSummarizeEmpty(t *testing.T) {
	r := summarize(nil, 0, 0)
	if r.Requests != 0 || r.RPS() != 0 {
		t.Fatalf("unexpected: %+v", r)
	}
}
