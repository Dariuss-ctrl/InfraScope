//go:build integration

package deployradar_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"infrascope/api/internal/deployradar"
	"infrascope/api/internal/postgres"
	"infrascope/api/internal/prometheus"
	"infrascope/api/internal/testenv"
)

// The milestone, end to end against real stores: a deployment at T turns a
// healthy service (0.5% errors) into a broken one (10% errors, each failure
// taking 250 ms). The engine must say regression, with the numbers and the
// queries behind them. The successful requests stay at 50 ms, and p95 leaves
// the failures out, so p95 must not move. A second, quiet service must get
// insufficient_data rather than a guess.
func TestEngineDetectsARegressionEndToEnd(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Truncate(15 * time.Second)
	deployAt := now.Add(-12 * time.Minute)
	windows := deployradar.Windows{Width: 3 * time.Minute, Settle: time.Minute}

	// Counters that start at T are new series, exactly like a restarted
	// service; the old ones stop at T. increase() only uses samples inside
	// each window, so the before window sees v1 and the after window v2.
	v1 := func(s testenv.RequestSeries) testenv.RequestSeries { s.End = deployAt; return s }
	v2 := func(s testenv.RequestSeries) testenv.RequestSeries { s.Start = deployAt; return s }
	series := []testenv.RequestSeries{
		v1(testenv.RequestSeries{Service: "demo-service", Environment: "local", Route: "/orders", Method: "GET", Status: 200, Rate: 9.95, Shares: map[float64]float64{0.05: 1}}),
		v1(testenv.RequestSeries{Service: "demo-service", Environment: "local", Route: "/orders", Method: "GET", Status: 500, Rate: 0.05, Shares: map[float64]float64{0.05: 1}}),
		v1(testenv.RequestSeries{Service: "sleepy", Environment: "local", Route: "/ping", Method: "GET", Status: 200, Rate: 0.2, Shares: map[float64]float64{0.01: 1}}),
		v2(testenv.RequestSeries{Service: "demo-service", Environment: "local", Route: "/orders", Method: "POST", Status: 200, Rate: 9, Shares: map[float64]float64{0.05: 1}}),
		v2(testenv.RequestSeries{Service: "demo-service", Environment: "local", Route: "/orders", Method: "POST", Status: 500, Rate: 1, Shares: map[float64]float64{0.25: 1}}),
		v2(testenv.RequestSeries{Service: "sleepy", Environment: "local", Route: "/ping", Method: "POST", Status: 200, Rate: 0.2, Shares: map[float64]float64{0.01: 1}}),
	}
	exposition := testenv.HistogramOpenMetrics(series, deployAt.Add(-10*time.Minute), deployAt.Add(10*time.Minute), 15*time.Second)

	var (
		wg             sync.WaitGroup
		pg             *testenv.Postgres
		prom           *testenv.Prometheus
		pgErr, promErr error
	)
	wg.Go(func() { pg, pgErr = testenv.StartPostgres(ctx) })
	wg.Go(func() { prom, promErr = testenv.StartPrometheus(ctx, exposition) })
	wg.Wait()
	if pgErr != nil || promErr != nil {
		t.Fatalf("start containers: postgres %v, prometheus %v", pgErr, promErr)
	}
	t.Cleanup(func() {
		_ = pg.Container.Terminate(ctx)
		_ = prom.Container.Terminate(ctx)
	})

	db, err := postgres.Open(ctx, pg.URL, postgres.Options{ConnectTimeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	metrics := prometheus.New(prometheus.Config{URL: prom.URL, DialTimeout: 2 * time.Second, QueryTimeout: 10 * time.Second})

	deploy := func(service string) int64 {
		t.Helper()
		id := "d-" + service
		if _, err := db.RecordDelivery(ctx, deployradar.Delivery{ID: id, Event: "deployment_status", Payload: json.RawMessage(`{}`), ReceivedAt: now}); err != nil {
			t.Fatal(err)
		}
		res, err := db.ApplyStatus(ctx, deployradar.StatusUpdate{
			DeliveryID: id,
			Facts: deployradar.Facts{
				Provider: deployradar.ProviderGitHub, Repository: "acme/" + service, ProviderDeploymentID: service,
				Service: service, Environment: "local", Ref: "main", SHA: "0123456789abcdef0123456789abcdef01234567",
				CreatedAt: deployAt.Add(-time.Minute),
			},
			State: deployradar.StateSuccess, StateAt: deployAt,
		}, windows, now)
		if err != nil {
			t.Fatal(err)
		}
		if !res.ComparisonQueued {
			t.Fatalf("%s: comparison not queued", service)
		}
		return res.Deployment.ID
	}
	demoID, sleepyID := deploy("demo-service"), deploy("sleepy")

	engine := &deployradar.Engine{
		Store: db, Metrics: metrics, Thresholds: deployradar.DefaultThresholds(200),
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Grace:  time.Second, // the history is already there
	}
	if n := engine.Tick(ctx); n != 2 {
		t.Fatalf("first tick claimed %d comparisons, want 2", n)
	}
	if n := engine.Tick(ctx); n != 0 {
		t.Errorf("second tick claimed %d, want 0: both are done", n)
	}

	demo, err := db.Deployment(ctx, demoID)
	if err != nil {
		t.Fatal(err)
	}
	c := demo.Comparison
	if c == nil || c.Status != deployradar.ComparisonDone || c.Result == nil {
		t.Fatalf("demo comparison = %+v, want done", c)
	}
	r := c.Result
	if r.Verdict != deployradar.VerdictRegression || r.ErrorRateVerdict != deployradar.VerdictRegression {
		t.Errorf("verdict = %s (error rate %s, p95 %s), want regression. Reasons: %q", r.Verdict, r.ErrorRateVerdict, r.P95Verdict, r.Reasons)
	}
	// 10 req/s over 3 minutes is 1800 requests; increase() extrapolates a
	// little at the edges of a window.
	approx(t, "before requests", r.BeforeRequests, 1800, 0.1)
	approx(t, "before errors", r.BeforeErrors, 9, 0.25)
	approx(t, "after requests", r.AfterRequests, 1800, 0.1)
	approx(t, "after errors", r.AfterErrors, 180, 0.1)
	if r.BeforeP95 == nil || r.AfterP95 == nil || *r.AfterP95 > 0.06 || r.P95Verdict != deployradar.VerdictNoChange {
		t.Errorf("p95 before %v, after %v (%s); want the slow failures left out and no change", r.BeforeP95, r.AfterP95, r.P95Verdict)
	}
	if q := r.Queries["after_p95"].PromQL; !strings.Contains(q, `http_response_status_code!~"5.."`) {
		t.Errorf("after_p95 query = %s, want 5xx left out", q)
	}
	if r.Confidence < 0.99 || !strings.Contains(r.Reasons[0], "error rate 0.5% → 10.0%") || !strings.Contains(r.Reasons[0], "p<0.001") {
		t.Errorf("confidence %v, reasons %q", r.Confidence, r.Reasons)
	}
	q := r.Queries["after_errors"]
	want := `sum(increase(http_server_request_duration_seconds_count{service_name="demo-service",deployment_environment_name="local",http_response_status_code=~"5.."}[180s]))`
	if q.PromQL != want || !q.Time.Equal(c.After.To) || q.Range != "180s" {
		t.Errorf("after_errors query = %+v\nwant %s at %v", q, want, c.After.To)
	}
	if len(r.Queries) != 10 || r.MethodVersion != deployradar.MethodVersion {
		t.Errorf("queries %d, method %d", len(r.Queries), r.MethodVersion)
	}
	// Per route: the service has one, and its counts match the totals.
	if len(r.Routes) != 1 || r.Routes[0].Route != "/orders" {
		t.Fatalf("routes = %+v, want /orders", r.Routes)
	}
	approx(t, "/orders before requests", r.Routes[0].BeforeRequests, 1800, 0.1)
	approx(t, "/orders after errors", r.Routes[0].AfterErrors, 180, 0.1)
	if !strings.Contains(r.Queries["after_errors_by_route"].PromQL, "sum by (http_route)") {
		t.Errorf("after_errors_by_route = %+v", r.Queries["after_errors_by_route"])
	}

	sleepy, err := db.Deployment(ctx, sleepyID)
	if err != nil {
		t.Fatal(err)
	}
	sr := sleepy.Comparison.Result
	if sleepy.Comparison.Status != deployradar.ComparisonDone || sr == nil || sr.Verdict != deployradar.VerdictInsufficientData || sr.Confidence != 0 {
		t.Fatalf("sleepy comparison = %+v, want done with insufficient_data", sleepy.Comparison)
	}
	if len(sr.Reasons) != 2 || !strings.Contains(sr.Reasons[0], "fewer than the 200 needed") {
		t.Errorf("sleepy reasons = %q", sr.Reasons)
	}
	approx(t, "sleepy requests", sr.BeforeRequests, 36, 0.25)
}

func approx(t *testing.T, name string, got, want, tolerance float64) {
	t.Helper()
	if diff := got - want; diff < -want*tolerance || diff > want*tolerance {
		t.Errorf("%s = %v, want %v ± %.0f%%", name, got, want, tolerance*100)
	}
}
