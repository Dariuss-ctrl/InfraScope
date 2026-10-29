package shop

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// fakeRepo serves product 7 and order 1; everything else is not found.
type fakeRepo struct {
	err       error // returned by every method when set
	lastLimit int
}

func (f *fakeRepo) ListProducts(_ context.Context, limit, _ int) ([]Product, error) {
	f.lastLimit = limit
	return []Product{{ID: 7, Name: "Product 7", PriceCents: 1299}}, f.err
}

func (f *fakeRepo) GetProduct(_ context.Context, id int64) (Product, error) {
	if f.err != nil {
		return Product{}, f.err
	}
	if id != 7 {
		return Product{}, ErrNotFound
	}
	return Product{ID: 7, Name: "Product 7", PriceCents: 1299}, nil
}

func (f *fakeRepo) OrdersByCustomer(_ context.Context, email string, limit int) ([]Order, error) {
	f.lastLimit = limit
	return []Order{{ID: 1, CustomerEmail: email}}, f.err
}

func (f *fakeRepo) CreateOrder(_ context.Context, email string, productID int64, quantity int32) (Order, error) {
	if f.err != nil {
		return Order{}, f.err
	}
	if productID != 7 {
		return Order{}, ErrNotFound
	}
	return Order{ID: 2, CustomerEmail: email, ProductID: productID, Quantity: quantity, TotalCents: int64(quantity) * 1299, CreatedAt: time.Now()}, nil
}

func (f *fakeRepo) GetOrder(_ context.Context, id int64) (Order, error) {
	if f.err != nil {
		return Order{}, f.err
	}
	if id != 1 {
		return Order{}, ErrNotFound
	}
	return Order{ID: 1}, nil
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func do(h http.Handler, method, target, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	h.ServeHTTP(rec, req)
	return rec
}

func TestRoutes(t *testing.T) {
	const validOrder = `{"customer_email":"ada@example.com","product_id":7,"quantity":2}`
	tests := []struct {
		name, method, target, body string
		want                       int
	}{
		{"health", "GET", "/healthz", "", http.StatusOK},
		{"index", "GET", "/", "", http.StatusOK},
		{"list products", "GET", "/products", "", http.StatusOK},
		{"list products, limit too high", "GET", "/products?limit=101", "", http.StatusBadRequest},
		{"list products, limit not a number", "GET", "/products?limit=ten", "", http.StatusBadRequest},
		{"get product", "GET", "/products/7", "", http.StatusOK},
		{"get unknown product", "GET", "/products/8", "", http.StatusNotFound},
		{"get product, bad id", "GET", "/products/abc", "", http.StatusBadRequest},
		{"get product, zero id", "GET", "/products/0", "", http.StatusBadRequest},
		{"orders by customer", "GET", "/orders?customer_email=ada@example.com", "", http.StatusOK},
		{"orders by customer, missing email", "GET", "/orders", "", http.StatusBadRequest},
		{"orders by customer, invalid email", "GET", "/orders?customer_email=ada", "", http.StatusBadRequest},
		{"create order", "POST", "/orders", validOrder, http.StatusCreated},
		{"create order, unknown product", "POST", "/orders", `{"customer_email":"ada@example.com","product_id":8,"quantity":1}`, http.StatusNotFound},
		{"create order, quantity zero", "POST", "/orders", `{"customer_email":"ada@example.com","product_id":7,"quantity":0}`, http.StatusBadRequest},
		{"create order, unknown field", "POST", "/orders", `{"customer_email":"ada@example.com","product_id":7,"quantity":1,"discount":50}`, http.StatusBadRequest},
		{"create order, not JSON", "POST", "/orders", `quantity=1`, http.StatusBadRequest},
		{"get order", "GET", "/orders/1", "", http.StatusOK},
		{"get unknown order", "GET", "/orders/2", "", http.StatusNotFound},
		{"unknown route", "GET", "/carts", "", http.StatusNotFound},
		{"wrong method", "DELETE", "/orders/1", "", http.StatusMethodNotAllowed},
	}
	h := NewHandler(&fakeRepo{}, discardLogger())
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if rec := do(h, tt.method, tt.target, tt.body); rec.Code != tt.want {
				t.Errorf("%s %s = %d, want %d (body %s)", tt.method, tt.target, rec.Code, tt.want, rec.Body)
			}
		})
	}
}

func TestPaginationDefaults(t *testing.T) {
	repo := &fakeRepo{}
	do(NewHandler(repo, discardLogger()), "GET", "/products", "")
	if repo.lastLimit != 20 {
		t.Errorf("default limit = %d, want 20", repo.lastLimit)
	}
}

func TestStorageFailureIsLoggedNotLeaked(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	h := NewHandler(&fakeRepo{err: errors.New("connection reset by peer")}, logger)

	rec := do(h, "GET", "/orders/1", "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "connection reset") {
		t.Errorf("response leaks the internal error: %s", rec.Body)
	}
	if !strings.Contains(logs.String(), `"msg":"request failed"`) || !strings.Contains(logs.String(), "connection reset") {
		t.Errorf("error was not logged with its cause; logs:\n%s", logs.String())
	}
	if !strings.Contains(logs.String(), `"http.route":"/orders/{id}"`) {
		t.Errorf("log record does not name the route; logs:\n%s", logs.String())
	}
}

func TestInstrumentNamesSpansAfterRoutesAndReturnsTraceID(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	h := Instrument(NewHandler(&fakeRepo{}, discardLogger()), otelhttp.WithTracerProvider(tp))

	rec := do(h, "GET", "/orders/1", "")
	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want 1", len(spans))
	}
	if got, want := spans[0].Name, "GET /orders/{id}"; got != want {
		t.Errorf("span name = %q, want %q", got, want)
	}
	if got, want := rec.Header().Get("X-Trace-Id"), spans[0].SpanContext.TraceID().String(); got != want {
		t.Errorf("X-Trace-Id = %q, want the span's trace ID %q", got, want)
	}

	exporter.Reset()
	do(h, "GET", "/healthz", "")
	if n := len(exporter.GetSpans()); n != 0 {
		t.Errorf("health check produced %d spans, want 0", n)
	}
}
