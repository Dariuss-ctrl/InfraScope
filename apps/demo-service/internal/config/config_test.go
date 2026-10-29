package config

import (
	"testing"
	"time"
)

func env(vars map[string]string) func(string) string {
	return func(key string) string { return vars[key] }
}

func TestLoadAppliesDefaults(t *testing.T) {
	cfg, err := load(env(map[string]string{"DEMO_DATABASE_URL": "postgres://shop:secret@postgres:5432/shop"}))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.HTTPAddr != DefaultHTTPAddr {
		t.Errorf("HTTPAddr = %q, want %q", cfg.HTTPAddr, DefaultHTTPAddr)
	}
	if cfg.SlowQueryThreshold != 100*time.Millisecond {
		t.Errorf("SlowQueryThreshold = %v, want 100ms", cfg.SlowQueryThreshold)
	}
	if cfg.SeedProducts != 10_000 || cfg.SeedCustomers != 100_000 || cfg.SeedOrders != 1_000_000 {
		t.Errorf("seed sizes = %d/%d/%d, want 10000/100000/1000000",
			cfg.SeedProducts, cfg.SeedCustomers, cfg.SeedOrders)
	}
}

func TestLoadReadsOverrides(t *testing.T) {
	cfg, err := load(env(map[string]string{
		"DEMO_DATABASE_URL":         "postgres://shop:secret@postgres:5432/shop",
		"DEMO_HTTP_ADDR":            ":9000",
		"DEMO_SLOW_QUERY_THRESHOLD": "250ms",
		"DEMO_SEED_ORDERS":          "5000",
	}))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.HTTPAddr != ":9000" || cfg.SlowQueryThreshold != 250*time.Millisecond || cfg.SeedOrders != 5000 {
		t.Errorf("overrides not applied: %+v", cfg)
	}
}

func TestLoadRejectsInvalidValues(t *testing.T) {
	const db = "postgres://shop:secret@postgres:5432/shop"
	tests := []struct {
		name string
		vars map[string]string
	}{
		{"missing database url", map[string]string{}},
		{"bad threshold", map[string]string{"DEMO_DATABASE_URL": db, "DEMO_SLOW_QUERY_THRESHOLD": "fast"}},
		{"negative threshold", map[string]string{"DEMO_DATABASE_URL": db, "DEMO_SLOW_QUERY_THRESHOLD": "-1s"}},
		{"zero seed size", map[string]string{"DEMO_DATABASE_URL": db, "DEMO_SEED_PRODUCTS": "0"}},
		{"non-numeric seed size", map[string]string{"DEMO_DATABASE_URL": db, "DEMO_SEED_ORDERS": "many"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := load(env(tt.vars)); err == nil {
				t.Fatal("want an error, got nil")
			}
		})
	}
}
