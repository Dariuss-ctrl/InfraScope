package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"infrascope/api/internal/health"
)

func checker(postgresUp, clickhouseUp bool) *health.Checker {
	probe := func(up bool) health.Probe {
		return func(context.Context) error {
			if up {
				return nil
			}
			return errors.New("down")
		}
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return health.NewChecker(time.Second, logger,
		health.Dependency{Name: "postgres", Required: true, Probe: probe(postgresUp)},
		health.Dependency{Name: "clickhouse", Probe: probe(clickhouseUp)},
	)
}

func serve(t *testing.T, h http.Handler, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("%s %s: Content-Type = %q, want application/json", method, path, ct)
	}
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.NewDecoder(rec.Body).Decode(&v); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	return v
}

func TestLiveness(t *testing.T) {
	// Liveness must not depend on dependencies: everything is down here.
	rec := serve(t, NewRouter(checker(false, false)), http.MethodGet, "/healthz")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestReadinessAndStatus(t *testing.T) {
	tests := []struct {
		name                     string
		postgresUp, clickhouseUp bool
		wantReadyCode            int
		wantStatus               string
	}{
		{"all up", true, true, http.StatusOK, health.StatusOK},
		{"clickhouse down degrades", true, false, http.StatusOK, health.StatusDegraded},
		{"postgres down is unavailable", false, true, http.StatusServiceUnavailable, health.StatusUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router := NewRouter(checker(tt.postgresUp, tt.clickhouseUp))

			ready := serve(t, router, http.MethodGet, "/readyz")
			if ready.Code != tt.wantReadyCode {
				t.Errorf("/readyz status = %d, want %d", ready.Code, tt.wantReadyCode)
			}
			if got := decode[health.Report](t, ready).Status; got != tt.wantStatus {
				t.Errorf("/readyz report status = %q, want %q", got, tt.wantStatus)
			}

			// The status endpoint always answers 200 so the dashboard can render it.
			st := serve(t, router, http.MethodGet, "/api/v1/status")
			if st.Code != http.StatusOK {
				t.Errorf("/api/v1/status status = %d, want 200", st.Code)
			}
			if got := decode[health.Report](t, st).Status; got != tt.wantStatus {
				t.Errorf("/api/v1/status report status = %q, want %q", got, tt.wantStatus)
			}
		})
	}
}

func TestErrorEnvelope(t *testing.T) {
	tests := []struct {
		method, path string
		wantCode     int
		wantErrCode  string
	}{
		{http.MethodGet, "/api/v1/traces", http.StatusNotImplemented, "not_implemented"},
		{http.MethodPost, "/api/v1/auth/login", http.StatusNotImplemented, "not_implemented"},
		{http.MethodGet, "/no/such/route", http.StatusNotFound, "not_found"},
	}
	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			rec := serve(t, NewRouter(checker(true, true)), tt.method, tt.path)
			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantCode)
			}
			body := decode[errorEnvelope](t, rec)
			if body.Error.Code != tt.wantErrCode {
				t.Errorf("error.code = %q, want %q", body.Error.Code, tt.wantErrCode)
			}
			if body.Error.Message == "" {
				t.Error("error.message is empty")
			}
		})
	}
}
