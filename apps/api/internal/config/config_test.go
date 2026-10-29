package config

import (
	"strings"
	"testing"
	"time"
)

func env(vars map[string]string) func(string) string {
	return func(key string) string { return vars[key] }
}

func TestLoadAppliesDefaults(t *testing.T) {
	cfg, err := load(env(map[string]string{
		"INFRASCOPE_DATABASE_URL": "postgres://infrascope:secret@postgres:5432/infrascope?sslmode=disable",
	}))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.HTTPAddr != DefaultHTTPAddr {
		t.Errorf("HTTPAddr = %q, want %q", cfg.HTTPAddr, DefaultHTTPAddr)
	}
	if cfg.ClickHouseURL != "http://clickhouse:8123" {
		t.Errorf("ClickHouseURL = %q", cfg.ClickHouseURL)
	}
	if cfg.DependencyTimeout != 2*time.Second {
		t.Errorf("DependencyTimeout = %v, want 2s", cfg.DependencyTimeout)
	}
}

func TestLoadTrimsTrailingSlashes(t *testing.T) {
	cfg, err := load(env(map[string]string{
		"INFRASCOPE_DATABASE_URL":   "postgres://u:p@db/x",
		"INFRASCOPE_PROMETHEUS_URL": "http://prom:9090/",
	}))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.PrometheusURL != "http://prom:9090" {
		t.Errorf("PrometheusURL = %q, want no trailing slash", cfg.PrometheusURL)
	}
}

func TestLoadRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name string
		vars map[string]string
	}{
		{"missing database url", map[string]string{}},
		{"wrong scheme", map[string]string{"INFRASCOPE_DATABASE_URL": "mysql://u:p@db:3306/x"}},
		{"no host", map[string]string{"INFRASCOPE_DATABASE_URL": "postgres:///x"}},
		{"bad timeout", map[string]string{"INFRASCOPE_DATABASE_URL": "postgres://u:p@db/x", "INFRASCOPE_DEPENDENCY_TIMEOUT": "soon"}},
		{"zero timeout", map[string]string{"INFRASCOPE_DATABASE_URL": "postgres://u:p@db/x", "INFRASCOPE_DEPENDENCY_TIMEOUT": "0s"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := load(env(tt.vars)); err == nil {
				t.Fatal("want an error, got nil")
			}
		})
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

func TestDatabaseAddrErrorHidesPassword(t *testing.T) {
	_, err := Config{DatabaseURL: "mysql://u:hunter2@db/x"}.DatabaseAddr()
	if err == nil {
		t.Fatal("want an error, got nil")
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("error leaks the password: %v", err)
	}
}
