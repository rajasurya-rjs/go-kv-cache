// Command kvcache runs the in-memory key/value cache HTTP server.
//
// Usage:
//
//	kvcache               start the server (configured via env vars, see internal/config)
//	kvcache -healthcheck  probe /health of a local server; exit 0 if healthy
//
// The -healthcheck mode exists so the Docker image can be built FROM scratch
// (no shell, no curl) and still have a working HEALTHCHECK.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/valyala/fasthttp"

	"go-kv-cache/internal/api"
	"go-kv-cache/internal/cache"
	"go-kv-cache/internal/config"
)

func main() {
	// ctx is cancelled on Ctrl+C (SIGINT) or `docker stop` / Kubernetes
	// (SIGTERM), which triggers a graceful shutdown in run.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Args[1:], os.Getenv, os.Stdout, nil)
	stop() // release signal handling before exiting

	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// run contains everything main does, but takes its dependencies as
// parameters (args, env lookup, output, context) so tests can drive it.
// onListen, if non-nil, is called with the bound address once the server is
// accepting connections.
func run(
	ctx context.Context,
	args []string,
	getenv func(string) string,
	out io.Writer,
	onListen func(addr string),
) error {
	flags := flag.NewFlagSet("kvcache", flag.ContinueOnError)
	flags.SetOutput(out)
	healthcheck := flags.Bool("healthcheck", false, "probe /health on the local server and exit")
	if err := flags.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(getenv)
	if err != nil {
		return err
	}
	if *healthcheck {
		return probe(cfg)
	}

	// Note: since Go 1.25, GOMAXPROCS automatically respects container CPU
	// limits, so the server uses exactly the cores it is allowed to.
	store, err := cache.New(cfg.Capacity, cfg.Shards)
	if err != nil {
		return err
	}
	srv := api.NewServer(cfg, store)

	ln, err := net.Listen("tcp", cfg.Addr())
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.Addr(), err)
	}

	logger := log.New(out, "kvcache ", log.LstdFlags)
	logger.Printf("listening on %s (capacity=%d shards=%d)", ln.Addr(), cfg.Capacity, store.Stats().Shards)
	if onListen != nil {
		onListen(ln.Addr().String())
	}

	// Serve in the background; this goroutine reports if serving fails.
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	select {
	case err := <-serveErr:
		return fmt.Errorf("server stopped unexpectedly: %w", err)
	case <-ctx.Done():
	}

	// Graceful shutdown: stop accepting connections, let in-flight requests
	// finish, but never wait longer than ShutdownTimeout.
	logger.Println("shutting down gracefully...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := srv.ShutdownWithContext(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	logger.Println("stopped")
	return nil
}

// probe sends GET /health to the locally running server.
func probe(cfg config.Config) error {
	host := cfg.Host
	// A wildcard bind address isn't dialable; the server is reachable on
	// loopback in that case.
	if host == "0.0.0.0" || host == "::" || host == "" {
		host = "127.0.0.1"
	}
	url := "http://" + net.JoinHostPort(host, strconv.Itoa(cfg.Port)) + "/health"

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.SetRequestURI(url)
	if err := fasthttp.DoTimeout(req, resp, 2*time.Second); err != nil {
		return fmt.Errorf("healthcheck %s: %w", url, err)
	}
	if resp.StatusCode() != fasthttp.StatusOK {
		return errors.New("healthcheck " + url + ": status " + strconv.Itoa(resp.StatusCode()))
	}
	return nil
}
