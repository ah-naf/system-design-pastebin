package config

import (
	"testing"
	"time"
)

func TestLoadRequiresWriteBackends(t *testing.T) {
	t.Setenv("READ_BACKENDS", "http://localhost:8081")
	t.Setenv("WRITE_BACKENDS", "")

	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want error for missing WRITE_BACKENDS")
	}
}

func TestLoadRequiresReadBackends(t *testing.T) {
	t.Setenv("WRITE_BACKENDS", "http://localhost:8080")
	t.Setenv("READ_BACKENDS", "")

	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want error for missing READ_BACKENDS")
	}
}

func TestLoadSplitsCommaSeparatedBackends(t *testing.T) {
	t.Setenv("WRITE_BACKENDS", "http://localhost:8080, http://localhost:8090")
	t.Setenv("READ_BACKENDS", "http://localhost:8081")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	want := []string{"http://localhost:8080", "http://localhost:8090"}
	if len(cfg.WriteBackends) != len(want) {
		t.Fatalf("WriteBackends = %v, want %v", cfg.WriteBackends, want)
	}
	for i, addr := range want {
		if cfg.WriteBackends[i] != addr {
			t.Errorf("WriteBackends[%d] = %q, want %q", i, cfg.WriteBackends[i], addr)
		}
	}
}

func TestLoadDefaultsPortAndHealthCheckInterval(t *testing.T) {
	t.Setenv("WRITE_BACKENDS", "http://localhost:8080")
	t.Setenv("READ_BACKENDS", "http://localhost:8081")
	t.Setenv("PORT", "")
	t.Setenv("HEALTH_CHECK_INTERVAL", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.Port != "8082" {
		t.Errorf("Port = %q, want %q", cfg.Port, "8082")
	}
	if cfg.HealthCheckInterval != 5*time.Second {
		t.Errorf("HealthCheckInterval = %v, want %v", cfg.HealthCheckInterval, 5*time.Second)
	}
}

func TestLoadRejectsMalformedHealthCheckInterval(t *testing.T) {
	t.Setenv("WRITE_BACKENDS", "http://localhost:8080")
	t.Setenv("READ_BACKENDS", "http://localhost:8081")
	t.Setenv("HEALTH_CHECK_INTERVAL", "not-a-duration")

	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want error for malformed HEALTH_CHECK_INTERVAL")
	}
}
