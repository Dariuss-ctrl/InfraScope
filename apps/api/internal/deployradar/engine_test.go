package deployradar

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"infrascope/api/internal/backend"
)

var now = time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)

type fakeStore struct {
	mu        sync.Mutex
	claims    []ClaimedComparison // handed out by the next ClaimComparisons
	claimErr  error
	finishErr error
	options   ClaimOptions
	finished  map[int64]ComparisonOutcome
	ticks     int

	successes    []Success // returned by SuccessesBetween
	successesErr error
	successesArg []any // the last SuccessesBetween arguments
}

func (s *fakeStore) SuccessesBetween(_ context.Context, service, environment string, exclude int64, from, to time.Time) ([]Success, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.successesArg = []any{service, environment, exclude, from, to}
	return s.successes, s.successesErr
}

func (s *fakeStore) RecordDelivery(context.Context, Delivery) (Receipt, error) { return Receipt{}, nil }
func (s *fakeStore) FinishDelivery(context.Context, string, Outcome, string, time.Time) error {
	return nil
}
func (s *fakeStore) ApplyStatus(context.Context, StatusUpdate, Windows, time.Time) (ApplyResult, error) {
	return ApplyResult{}, nil
}
func (s *fakeStore) ListDeployments(context.Context, DeploymentFilter) ([]DeploymentSummary, error) {
	return nil, nil
}
func (s *fakeStore) Deployment(context.Context, int64) (DeploymentDetail, error) {
	return DeploymentDetail{}, backend.ErrNotFound
}

func (s *fakeStore) ClaimComparisons(_ context.Context, o ClaimOptions) ([]ClaimedComparison, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ticks++
	s.options = o
	if s.claimErr != nil {
		return nil, s.claimErr
	}
	out := s.claims
	s.claims = nil
	return out, nil
}

func (s *fakeStore) FinishComparison(_ context.Context, id int64, o ComparisonOutcome) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finishErr != nil {
		return s.finishErr
	}
	if s.finished == nil {
		s.finished = map[int64]ComparisonOutcome{}
	}
	s.finished[id] = o
	return nil
}

type fakeMetrics struct {
	stats func(service, environment string, w Window) (WindowStats, error)
	calls []Window
}

func (m *fakeMetrics) WindowStats(_ context.Context, service, environment string, w Window) (WindowStats, error) {
	m.calls = append(m.calls, w)
	return m.stats(service, environment, w)
}

func measured(v float64, promql string, w Window) Measurement {
	return Measurement{Value: &v, Query: Query{PromQL: promql, Time: w.To, Range: "180s"}}
}

func claim(attempts int) ClaimedComparison {
	success := now.Add(-10 * time.Minute)
	before, after := (Windows{Width: 3 * time.Minute, Settle: time.Minute}).Around(success)
	return ClaimedComparison{
		Comparison: Comparison{ID: 7, DeploymentID: 3, Status: ComparisonPending, Attempts: attempts, Before: before, After: after},
		Deployment: Deployment{ID: 3, Facts: Facts{Service: "demo-service", Environment: "local"}, State: StateSuccess},
	}
}

func newEngine(store *fakeStore, metrics *fakeMetrics) *Engine {
	return &Engine{
		Store: store, Metrics: metrics, Thresholds: DefaultThresholds(200),
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:    func() time.Time { return now },
		Grace:  30 * time.Second, Lease: 2 * time.Minute, MaxAttempts: 5, Batch: 10,
	}
}

