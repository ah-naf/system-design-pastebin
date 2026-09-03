package config

import (
	"fmt"
	"os"
	"strings"
	"time"
)

type Config struct {
	ReadBackends        []string
	WriteBackends       []string
	Port                string
	HealthCheckInterval time.Duration
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

func splitAndTrim(value string) []string {
	parts := strings.Split(value, ",")

	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}

	return parts
}

func Load() (*Config, error) {
	rawReadBackends, err := requiredEnv("READ_BACKENDS")
	if err != nil {
		return nil, err
	}
	readBackends := splitAndTrim(rawReadBackends)

	rawWriteBackends, err := requiredEnv("WRITE_BACKENDS")
	if err != nil {
		return nil, err
	}
	writeBackends := splitAndTrim(rawWriteBackends)

	healthCheckInterval, err := time.ParseDuration(envOrDefault("HEALTH_CHECK_INTERVAL", "5s"))
	if err != nil {
		return nil, err
	}

	return &Config{
		ReadBackends:        readBackends,
		WriteBackends:       writeBackends,
		Port:                envOrDefault("PORT", "8082"),
		HealthCheckInterval: healthCheckInterval,
	}, nil
}
