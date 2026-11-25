package main

import (
	"context"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/valyala/fasthttp"
)

// freePort asks the OS for an unused TCP port.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	return ln.Addr().(*net.TCPAddr).Port
}

func envWith(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

// startServer runs `run` in a goroutine and returns its address, a function
// that stops it, and a channel with run's final error.
func startServer(t *testing.T, port int) (string, context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan string, 1)
	done := make(chan error, 1)

	env := envWith(map[string]string{"HOST": "127.0.0.1", "PORT": strconv.Itoa(port)})
	go func() {
		done <- run(ctx, nil, env, io.Discard, func(addr string) { ready <- addr })
	}()

	select {
	case addr := <-ready:
		return addr, cancel, done
	case err := <-done:
		cancel()
		t.Fatalf("server failed to start: %v", err)
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("server did not start in time")
	}
	return "", cancel, done
}

func TestRunServesAndShutsDownGracefully(t *testing.T) {
	port := freePort(t)
	addr, stop, done := startServer(t, port)

	status, body, err := fasthttp.Get(nil, "http://"+addr+"/health")
	if err != nil || status != 200 || string(body) != `{"status":"healthy"}` {
		t.Fatalf("GET /health = %d %s %v", status, body, err)
	}

	// The -healthcheck mode must succeed against the running server.
	env := envWith(map[string]string{"HOST": "0.0.0.0", "PORT": strconv.Itoa(port)})
	if err := run(context.Background(), []string{"-healthcheck"}, env, io.Discard, nil); err != nil {
		t.Fatalf("healthcheck against running server failed: %v", err)
	}

	stop()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run returned %v after shutdown; want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not shut down")
	}
}

func TestHealthcheckFailsWhenServerIsDown(t *testing.T) {
	env := envWith(map[string]string{"PORT": strconv.Itoa(freePort(t))})
	if err := run(context.Background(), []string{"-healthcheck"}, env, io.Discard, nil); err == nil {
		t.Fatal("healthcheck should fail when nothing is listening")
	}
}

func TestRunReportsPortInUse(t *testing.T) {
	port := freePort(t)
	_, stop, done := startServer(t, port)
	defer func() {
		stop()
		if err := <-done; err != nil {
			t.Errorf("first server shut down with error: %v", err)
		}
	}()

	env := envWith(map[string]string{"HOST": "127.0.0.1", "PORT": strconv.Itoa(port)})
	err := run(context.Background(), nil, env, io.Discard, nil)
	if err == nil || !strings.Contains(err.Error(), "listen") {
		t.Fatalf("expected listen error, got %v", err)
	}
}

func TestRunRejectsBadConfigAndFlags(t *testing.T) {
	if err := run(context.Background(), nil, envWith(map[string]string{"PORT": "nope"}), io.Discard, nil); err == nil {
		t.Fatal("expected config error")
	}
	if err := run(context.Background(), []string{"-unknown"}, envWith(nil), io.Discard, nil); err == nil {
		t.Fatal("expected flag parse error")
	}
}
