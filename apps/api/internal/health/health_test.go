package health

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

func up(context.Context) error   { return nil }
func down(context.Context) error { return errors.New("connection refused") }

func TestCheckSummarisesDependencies(t *testing.T) {
	tests := []struct {
		name string
		deps []Dependency
		want string
	}{
		{
			name: "all up",
			deps: []Dependency{{Name: "postgres", Required: true, Probe: up}, {Name: "clickhouse", Probe: up}},
			want: StatusOK,
		},
		{
			name: "optional down",
			deps: []Dependency{{Name: "postgres", Required: true, Probe: up}, {Name: "clickhouse", Probe: down}},
			want: StatusDegraded,
		},
		{
			name: "required down",
			deps: []Dependency{{Name: "postgres", Required: true, Probe: down}, {Name: "clickhouse", Probe: up}},
			want: StatusUnavailable,
		},
		{
			name: "required down wins over optional down",
			deps: []Dependency{{Name: "clickhouse", Probe: down}, {Name: "postgres", Required: true, Probe: down}},
			want: StatusUnavailable,
		},
		{
			name: "no dependencies",
			want: StatusOK,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			report := NewChecker(time.Second, discard, tt.deps...).Check(context.Background())
			if report.Status != tt.want {
				t.Errorf("Status = %q, want %q", report.Status, tt.want)
			}
			if len(report.Dependencies) != len(tt.deps) {
				t.Fatalf("got %d dependency results, want %d", len(report.Dependencies), len(tt.deps))
			}
			for i, dep := range tt.deps {
				if got := report.Dependencies[i].Name; got != dep.Name {
					t.Errorf("Dependencies[%d].Name = %q, want %q (order must be preserved)", i, got, dep.Name)
				}
			}
		})
	}
}

func TestCheckBoundsProbesThatIgnoreTheirContext(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	stuck := func(context.Context) error { <-release; return nil }

	checker := NewChecker(50*time.Millisecond, discard, Dependency{Name: "stuck", Probe: stuck})
	start := time.Now()
	report := checker.Check(context.Background())

	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Check took %v; the timeout was not enforced", elapsed)
	}
	if got := report.Dependencies[0].Status; got != StatusDown {
		t.Errorf("stuck dependency status = %q, want %q", got, StatusDown)
	}
}

func TestHTTPProbe(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "Ok.\n")
	}))
	defer ok.Close()
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer failing.Close()
	closed := httptest.NewServer(http.NotFoundHandler())
	closedURL := closed.URL
	closed.Close()

	tests := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{"2xx is up", ok.URL, false},
		{"5xx is down", failing.URL, true},
		{"unreachable is down", closedURL, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := HTTPProbe(http.DefaultClient, tt.url)(context.Background())
			if (err != nil) != tt.wantErr {
				t.Errorf("err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
