// Package config loads the API's settings from environment variables.
package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"
)

// DefaultHTTPAddr is the listen address used when INFRASCOPE_HTTP_ADDR is unset.
const DefaultHTTPAddr = ":8081"

// Config holds every setting the API reads at startup.
type Config struct {
	HTTPAddr           string
	DatabaseURL        string
	ClickHouseURL      string
	PrometheusURL      string
	CollectorHealthURL string
	// DependencyTimeout bounds every call to a dependency, so a slow backend
	// produces a fast error instead of a hung request (NFR-1).
	DependencyTimeout time.Duration
}

// Load reads the configuration from the process environment. Defaults match
// the service names in deployments/docker-compose.yml.
func Load() (Config, error) {
	return load(os.Getenv)
}

func load(getenv func(string) string) (Config, error) {
	cfg := Config{
		HTTPAddr:           valueOr(getenv("INFRASCOPE_HTTP_ADDR"), DefaultHTTPAddr),
		DatabaseURL:        getenv("INFRASCOPE_DATABASE_URL"),
		ClickHouseURL:      baseURL(valueOr(getenv("INFRASCOPE_CLICKHOUSE_URL"), "http://clickhouse:8123")),
		PrometheusURL:      baseURL(valueOr(getenv("INFRASCOPE_PROMETHEUS_URL"), "http://prometheus:9090")),
		CollectorHealthURL: baseURL(valueOr(getenv("INFRASCOPE_COLLECTOR_HEALTH_URL"), "http://otel-collector:13133")),
		DependencyTimeout:  2 * time.Second,
	}

	if raw := getenv("INFRASCOPE_DEPENDENCY_TIMEOUT"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			return Config{}, fmt.Errorf("INFRASCOPE_DEPENDENCY_TIMEOUT: want a positive duration such as 2s, got %q", raw)
		}
		cfg.DependencyTimeout = d
	}

	if cfg.DatabaseURL == "" {
		return Config{}, errors.New("INFRASCOPE_DATABASE_URL is required")
	}
	if _, err := cfg.DatabaseAddr(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// DatabaseAddr returns the host:port of the PostgreSQL server in DatabaseURL.
// Errors never echo the URL, because it contains a password.
func (c Config) DatabaseAddr() (string, error) {
	u, err := url.Parse(c.DatabaseURL)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Hostname() == "" {
		return "", errors.New("INFRASCOPE_DATABASE_URL: want postgres://user:password@host:port/database")
	}
	port := u.Port()
	if port == "" {
		port = "5432"
	}
	return net.JoinHostPort(u.Hostname(), port), nil
}

func valueOr(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

func baseURL(raw string) string {
	return strings.TrimRight(raw, "/")
}
