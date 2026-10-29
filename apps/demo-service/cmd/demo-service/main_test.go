package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRoutes(t *testing.T) {
	tests := []struct {
		path     string
		wantCode int
	}{
		{"/healthz", http.StatusOK},
		{"/", http.StatusOK},
		{"/products", http.StatusNotFound}, // arrives in Phase 1
	}
	router := newRouter()
	for _, tt := range tests {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.path, nil))
		if rec.Code != tt.wantCode {
			t.Errorf("GET %s = %d, want %d", tt.path, rec.Code, tt.wantCode)
		}
	}
}

func TestLoopbackURL(t *testing.T) {
	got, err := loopbackURL(":8082", "/healthz")
	if err != nil {
		t.Fatalf("loopbackURL: %v", err)
	}
	if want := "http://127.0.0.1:8082/healthz"; got != want {
		t.Errorf("loopbackURL = %q, want %q", got, want)
	}
}
