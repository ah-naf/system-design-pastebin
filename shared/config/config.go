package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	DatabaseURL       string
	Port              string
	PublicBaseURL     string
	RedisAddr         string
	S3Endpoint        string
	S3Bucket          string
	S3AccessKey       string
	S3SecretKey       string
	S3UseSSL          bool
	MaxPasteBytes     int64
	IDXORSecret       uint64
	DBMaxOpenConns    int
	DBMaxIdleConns    int
	DBConnMaxLifetime time.Duration
}

func requiredEnv(key string) (string, error) {
	value := os.Getenv(key)
	if value == "" {
		return "", fmt.Errorf("required environment variable %s is not set", key)
	}
	return value, nil
}

func envOrDefault(key, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	return value
}

func Load() (*Config, error) {
	databaseURL, err := requiredEnv("DATABASE_URL")
	if err != nil {
		return nil, err
	}

	s3AccessKey, err := requiredEnv("S3_ACCESS_KEY")
	if err != nil {
		return nil, err
	}

	s3SecretKey, err := requiredEnv("S3_SECRET_KEY")
	if err != nil {
		return nil, err
	}

	s3UseSSL, err := strconv.ParseBool(
		envOrDefault("S3_USE_SSL", "false"),
	)
	if err != nil {
		return nil, fmt.Errorf("invalid S3_USE_SSL: %w", err)
	}

	maxPasteBytes, err := strconv.ParseInt(
		envOrDefault("MAX_PASTE_BYTES", "1048576"),
		10, 64,
	)
	if err != nil {
		return nil, fmt.Errorf("invalid MAX_PASTE_BYTES: %w", err)
	}

	xorSecret := envOrDefault("ID_XOR_SECRET", "")
	if len(xorSecret) == 0 {
		xorSecret = "0"
	}

	idXORSecret, err := strconv.ParseUint(xorSecret, 16, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid ID_XOR_SECRET: %w", err)
	}

	dbMaxOpenConns, err := strconv.Atoi(envOrDefault("DB_MAX_OPEN_CONNS", "10"))
	if err != nil {
		return nil, fmt.Errorf("invalid DB_MAX_OPEN_CONNS: %w", err)
	}

	dbMaxIdleConns, err := strconv.Atoi(envOrDefault("DB_MAX_IDLE_CONNS", "5"))
	if err != nil {
		return nil, fmt.Errorf("invalid DB_MAX_IDLE_CONNS: %w", err)
	}

	dbConnMaxLifetimeSeconds, err := strconv.Atoi(envOrDefault("DB_CONN_MAX_LIFETIME_SECONDS", "300"))
	if err != nil {
		return nil, fmt.Errorf("invalid DB_CONN_MAX_LIFETIME_SECONDS: %w", err)
	}

	return &Config{
		DatabaseURL:       databaseURL,
		Port:              envOrDefault("PORT", "8080"),
		PublicBaseURL:     envOrDefault("PUBLIC_BASE_URL", "http://localhost:8081"),
		RedisAddr:         envOrDefault("REDIS_ADDR", "localhost:6379"),
		S3Endpoint:        envOrDefault("S3_ENDPOINT", "localhost:9000"),
		S3Bucket:          envOrDefault("S3_BUCKET", "pastebin"),
		S3AccessKey:       s3AccessKey,
		S3SecretKey:       s3SecretKey,
		S3UseSSL:          s3UseSSL,
		MaxPasteBytes:     maxPasteBytes,
		IDXORSecret:       idXORSecret,
		DBMaxOpenConns:    dbMaxOpenConns,
		DBMaxIdleConns:    dbMaxIdleConns,
		DBConnMaxLifetime: time.Duration(dbConnMaxLifetimeSeconds) * time.Second,
	}, nil
}
