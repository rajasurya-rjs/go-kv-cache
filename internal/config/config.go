// Package config loads runtime settings from environment variables.
//
// Every setting has a production-ready default, so the server runs with zero
// configuration, but each one can be overridden per environment (Docker,
// Kubernetes, CI). Invalid values fail fast at startup with a clear message.
package config

import (
	"fmt"
	"net"
	"strconv"
	"time"
)

// Config holds every tunable of the server.
type Config struct {
	Host string // interface to bind, e.g. "0.0.0.0"
	Port int    // TCP port

	Capacity int // maximum number of keys before eviction starts
	Shards   int // number of cache shards (rounded up to a power of two)

	MaxKeyBytes   int // maximum key length in bytes
	MaxValueBytes int // maximum value length in bytes
	MaxBodyBytes  int // maximum raw request body size

	ReadTimeout     time.Duration // max time to read a full request
	WriteTimeout    time.Duration // max time to write a response
	IdleTimeout     time.Duration // how long an idle keep-alive connection stays open
	ShutdownTimeout time.Duration // grace period for in-flight requests on shutdown
}

// Addr returns the "host:port" string the server listens on.
func (c Config) Addr() string {
	return net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
}

// Load builds a Config from an environment lookup function. Passing the
// lookup (normally os.Getenv) instead of reading os.Getenv directly keeps
// this function pure and easy to test with a plain map.
func Load(getenv func(string) string) (Config, error) {
	r := reader{getenv: getenv}

	cfg := Config{
		Host:            r.str("HOST", "0.0.0.0"),
		Port:            r.int("PORT", 7171),
		Capacity:        r.int("CACHE_CAPACITY", 1_000_000),
		Shards:          r.int("CACHE_SHARDS", 256),
		MaxKeyBytes:     r.int("MAX_KEY_BYTES", 256),
		MaxValueBytes:   r.int("MAX_VALUE_BYTES", 256),
		MaxBodyBytes:    r.int("MAX_BODY_BYTES", 4096),
		ReadTimeout:     r.duration("READ_TIMEOUT", 5*time.Second),
		WriteTimeout:    r.duration("WRITE_TIMEOUT", 5*time.Second),
		IdleTimeout:     r.duration("IDLE_TIMEOUT", 60*time.Second),
		ShutdownTimeout: r.duration("SHUTDOWN_TIMEOUT", 10*time.Second),
	}
	if r.err != nil {
		return Config{}, r.err
	}
	if cfg.Port > 65535 {
		return Config{}, fmt.Errorf("PORT must be between 1 and 65535, got %d", cfg.Port)
	}
	return cfg, nil
}

// reader collects the first parsing error so Load can parse every field in
// a flat list instead of checking an error after each line.
type reader struct {
	getenv func(string) string
	err    error
}

func (r *reader) str(name, fallback string) string {
	if v := r.getenv(name); v != "" {
		return v
	}
	return fallback
}

// int parses a positive integer.
func (r *reader) int(name string, fallback int) int {
	raw := r.getenv(name)
	if raw == "" {
		return fallback
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < 1 {
		r.fail(fmt.Errorf("%s must be a positive integer, got %q", name, raw))
		return fallback
	}
	return v
}

// duration parses a positive Go duration such as "5s" or "250ms".
func (r *reader) duration(name string, fallback time.Duration) time.Duration {
	raw := r.getenv(name)
	if raw == "" {
		return fallback
	}
	v, err := time.ParseDuration(raw)
	if err != nil || v <= 0 {
		r.fail(fmt.Errorf("%s must be a positive duration like 5s or 250ms, got %q", name, raw))
		return fallback
	}
	return v
}

func (r *reader) fail(err error) {
	if r.err == nil {
		r.err = err
	}
}
