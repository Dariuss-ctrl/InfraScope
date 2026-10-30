package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"

	"go.opentelemetry.io/otel/trace"

	"infrascope/api/internal/backend"
	"infrascope/api/internal/clickhouse"
)

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

// validationError carries every problem found in a request.
type validationError struct {
	fields []FieldError
}

func (e *validationError) Error() string {
	parts := make([]string, len(e.fields))
	for i, f := range e.fields {
		parts[i] = f.Field + " " + f.Message
	}
	return "invalid request: " + strings.Join(parts, "; ")
}

func invalid(fields ...FieldError) error {
	slices.SortStableFunc(fields, func(a, b FieldError) int { return strings.Compare(a.Field, b.Field) })
	return &validationError{fields: fields}
}

// dependency names the backend a handler was calling, which decides the
// error code when it is unavailable.
type dependency int

const (
	storage  dependency = iota // ClickHouse: traces and logs
	metrics                    // Prometheus
	database                   // PostgreSQL: users and sessions
)

var unavailable = map[dependency]struct{ code, message string }{
	storage:  {"storage_unavailable", "Trace and log storage is unavailable. Service metrics may still work."},
	metrics:  {"metrics_unavailable", "Metrics storage is unavailable. Traces and logs may still work."},
	database: {"database_unavailable", "The configuration database is unavailable, so sessions cannot be checked."},
}

// fail turns err into the standard error response. Details of unexpected
// errors are logged and attached to the request's span, never returned.
func (a *api) fail(w http.ResponseWriter, r *http.Request, err error, dep dependency) {
	var verr *validationError
	switch {
	case errors.As(err, &verr):
		writeError(w, http.StatusBadRequest, "invalid_argument", "The request has invalid parameters.", verr.fields...)
	case errors.Is(err, clickhouse.ErrInvalidCursor):
		writeError(w, http.StatusBadRequest, "invalid_argument", "The request has invalid parameters.",
			FieldError{Field: "cursor", Message: "is not a cursor returned by this search"})
	case errors.Is(err, backend.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "Not found.")
	case errors.Is(err, backend.ErrTimeout):
		a.logger.WarnContext(r.Context(), "query timed out", "route", r.Pattern, "error", err)
		writeError(w, http.StatusGatewayTimeout, "query_timeout",
			"The query took longer than its time limit. Narrow the time range or add filters.")
	case errors.Is(err, backend.ErrUnavailable):
		a.logger.WarnContext(r.Context(), "dependency unavailable", "route", r.Pattern, "error", err)
		u := unavailable[dep]
		writeError(w, http.StatusServiceUnavailable, u.code, u.message)
	default:
		trace.SpanFromContext(r.Context()).RecordError(err)
		a.logger.ErrorContext(r.Context(), "request failed", "route", r.Pattern, "error", err)
		writeError(w, http.StatusInternalServerError, "internal", "Something went wrong. The error has been logged.")
	}
}

func writeError(w http.ResponseWriter, status int, code, message string, fields ...FieldError) {
	writeJSON(w, status, errorEnvelope{Error: errorBody{Code: code, Message: message, Fields: fields}})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	// The status line is already sent, so an encoding error can only show
	// as a truncated body; there is nothing useful left to do.
	_ = json.NewEncoder(w).Encode(v)
}
