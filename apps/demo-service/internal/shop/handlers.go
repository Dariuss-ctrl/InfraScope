package shop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/trace"
)

// Repository is the storage the HTTP API needs. *Store implements it.
type Repository interface {
	ListProducts(ctx context.Context, limit, offset int) ([]Product, error)
	GetProduct(ctx context.Context, id int64) (Product, error)
	OrdersByCustomer(ctx context.Context, email string, limit int) ([]Order, error)
	CreateOrder(ctx context.Context, email string, productID int64, quantity int32) (Order, error)
	GetOrder(ctx context.Context, id int64) (Order, error)
}

// NewHandler returns the shop's HTTP API (docs/SPEC.md, section 9). Every
// request except health checks writes an access log record in its trace
// context.
func NewHandler(repo Repository, logger *slog.Logger) http.Handler {
	a := &api{repo: repo, logger: logger}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", a.healthz)
	mux.HandleFunc("GET /{$}", a.index)
	mux.HandleFunc("GET /products", a.listProducts)
	mux.HandleFunc("GET /products/{id}", a.getProduct)
	mux.HandleFunc("GET /orders", a.ordersByCustomer)
	mux.HandleFunc("POST /orders", a.createOrder)
	mux.HandleFunc("GET /orders/{id}", a.getOrder)
	return a.accessLog(mux)
}

// Instrument wraps h with OpenTelemetry HTTP instrumentation: a server span
// per request, named after the matched route (for example "GET /orders/{id}"),
// the http.server.request.duration histogram labelled with that route, and an
// X-Trace-Id response header so a caller can look up its own trace. Health
// checks are not traced.
func Instrument(h http.Handler, opts ...otelhttp.Option) http.Handler {
	traced := otelhttp.WithFilter(func(r *http.Request) bool { return !isHealthCheck(r) })
	return otelhttp.NewHandler(traceIDHeader(h), "demo-service", append([]otelhttp.Option{traced}, opts...)...)
}

func isHealthCheck(r *http.Request) bool {
	return r.URL.Path == "/healthz"
}

func traceIDHeader(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if sc := trace.SpanContextFromContext(r.Context()); sc.HasTraceID() {
			w.Header().Set("X-Trace-Id", sc.TraceID().String())
		}
		next.ServeHTTP(w, r)
	})
}

type api struct {
	repo   Repository
	logger *slog.Logger
}

func (a *api) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *api) index(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"service": "demo-service",
		"endpoints": []string{
			"GET /products?limit=&offset=",
			"GET /products/{id}",
			"GET /orders?customer_email=",
			"POST /orders",
			"GET /orders/{id}",
		},
	})
}

func (a *api) listProducts(w http.ResponseWriter, r *http.Request) {
	limit, ok := intQuery(w, r, "limit", 20, 1, 100)
	if !ok {
		return
	}
	offset, ok := intQuery(w, r, "offset", 0, 0, math.MaxInt32)
	if !ok {
		return
	}
	products, err := a.repo.ListProducts(r.Context(), limit, offset)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"products": products})
}

func (a *api) getProduct(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	product, err := a.repo.GetProduct(r.Context(), id)
	switch {
	case errors.Is(err, ErrNotFound):
		writeError(w, http.StatusNotFound, "product not found")
	case err != nil:
		a.serverError(w, r, err)
	default:
		writeJSON(w, http.StatusOK, product)
	}
}

func (a *api) ordersByCustomer(w http.ResponseWriter, r *http.Request) {
	email := r.URL.Query().Get("customer_email")
	if !validEmail(email) {
		writeError(w, http.StatusBadRequest, "customer_email is required and must be an email address")
		return
	}
	limit, ok := intQuery(w, r, "limit", 20, 1, 100)
	if !ok {
		return
	}
	orders, err := a.repo.OrdersByCustomer(r.Context(), email, limit)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"orders": orders})
}

type createOrderRequest struct {
	CustomerEmail string `json:"customer_email"`
	ProductID     int64  `json:"product_id"`
	Quantity      int32  `json:"quantity"`
}

func (req createOrderRequest) problem() string {
	switch {
	case !validEmail(req.CustomerEmail):
		return "customer_email must be an email address"
	case req.ProductID <= 0:
		return "product_id must be a positive integer"
	case req.Quantity < 1 || req.Quantity > 100:
		return "quantity must be between 1 and 100"
	}
	return ""
}

func (a *api) createOrder(w http.ResponseWriter, r *http.Request) {
	var req createOrderRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "body must be a JSON object with customer_email, product_id and quantity")
		return
	}
	if msg := req.problem(); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}

	order, err := a.repo.CreateOrder(r.Context(), req.CustomerEmail, req.ProductID, req.Quantity)
	switch {
	case errors.Is(err, ErrNotFound):
		writeError(w, http.StatusNotFound, "product not found")
	case err != nil:
		a.serverError(w, r, err)
	default:
		a.logger.InfoContext(r.Context(), "order created",
			slog.Int64("order.id", order.ID),
			slog.Int64("order.total_cents", order.TotalCents),
		)
		writeJSON(w, http.StatusCreated, order)
	}
}

func (a *api) getOrder(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	order, err := a.repo.GetOrder(r.Context(), id)
	switch {
	case errors.Is(err, ErrNotFound):
		writeError(w, http.StatusNotFound, "order not found")
	case err != nil:
		a.serverError(w, r, err)
	default:
		writeJSON(w, http.StatusOK, order)
	}
}

// serverError records err on the request's span, logs it in the trace
// context, and answers 500 without exposing the error to the client.
func (a *api) serverError(w http.ResponseWriter, r *http.Request, err error) {
	trace.SpanFromContext(r.Context()).RecordError(err)
	a.logger.ErrorContext(r.Context(), "request failed",
		slog.String("http.route", route(r)),
		slog.Any("error", err),
	)
	writeError(w, http.StatusInternalServerError, "internal error")
}

func (a *api) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isHealthCheck(r) {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		a.logger.LogAttrs(r.Context(), slog.LevelInfo, "request",
			slog.String("http.request.method", r.Method),
			slog.String("http.route", route(r)),
			slog.Int("http.response.status_code", rec.status),
			slog.Int64("duration_ms", time.Since(start).Milliseconds()),
		)
	})
}

// route returns the matched route without its method, for example
// "/orders/{id}". The mux sets r.Pattern on the request it is given, so it is
// known once routing has happened.
func route(r *http.Request) string {
	if _, path, ok := strings.Cut(r.Pattern, " "); ok {
		return path
	}
	if r.Pattern != "" {
		return r.Pattern
	}
	return "unmatched"
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (s *statusRecorder) Unwrap() http.ResponseWriter {
	return s.ResponseWriter
}

func validEmail(s string) bool {
	local, domain, ok := strings.Cut(s, "@")
	return ok && local != "" && domain != "" && !strings.Contains(domain, "@") && len(s) <= 254
}

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "id must be a positive integer")
		return 0, false
	}
	return id, true
}

func intQuery(w http.ResponseWriter, r *http.Request, name string, fallback, lo, hi int) (int, bool) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return fallback, true
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < lo || n > hi {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("%s must be an integer between %d and %d", name, lo, hi))
		return 0, false
	}
	return n, true
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, message string) {
	writeJSON(w, code, map[string]string{"error": message})
}
