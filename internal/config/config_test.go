package config

import (
	"strings"
	"testing"
	"time"
)

// env turns a map into a getenv-style lookup function.
func env(vars map[string]string) func(string) string {
	return func(key string) string { return vars[key] }
}

func TestDefaults(t *testing.T) {
	cfg, err := Load(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != 7171 || cfg.Host != "0.0.0.0" {
		t.Fatalf("unexpected listen defaults: %s", cfg.Addr())
	}
	if cfg.Capacity != 1_000_000 || cfg.Shards != 256 {
		t.Fatalf("unexpected cache defaults: %+v", cfg)
	}
	if cfg.MaxKeyBytes != 256 || cfg.MaxValueBytes != 256 {
		t.Fatalf("unexpected size limits: %+v", cfg)
	}
	if cfg.ReadTimeout != 5*time.Second || cfg.ShutdownTimeout != 10*time.Second {
		t.Fatalf("unexpected timeouts: %+v", cfg)
	}
}

func TestOverrides(t *testing.T) {
	cfg, err := Load(env(map[string]string{
		"HOST":           "127.0.0.1",
		"PORT":           "8080",
		"CACHE_CAPACITY": "500",
		"CACHE_SHARDS":   "16",
		"READ_TIMEOUT":   "250ms",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr() != "127.0.0.1:8080" {
		t.Fatalf("Addr() = %s", cfg.Addr())
	}
	if cfg.Capacity != 500 || cfg.Shards != 16 || cfg.ReadTimeout != 250*time.Millisecond {
		t.Fatalf("overrides not applied: %+v", cfg)
	}
}

func TestAddrSupportsIPv6(t *testing.T) {
	cfg, err := Load(env(map[string]string{"HOST": "::1"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr() != "[::1]:7171" {
		t.Fatalf("Addr() = %s; want [::1]:7171", cfg.Addr())
	}
}

func TestInvalidValues(t *testing.T) {
	// Table-driven test: each row is one bad variable and its value.
	tests := []struct{ name, value string }{
		{"PORT", "abc"},
		{"PORT", "70000"},
		{"CACHE_CAPACITY", "0"},
		{"MAX_KEY_BYTES", "-1"},
		{"READ_TIMEOUT", "soon"},
		{"IDLE_TIMEOUT", "-5s"},
	}
	for _, tt := range tests {
		t.Run(tt.name+"="+tt.value, func(t *testing.T) {
			_, err := Load(env(map[string]string{tt.name: tt.value}))
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tt.name) {
				t.Fatalf("error %q should mention %s", err, tt.name)
			}
		})
	}
}

func TestFirstErrorWins(t *testing.T) {
	_, err := Load(env(map[string]string{"PORT": "x", "CACHE_SHARDS": "y"}))
	if err == nil || !strings.Contains(err.Error(), "PORT") {
		t.Fatalf("expected the first (PORT) error, got %v", err)
	}
}
