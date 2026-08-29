package config

import "testing"

func setRequiredEnv(t *testing.T) {
	t.Helper()
	t.Setenv("DATABASE_URL", "postgres://user:pass@localhost:5433/pastebin?sslmode=disable")
	t.Setenv("S3_ACCESS_KEY", "test-access-key")
	t.Setenv("S3_SECRET_KEY", "test-secret-key")
	t.Setenv("ID_XOR_SECRET", "9f3a1c2e5b7d0f14")
}

func TestLoadAppliesDefaults(t *testing.T) {
	setRequiredEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.Port != "8080" {
		t.Errorf("Port = %q, want \"8080\"", cfg.Port)
	}
	if cfg.PublicBaseURL != "http://localhost:8081" {
		t.Errorf("PublicBaseURL = %q, want \"http://localhost:8081\"", cfg.PublicBaseURL)
	}
	if cfg.RedisAddr != "localhost:6379" {
		t.Errorf("RedisAddr = %q, want \"localhost:6379\"", cfg.RedisAddr)
	}
	if cfg.S3Endpoint != "localhost:9000" {
		t.Errorf("S3Endpoint = %q, want \"localhost:9000\"", cfg.S3Endpoint)
	}
	if cfg.S3Bucket != "pastebin" {
		t.Errorf("S3Bucket = %q, want \"pastebin\"", cfg.S3Bucket)
	}
	if cfg.S3UseSSL != false {
		t.Errorf("S3UseSSL = %v, want false", cfg.S3UseSSL)
	}
	if cfg.MaxPasteBytes != 1048576 {
		t.Errorf("MaxPasteBytes = %d, want 1048576", cfg.MaxPasteBytes)
	}
}

func TestLoadParsesXORSecret(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("ID_XOR_SECRET", "00000000000000ff")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.IDXORSecret != 0xff {
		t.Errorf("IDXORSecret = %#x, want 0xff", cfg.IDXORSecret)
	}
}

func TestLoadOverridesDefaults(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("PORT", "9090")
	t.Setenv("S3_USE_SSL", "true")
	t.Setenv("MAX_PASTE_BYTES", "2048")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.Port != "9090" {
		t.Errorf("Port = %q, want \"9090\"", cfg.Port)
	}
	if cfg.S3UseSSL != true {
		t.Errorf("S3UseSSL = %v, want true", cfg.S3UseSSL)
	}
	if cfg.MaxPasteBytes != 2048 {
		t.Errorf("MaxPasteBytes = %d, want 2048", cfg.MaxPasteBytes)
	}
}

func TestLoadRequiresDatabaseURL(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("DATABASE_URL", "")
	if _, err := Load(); err == nil {
		t.Error("Load() with empty DATABASE_URL: expected error, got nil")
	}
}

func TestLoadRequiresS3Credentials(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("S3_ACCESS_KEY", "")
	if _, err := Load(); err == nil {
		t.Error("Load() with empty S3_ACCESS_KEY: expected error, got nil")
	}
}

func TestLoadRequiresIDXORSecret(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("ID_XOR_SECRET", "")
	if _, err := Load(); err == nil {
		t.Error("Load() with empty ID_XOR_SECRET: expected error, got nil")
	}
}

func TestLoadRejectsMalformedXORSecret(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("ID_XOR_SECRET", "not-hex-at-all")
	if _, err := Load(); err == nil {
		t.Error("Load() with non-hex ID_XOR_SECRET: expected error, got nil")
	}
}

func TestLoadRejectsMalformedMaxPasteBytes(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("MAX_PASTE_BYTES", "not-a-number")
	if _, err := Load(); err == nil {
		t.Error("Load() with non-numeric MAX_PASTE_BYTES: expected error, got nil")
	}
}
