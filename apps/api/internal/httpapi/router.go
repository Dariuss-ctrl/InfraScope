// Package httpapi defines the API's HTTP routes and response conventions.
// apps/api/openapi.yaml is the contract these routes implement.
package httpapi

import (
	"encoding/json"
	"net/http"

	"infrascope/api/internal/health"
)

// NewRouter returns the API's HTTP handler. Endpoints that are in the
// contract but not built yet answer 501 with the standard error envelope, so
// clients can be written against the final shape from day one.
func NewRouter(checker *health.Checker) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", liveness)
	mux.HandleFunc("GET /readyz", readiness(checker))
	mux.HandleFunc("GET /api/v1/status", status(checker))
	mux.HandleFunc("/api/", notImplemented)
	mux.HandleFunc("/", notFound)
	return mux
}

// liveness reports that the process is serving. It deliberately checks no
// dependencies, so a database outage never gets the API restarted.
func liveness(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": health.StatusOK})
}

// readiness answers 503 when a required dependency is down, for orchestrators.
func readiness(checker *health.Checker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		report := checker.Check(r.Context())
		code := http.StatusOK
		if report.Status == health.StatusUnavailable {
			code = http.StatusServiceUnavailable
		}
		writeJSON(w, code, report)
	}
}

// status always answers 200 so the dashboard can show which dependencies are
// down instead of failing to load the status itself.
func status(checker *health.Checker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, checker.Check(r.Context()))
	}
}

func notImplemented(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusNotImplemented, "not_implemented",
		"This endpoint is part of the API contract but has not been built yet. See docs/SPEC.md, section 12.")
}

func notFound(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusNotFound, "not_found", "No such route.")
}

// FieldError describes one invalid request field in an invalid_argument error.
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

type errorEnvelope struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code    string       `json:"code"`
	Message string       `json:"message"`
	Fields  []FieldError `json:"fields,omitempty"`
}

func writeError(w http.ResponseWriter, code int, errCode, message string, fields ...FieldError) {
	writeJSON(w, code, errorEnvelope{Error: errorBody{Code: errCode, Message: message, Fields: fields}})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	// The status line is already sent, so an encoding error can only be
	// reported by the truncated body; there is nothing useful left to do.
	_ = json.NewEncoder(w).Encode(v)
}
