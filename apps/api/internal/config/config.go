// Package config loads the API's settings from environment variables.
package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// DefaultHTTPAddr is the listen address used when INFRASCOPE_HTTP_ADDR is unset.
const DefaultHTTPAddr = ":8081"

// Admin password length limits: at least 12 characters, and at most 72
// bytes, bcrypt's input limit.
const (
	minAdminPassword = 12
	maxAdminPassword = 72
)

// Config holds every setting the API reads at startup.
type Config struct {
	HTTPAddr           string
	DatabaseURL        string
	ClickHouseURL      string
	ClickHouseUser     string
	ClickHousePassword string
	PrometheusURL      string
	CollectorHealthURL string

	AdminUser     string
	AdminPassword string
	// SessionTTL is how long a login lasts.
	SessionTTL time.Duration
	// CookieSecure marks the session cookie Secure; enable it whenever the
	// dashboard is served over HTTPS.
	CookieSecure bool

	// DependencyTimeout bounds health probes and connection attempts, so a
	// down dependency produces a fast error instead of a hung request (NFR-1).
	DependencyTimeout time.Duration
	// QueryTimeout is the longest a single search may run (NFR-2 guard rail).
	QueryTimeout time.Duration
	// MaxSearchRange is the widest time window a trace or log search may cover.
	MaxSearchRange time.Duration
}

// Load reads the configuration from the process environment. Defaults match
// the service names in deployments/docker-compose.yml.
func Load() (Config, error) {
	return load(os.Getenv)
}

func load(getenv func(string) string) (Config, error) {
	env := func(key, fallback string) string {
		if v := getenv(key); v != "" {
			return v
		}
		return fallback
	}
	cfg := Config{
		HTTPAddr:           env("INFRASCOPE_HTTP_ADDR", DefaultHTTPAddr),
		DatabaseURL:        getenv("INFRASCOPE_DATABASE_URL"),
		ClickHouseURL:      baseURL(env("INFRASCOPE_CLICKHOUSE_URL", "http://clickhouse:8123")),
		ClickHouseUser:     env("INFRASCOPE_CLICKHOUSE_USER", "default"),
		ClickHousePassword: getenv("INFRASCOPE_CLICKHOUSE_PASSWORD"),
		PrometheusURL:      baseURL(env("INFRASCOPE_PROMETHEUS_URL", "http://prometheus:9090")),
		CollectorHealthURL: baseURL(env("INFRASCOPE_COLLECTOR_HEALTH_URL", "http://otel-collector:13133")),
		AdminUser:          env("INFRASCOPE_ADMIN_USER", "admin"),
		AdminPassword:      getenv("INFRASCOPE_ADMIN_PASSWORD"),
	}

	var errs []error
	durationVar := func(dst *time.Duration, key string, fallback, lo, hi time.Duration) {
		*dst = fallback
		raw := getenv(key)
		if raw == "" {
			return
		}
		d, err := time.ParseDuration(raw)
		if err != nil || d < lo || d > hi {
			errs = append(errs, fmt.Errorf("%s: want a duration from %s to %s, got %q", key, lo, hi, raw))
			return
		}
		*dst = d
	}
	durationVar(&cfg.DependencyTimeout, "INFRASCOPE_DEPENDENCY_TIMEOUT", 2*time.Second, time.Millisecond, time.Minute)
	durationVar(&cfg.QueryTimeout, "INFRASCOPE_QUERY_TIMEOUT", 10*time.Second, time.Millisecond, 5*time.Minute)
	durationVar(&cfg.SessionTTL, "INFRASCOPE_SESSION_TTL", 12*time.Hour, 5*time.Minute, 30*24*time.Hour)
	durationVar(&cfg.MaxSearchRange, "INFRASCOPE_MAX_SEARCH_RANGE", 24*time.Hour, time.Minute, 7*24*time.Hour)

	if raw := getenv("INFRASCOPE_COOKIE_SECURE"); raw != "" {
		secure, err := strconv.ParseBool(raw)
		if err != nil {
			errs = append(errs, fmt.Errorf("INFRASCOPE_COOKIE_SECURE: want true or false, got %q", raw))
		}
		cfg.CookieSecure = secure
	}

	if cfg.DatabaseURL == "" {
		errs = append(errs, errors.New("INFRASCOPE_DATABASE_URL is required"))
	} else if _, err := cfg.DatabaseAddr(); err != nil {
		errs = append(errs, err)
	}
	switch n := len(cfg.AdminPassword); {
	case n == 0:
		errs = append(errs, errors.New("INFRASCOPE_ADMIN_PASSWORD is required"))
	case n < minAdminPassword || n > maxAdminPassword:
		errs = append(errs, fmt.Errorf("INFRASCOPE_ADMIN_PASSWORD must be %d to %d characters", minAdminPassword, maxAdminPassword))
	}
	if strings.TrimSpace(cfg.AdminUser) == "" || len(cfg.AdminUser) > 128 {
		errs = append(errs, errors.New("INFRASCOPE_ADMIN_USER must be 1 to 128 characters"))
	}

	if err := errors.Join(errs...); err != nil {
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

func baseURL(raw string) string {
	return strings.TrimRight(raw, "/")
}
