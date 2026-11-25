package api

import (
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/valyala/fasthttp"
	"github.com/valyala/fasthttp/fasthttputil"

	"go-kv-cache/internal/cache"
	"go-kv-cache/internal/config"
)

// startInMemory runs the real fasthttp.Server over an in-memory listener:
// full HTTP parsing and connection handling, but no real TCP port.
func startInMemory(t *testing.T, cfg config.Config) *fasthttp.Client {
	t.Helper()
	c, err := cache.New(cfg.Capacity, cfg.Shards)
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(cfg, c)
	ln := fasthttputil.NewInmemoryListener()

	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Shutdown() })

	return &fasthttp.Client{
		Dial: func(string) (net.Conn, error) { return ln.Dial() },
	}
}

func testConfig(t *testing.T) config.Config {
	t.Helper()
	cfg, err := config.Load(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxBodyBytes = 128
	return cfg
}

func TestServerEndToEnd(t *testing.T) {
	client := startInMemory(t, testConfig(t))

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.SetRequestURI("http://kv/put")
	req.Header.SetMethod("POST")
	req.Header.SetContentType("application/json")
	req.SetBodyString(`{"key":"net","value":"ok"}`)
	if err := client.DoTimeout(req, resp, time.Second); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode() != 200 {
		t.Fatalf("PUT status %d", resp.StatusCode())
	}

	req.Reset()
	req.SetRequestURI("http://kv/get?key=net")
	if err := client.DoTimeout(req, resp, time.Second); err != nil {
		t.Fatal(err)
	}
	if string(resp.Body()) != `{"status":"OK","key":"net","value":"ok"}` {
		t.Fatalf("GET body %s", resp.Body())
	}
	if len(resp.Header.Peek("Server")) != 0 {
		t.Fatal("Server header should not be sent")
	}
}

func TestServerRejectsOversizedBody(t *testing.T) {
	client := startInMemory(t, testConfig(t))

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.SetRequestURI("http://kv/put")
	req.Header.SetMethod("POST")
	req.SetBodyString(`{"key":"k","value":"` + strings.Repeat("v", 500) + `"}`)
	// fasthttp rejects the body before our handler runs; serverError turns
	// that into a JSON 413.
	if err := client.DoTimeout(req, resp, time.Second); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode() != fasthttp.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d; want 413", resp.StatusCode())
	}
	if string(resp.Body()) != `{"status":"ERROR","message":"Request body too large."}` {
		t.Fatalf("body = %s", resp.Body())
	}
}

func TestServerErrorFallback(t *testing.T) {
	var ctx fasthttp.RequestCtx
	serverError(&ctx, errors.New("malformed request line"))
	if ctx.Response.StatusCode() != fasthttp.StatusBadRequest {
		t.Fatalf("status = %d; want 400", ctx.Response.StatusCode())
	}
	if string(ctx.Response.Body()) != `{"status":"ERROR","message":"Bad request."}` {
		t.Fatalf("body = %s", ctx.Response.Body())
	}
}
