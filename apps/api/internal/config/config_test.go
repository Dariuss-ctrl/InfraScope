package config

import (
	"strings"
	"testing"
	"time"
)

// valid is the minimal environment that loads.
func valid(overrides map[string]string) func(string) string {
	vars := map[string]string{
		"INFRASCOPE_DATABASE_URL":   "postgres://infrascope:secret@postgres:5432/infrascope?sslmode=disable",
		"INFRASCOPE_ADMIN_PASSWORD": "a-long-enough-password",
	}
	for k, v := range overrides {
		vars[k] = v
	}
	return func(key string) string { return vars[key] }
}

func TestLoadAppliesDefaults(t *testing.T) {
	cfg, err := load(valid(nil))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	checks := []struct {
		name      string
		got, want any
	}{
		{"HTTPAddr", cfg.HTTPAddr, DefaultHTTPAddr},
		{"ClickHouseURL", cfg.ClickHouseURL, "http://clickhouse:8123"},
		{"ClickHouseUser", cfg.ClickHouseUser, "default"},
		{"AdminUser", cfg.AdminUser, "admin"},
		{"DependencyTimeout", cfg.DependencyTimeout, 2 * time.Second},
		{"QueryTimeout", cfg.QueryTimeout, 10 * time.Second},
		{"SessionTTL", cfg.SessionTTL, 12 * time.Hour},
		{"MaxSearchRange", cfg.MaxSearchRange, 24 * time.Hour},
		{"CookieSecure", cfg.CookieSecure, false},
		{"GitHubWebhookSecret", cfg.GitHubWebhookSecret, ""},
		{"DeployRadarWindow", cfg.DeployRadarWindow, 10 * time.Minute},
		{"DeployRadarSettle", cfg.DeployRadarSettle, 2 * time.Minute},
		{"DeployRadarMinRequests", cfg.DeployRadarMinRequests, 200},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
}

func TestLoadReadsOverrides(t *testing.T) {
	cfg, err := load(valid(map[string]string{
		"INFRASCOPE_PROMETHEUS_URL":   "http://prom:9090/",
		"INFRASCOPE_QUERY_TIMEOUT":    "3s",
		"INFRASCOPE_SESSION_TTL":      "1h",
		"INFRASCOPE_COOKIE_SECURE":    "true",
		"INFRASCOPE_MAX_SEARCH_RANGE": "6h",

		"INFRASCOPE_GITHUB_WEBHOOK_SECRET":    "sixteen-chars-ok",
		"INFRASCOPE_DEPLOYRADAR_WINDOW":       "3m",
		"INFRASCOPE_DEPLOYRADAR_SETTLE":       "0s",
		"INFRASCOPE_DEPLOYRADAR_MIN_REQUESTS": "50",
	}))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.PrometheusURL != "http://prom:9090" {
		t.Errorf("PrometheusURL = %q, want the trailing slash trimmed", cfg.PrometheusURL)
	}
	if cfg.QueryTimeout != 3*time.Second || cfg.SessionTTL != time.Hour || !cfg.CookieSecure || cfg.MaxSearchRange != 6*time.Hour {
		t.Errorf("overrides not applied: %+v", cfg)
	}
	if cfg.GitHubWebhookSecret != "sixteen-chars-ok" || cfg.DeployRadarWindow != 3*time.Minute || cfg.DeployRadarSettle != 0 || cfg.DeployRadarMinRequests != 50 {
		t.Errorf("DeployRadar overrides not applied: %+v", cfg)
	}
}

func TestLoadRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
	}{
		{"missing database url", map[string]string{"INFRASCOPE_DATABASE_URL": ""}},
		{"wrong scheme", map[string]string{"INFRASCOPE_DATABASE_URL": "mysql://u:p@db:3306/x"}},
		{"missing admin password", map[string]string{"INFRASCOPE_ADMIN_PASSWORD": ""}},
		{"short admin password", map[string]string{"INFRASCOPE_ADMIN_PASSWORD": "short"}},
		{"admin password over bcrypt's limit", map[string]string{"INFRASCOPE_ADMIN_PASSWORD": strings.Repeat("x", 73)}},
		{"bad timeout", map[string]string{"INFRASCOPE_QUERY_TIMEOUT": "soon"}},
		{"zero timeout", map[string]string{"INFRASCOPE_DEPENDENCY_TIMEOUT": "0s"}},
		{"session ttl too short", map[string]string{"INFRASCOPE_SESSION_TTL": "1s"}},
		{"bad boolean", map[string]string{"INFRASCOPE_COOKIE_SECURE": "sometimes"}},
		{"short webhook secret", map[string]string{"INFRASCOPE_GITHUB_WEBHOOK_SECRET": "fifteen-chars-x"}},
		{"window too short", map[string]string{"INFRASCOPE_DEPLOYRADAR_WINDOW": "30s"}},
		{"negative settle", map[string]string{"INFRASCOPE_DEPLOYRADAR_SETTLE": "-1m"}},
		{"min requests not a number", map[string]string{"INFRASCOPE_DEPLOYRADAR_MIN_REQUESTS": "many"}},
		{"min requests zero", map[string]string{"INFRASCOPE_DEPLOYRADAR_MIN_REQUESTS": "0"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := load(valid(tt.env)); err == nil {
				t.Fatal("want an error, got nil")
			}
		})
	}
}

func TestErrorsNeverEchoSecrets(t *testing.T) {
	_, err := load(valid(map[string]string{
		"INFRASCOPE_DATABASE_URL":          "mysql://u:hunter2@db/x",
		"INFRASCOPE_ADMIN_PASSWORD":        "tiny-secret",
		"INFRASCOPE_GITHUB_WEBHOOK_SECRET": "hook-secret",
	}))
	if err == nil {
		t.Fatal("want an error, got nil")
	}
	for _, secret := range []string{"hunter2", "tiny-secret", "hook-secret"} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("error leaks %q: %v", secret, err)
		}
	}
}

func TestDatabaseAddr(t *testing.T) {
	tests := []struct {
		url  string
		want string
	}{
		{"postgres://u:p@postgres:5432/infrascope", "postgres:5432"},
		{"postgresql://u:p@db/infrascope", "db:5432"},
		{"postgres://u:p@[::1]:6432/x", "[::1]:6432"},
	}
	for _, tt := range tests {
		got, err := Config{DatabaseURL: tt.url}.DatabaseAddr()
		if err != nil {
			t.Errorf("DatabaseAddr(%q): %v", tt.url, err)
			continue
		}
		if got != tt.want {
			t.Errorf("DatabaseAddr(%q) = %q, want %q", tt.url, got, tt.want)
		}
	}
}