func TestTickComputesAndStoresAVerdict(t *testing.T) {
	c := claim(1)
	store := &fakeStore{claims: []ClaimedComparison{c}}
	metrics := &fakeMetrics{stats: func(service, environment string, w Window) (WindowStats, error) {
		if service != "demo-service" || environment != "local" {
			t.Errorf("measured %s/%s", service, environment)
		}
		q := func(what string) string {
			return what + `{service_name="demo-service"}[180s] @ ` + w.To.Format(time.RFC3339)
		}
		if w == c.Before {
			return WindowStats{Requests: measured(1200, q("requests"), w), Errors: measured(5, q("errors"), w), P95: measured(0.120, q("p95"), w)}, nil
		}
		return WindowStats{Requests: measured(1180, q("requests"), w), Errors: measured(116, q("errors"), w), P95: measured(0.131, q("p95"), w)}, nil
	}}

	if n := newEngine(store, metrics).Tick(context.Background()); n != 1 {
		t.Fatalf("Tick claimed %d, want 1", n)
	}
	if o := store.options; o.Now != now || o.Grace != 30*time.Second || o.Lease != 2*time.Minute || o.MaxAttempts != 5 || o.Limit != 10 {
		t.Errorf("claim options = %+v", o)
	}
	if len(metrics.calls) != 2 || metrics.calls[0] != c.Before || metrics.calls[1] != c.After {
		t.Errorf("windows measured = %v, want before then after", metrics.calls)
	}

	o := store.finished[7]
	if o.Status != ComparisonDone || o.Result == nil || o.At != now {
		t.Fatalf("outcome = %+v, want done", o)
	}
	r := o.Result
	if r.Verdict != VerdictRegression || r.ErrorRateVerdict != VerdictRegression || r.P95Verdict != VerdictNoChange {
		t.Errorf("verdicts = %s / %s / %s", r.Verdict, r.ErrorRateVerdict, r.P95Verdict)
	}
	if r.BeforeRequests != 1200 || r.AfterErrors != 116 || *r.AfterP95 != 0.131 || r.MethodVersion != MethodVersion || r.ComputedAt != now {
		t.Errorf("result = %+v", r)
	}
	for _, key := range []string{"before_requests", "before_errors", "before_p95", "after_requests", "after_errors", "after_p95"} {
		q, ok := r.Queries[key]
		if !ok || q.PromQL == "" || q.Range != "180s" {
			t.Errorf("queries[%s] = %+v", key, q)
		}
	}
	if !strings.HasPrefix(r.Queries["after_errors"].PromQL, "errors{") || !r.Queries["after_errors"].Time.Equal(c.After.To) {
		t.Errorf("after_errors query = %+v, want the exact query and the window's end", r.Queries["after_errors"])
	}
	if err := o.Validate(); err != nil {
		t.Errorf("outcome invalid: %v", err)
	}
}

func TestPrometheusOutagesAreRetriedThenGivenUp(t *testing.T) {
	down := &fakeMetrics{stats: func(string, string, Window) (WindowStats, error) {
		return WindowStats{}, backend.ErrUnavailable
	}}

	store := &fakeStore{claims: []ClaimedComparison{claim(1)}}
	newEngine(store, down).Tick(context.Background())
	if o := store.finished[7]; o.Status != ComparisonPending || !strings.HasPrefix(o.Error, "attempt 1: ") || o.Result != nil {
		t.Errorf("first attempt = %+v, want pending for retry", o)
	}

	store = &fakeStore{claims: []ClaimedComparison{claim(5)}}
	newEngine(store, down).Tick(context.Background())
	if o := store.finished[7]; o.Status != ComparisonFailed || !strings.Contains(o.Error, "attempt 5") {
		t.Errorf("last attempt = %+v, want failed", o)
	}

	timeout := &fakeMetrics{stats: func(string, string, Window) (WindowStats, error) {
		return WindowStats{}, backend.ErrTimeout
	}}
	store = &fakeStore{claims: []ClaimedComparison{claim(2)}}
	newEngine(store, timeout).Tick(context.Background())
	if o := store.finished[7]; o.Status != ComparisonPending {
		t.Errorf("timeout = %+v, want pending for retry", o)
	}
}

func TestOverlappingDeploymentsReachTheVerdict(t *testing.T) {
	c := claim(1)
	store := &fakeStore{
		claims:    []ClaimedComparison{c},
		successes: []Success{{DeploymentID: 12, At: c.After.From.Add(time.Minute)}},
	}
	steady := &fakeMetrics{stats: func(_, _ string, w Window) (WindowStats, error) {
		return WindowStats{Requests: measured(1800, "r", w), Errors: measured(0, "e", w), P95: measured(0.05, "p", w)}, nil
	}}
	newEngine(store, steady).Tick(context.Background())

	want := []any{"demo-service", "local", int64(3), c.Before.From, c.After.To}
	if fmt.Sprint(store.successesArg) != fmt.Sprint(want) {
		t.Errorf("SuccessesBetween(%v), want (%v): this deployment excluded, across both windows", store.successesArg, want)
	}
	r := store.finished[7].Result
	if r == nil || r.Confidence > 0.5 || !strings.Contains(strings.Join(r.Reasons, "\n"), "deployment 12 succeeded during the after window") {
		t.Errorf("result = %+v, want the overlap named and the confidence capped", r)
	}
	if r.MethodVersion != 2 {
		t.Errorf("method version = %d, want 2", r.MethodVersion)
	}

	// Not knowing the overlaps is a storage failure like any other: retried.
	store = &fakeStore{claims: []ClaimedComparison{claim(1)}, successesErr: backend.ErrUnavailable}
	newEngine(store, steady).Tick(context.Background())
	if o := store.finished[7]; o.Status != ComparisonPending {
		t.Errorf("outcome = %+v, want pending for retry", o)
	}
}

