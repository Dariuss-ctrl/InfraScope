// Package config loads the demo service's settings from environment variables.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"
)

// DefaultHTTPAddr is the listen address used when DEMO_HTTP_ADDR is unset.
const DefaultHTTPAddr = ":8082"

// Config holds every setting the demo service reads at startup.
type Config struct {
	HTTPAddr    string
	DatabaseURL string
	// SlowQueryThreshold is the duration above which a statement is logged
	// at WARN, with the trace context of its database span.
	SlowQueryThreshold time.Duration
	// Seed sizes for the deterministic demo data created on first start.
	SeedProducts  int
	SeedCustomers int
	SeedOrders    int
}

// Load reads the configuration from the process environment.
func Load() (Config, error) {
	return load(os.Getenv)
}

func load(getenv func(string) string) (Config, error) {
	cfg := Config{
		HTTPAddr:    DefaultHTTPAddr,
		DatabaseURL: getenv("DEMO_DATABASE_URL"),
	}
	if v := getenv("DEMO_HTTP_ADDR"); v != "" {
		cfg.HTTPAddr = v
	}
	if cfg.DatabaseURL == "" {
		return Config{}, errors.New("DEMO_DATABASE_URL is required")
	}

	var err error
	if cfg.SlowQueryThreshold, err = duration(getenv, "DEMO_SLOW_QUERY_THRESHOLD", 100*time.Millisecond); err != nil {
		return Config{}, err
	}
	if cfg.SeedProducts, err = positiveInt(getenv, "DEMO_SEED_PRODUCTS", 10_000); err != nil {
		return Config{}, err
	}
	if cfg.SeedCustomers, err = positiveInt(getenv, "DEMO_SEED_CUSTOMERS", 100_000); err != nil {
		return Config{}, err
	}
	if cfg.SeedOrders, err = positiveInt(getenv, "DEMO_SEED_ORDERS", 1_000_000); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func duration(getenv func(string) string, key string, fallback time.Duration) (time.Duration, error) {
	raw := getenv(key)
	if raw == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("%s: want a duration such as 100ms, got %q", key, raw)
	}
	return d, nil
}

func positiveInt(getenv func(string) string, key string, fallback int) (int, error) {
	raw := getenv(key)
	if raw == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%s: want a positive integer, got %q", key, raw)
	}
	return n, nil
}
