package main

import (
	"bytes"
	"context"
	"net"
	"strings"
	"testing"

	"go-kv-cache/internal/api"
	"go-kv-cache/internal/cache"
	"go-kv-cache/internal/config"
)

func TestRunPrintsAllScenarios(t *testing.T) {
	cfg, _ := config.Load(func(string) string { return "" })
	c, _ := cache.New(1_000, 4)
	srv := api.NewServer(cfg, c)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Shutdown() }()

	var out bytes.Buffer
	args := []string{"-url", "http://" + ln.Addr().String(), "-c", "2", "-d", "100ms", "-keys", "50"}
	if err := run(context.Background(), args, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"GET only", "PUT only", "mixed 90/10", "req/s"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, out.String())
		}
	}
}

func TestRunRejectsBadFlags(t *testing.T) {
	if err := run(context.Background(), []string{"-c", "x"}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected flag error")
	}
	if err := run(context.Background(), []string{"-c", "0"}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected validation error")
	}
}
