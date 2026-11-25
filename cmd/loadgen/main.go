// Command loadgen load-tests a running kvcache server.
//
// Example:
//
//	loadgen -url http://127.0.0.1:7171 -c 100 -d 10s -read 90
//
// It runs three scenarios (GET only, PUT only, and the -read mix) and prints
// throughput and latency percentiles for each.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"go-kv-cache/internal/loadtest"
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, out io.Writer) error {
	flags := flag.NewFlagSet("loadgen", flag.ContinueOnError)
	flags.SetOutput(out)
	target := flags.String("url", "http://127.0.0.1:7171", "base URL of the server")
	concurrency := flags.Int("c", 100, "concurrent connections")
	duration := flags.Duration("d", 10*time.Second, "duration of each scenario")
	keys := flags.Int("keys", 10_000, "number of distinct keys")
	read := flags.Int("read", 90, "percentage of reads in the mixed scenario")
	if err := flags.Parse(args); err != nil {
		return err
	}

	scenarios := []struct {
		name    string
		readPct int
	}{
		{"GET only", 100},
		{"PUT only", 0},
		{fmt.Sprintf("mixed %d/%d", *read, 100-*read), *read},
	}

	_, _ = fmt.Fprintf(out, "target=%s connections=%d duration=%s keys=%d\n",
		*target, *concurrency, *duration, *keys)

	for i, s := range scenarios {
		res, err := loadtest.Run(ctx, loadtest.Options{
			URL:         *target,
			Concurrency: *concurrency,
			Duration:    *duration,
			Keys:        *keys,
			ReadPercent: s.readPct,
			Prefill:     i == 0, // fill keys once so reads are cache hits
		})
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(out, "%-14s %s\n", s.name, res)
	}
	return nil
}
