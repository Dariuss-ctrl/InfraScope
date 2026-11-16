package shop

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// sequence returns the given numbers in turn, as a deterministic random.
func sequence(values ...float64) func() float64 {
	i := 0
	return func() float64 {
		v := values[i%len(values)]
		i++
		return v
	}
}

func TestWithFaultsZeroRateIsTheRepositoryItself(t *testing.T) {
	repo := &fakeRepo{}
	if got := WithFaults(repo, 0, func() float64 { panic("random called with faults off") }); got != Repository(repo) {
		t.Errorf("WithFaults(rate 0) = %T, want the repository unwrapped", got)
	}
}

func TestWithFaultsFailsTheConfiguredShare(t *testing.T) {
	// A rate of 0.2 fails exactly the draws below 0.2.
	repo := WithFaults(&fakeRepo{}, 0.2, sequence(0.1, 0.5, 0.19, 0.2, 0.9))
	ctx := context.Background()
	var failures int
	for range 5 {
		if _, err := repo.GetProduct(ctx, 7); errors.Is(err, ErrInjectedFault) {
			failures++
		} else if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if failures != 2 {
		t.Errorf("%d of 5 calls failed, want 2 (draws 0.1 and 0.19)", failures)
	}

	// Every method is covered.
	always := WithFaults(&fakeRepo{}, 1, func() float64 { return 0.5 })
	calls := map[string]error{}
	_, calls["ListProducts"] = always.ListProducts(ctx, 10, 0)
	_, calls["GetProduct"] = always.GetProduct(ctx, 7)
	_, calls["OrdersByCustomer"] = always.OrdersByCustomer(ctx, "a@b.c", 10)
	_, calls["CreateOrder"] = always.CreateOrder(ctx, "a@b.c", 7, 1)
	_, calls["GetOrder"] = always.GetOrder(ctx, 1)
	for name, err := range calls {
		if !errors.Is(err, ErrInjectedFault) {
			t.Errorf("%s = %v, want the injected fault", name, err)
		}
	}
}

// An injected fault must look like a real failure from outside: a 500, an
// exception on the server span, and an ERROR log under the real route.
func TestInjectedFaultLooksLikeARealFailure(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))

	h := Instrument(NewHandler(WithFaults(&fakeRepo{}, 1, func() float64 { return 0 }), logger),
		otelhttp.WithTracerProvider(provider))

	rec := do(h, http.MethodGet, "/products/7", "")
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "injected") {
		t.Errorf("status %d body %s, want a plain 500 that does not reveal the fault", rec.Code, rec.Body)
	}
	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("%d spans, want 1", len(spans))
	}
	var exception bool
	for _, e := range spans[0].Events() {
		exception = exception || e.Name == "exception"
	}
	if !exception {
		t.Error("the server span has no exception event")
	}
	if out := logs.String(); !strings.Contains(out, "level=ERROR") || !strings.Contains(out, "http.route=/products/{id}") ||
		!strings.Contains(out, "injected fault") {
		t.Errorf("logs = %s, want an ERROR under the real route", out)
	}

	// Requests rejected before storage are never faulted.
	if rec := do(h, http.MethodGet, "/products/abc", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("invalid id = %d, want 400 even with every storage call failing", rec.Code)
	}
}