func TestBadQueriesFailAtOnce(t *testing.T) {
	broken := &fakeMetrics{stats: func(string, string, Window) (WindowStats, error) {
		return WindowStats{}, errors.New("prometheus: bad_data: parse error")
	}}
	store := &fakeStore{claims: []ClaimedComparison{claim(1)}}
	newEngine(store, broken).Tick(context.Background())
	if o := store.finished[7]; o.Status != ComparisonFailed || !strings.Contains(o.Error, "parse error") {
		t.Errorf("outcome = %+v, want failed on the first attempt", o)
	}
}

func TestNoSeriesIsInsufficientData(t *testing.T) {
	empty := &fakeMetrics{stats: func(_, _ string, w Window) (WindowStats, error) {
		q := Query{PromQL: "sum(increase(...))", Time: w.To, Range: "180s"}
		return WindowStats{Requests: Measurement{Query: q}, Errors: Measurement{Query: q}, P95: Measurement{Query: q}}, nil
	}}
	store := &fakeStore{claims: []ClaimedComparison{claim(1)}}
	newEngine(store, empty).Tick(context.Background())
	o := store.finished[7]
	if o.Status != ComparisonDone || o.Result.Verdict != VerdictInsufficientData || o.Result.Confidence != 0 {
		t.Fatalf("outcome = %+v, want done with insufficient_data", o)
	}
	if o.Result.BeforeP95 != nil || o.Result.BeforeRequests != 0 || len(o.Result.Reasons) != 2 {
		t.Errorf("result = %+v", o.Result)
	}
}

func TestStorageErrorsEndTheTick(t *testing.T) {
	store := &fakeStore{claimErr: backend.ErrUnavailable}
	metrics := &fakeMetrics{stats: func(string, string, Window) (WindowStats, error) {
		t.Error("metrics queried although nothing was claimed")
		return WindowStats{}, nil
	}}
	if n := newEngine(store, metrics).Tick(context.Background()); n != 0 {
		t.Errorf("Tick = %d, want 0", n)
	}

	store = &fakeStore{claims: []ClaimedComparison{claim(1)}, finishErr: backend.ErrUnavailable}
	ok := &fakeMetrics{stats: func(_, _ string, w Window) (WindowStats, error) {
		return WindowStats{Requests: measured(1000, "r", w), Errors: measured(0, "e", w), P95: measured(0.1, "p", w)}, nil
	}}
	if n := newEngine(store, ok).Tick(context.Background()); n != 1 {
		t.Errorf("Tick = %d, want 1 even though storing failed (the lease frees the row)", n)
	}
}

func TestRunStopsWhenCancelled(t *testing.T) {
	store := &fakeStore{}
	e := newEngine(store, &fakeMetrics{stats: func(string, string, Window) (WindowStats, error) { return WindowStats{}, nil }})
	e.Interval = time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		e.Run(ctx)
		close(done)
	}()
	deadline := time.After(2 * time.Second)
	for {
		store.mu.Lock()
		ticks := store.ticks
		store.mu.Unlock()
		if ticks >= 3 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("only %d ticks in 2 s", ticks)
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

func TestEngineDefaults(t *testing.T) {
	e := &Engine{}
	e.defaults()
	if e.Interval != 15*time.Second || e.Grace != 30*time.Second || e.Lease != 2*time.Minute || e.MaxAttempts != 5 || e.Batch != 10 || e.Now == nil || e.Logger == nil {
		t.Errorf("defaults = %+v", e)
	}
}
