//go:build integration

package prometheus

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"testing"
	"time"

	"infrascope/api/internal/backend"
	"infrascope/api/internal/testenv"
)

var (
	env                *testenv.Prometheus
	client             *Client
	dataStart, dataEnd time.Time
)

// fixture: checkout serves 10 req/s, 5% of them 5xx; catalog serves 4 req/s
// and has never returned a 5xx (so it has no 5xx series at all).
var fixture = []testenv.RequestSeries{
	{Service: "checkout", Environment: "local", Route: "/orders", Method: "GET", Status: 200, Rate: 9.5,
		Shares: map[float64]float64{0.05: 0.8, 0.1: 0.15, 0.25: 0.05}},
	{Service: "checkout", Environment: "local", Route: "/orders", Method: "GET", Status: 500, Rate: 0.5,
		Shares: map[float64]float64{0.25: 1}},
	{Service: "catalog", Environment: "local", Route: "/products", Method: "GET", Status: 200, Rate: 4,
		Shares: map[float64]float64{0.01: 0.5, 0.025: 0.5}},
}

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	ctx := context.Background()
	now := time.Now().UTC().Truncate(15 * time.Second)
	dataStart, dataEnd = now.Add(-41*time.Minute), now.Add(-time.Minute)

	var err error
	env, err = testenv.StartPrometheus(ctx, testenv.HistogramOpenMetrics(fixture, dataStart, dataEnd, 15*time.Second))
	if err != nil {
		fmt.Fprintln(os.Stderr, "start prometheus:", err)
		return 1
	}
	defer func() { _ = env.Container.Terminate(ctx) }()
	client = New(Config{URL: env.URL, DialTimeout: 2 * time.Second, QueryTimeout: 10 * time.Second})
	return m.Run()
}

func approx(t *testing.T, name string, got *float64, want, tolerance float64) {
	t.Helper()
	if got == nil {
		t.Errorf("%s = no data, want %v", name, want)
		return
	}
	if math.Abs(*got-want) > tolerance {
		t.Errorf("%s = %v, want %v ± %v", name, *got, want, tolerance)
	}
}

// TestServicesMatchDirectPromQL is SPEC AC-2.1: the API reports exactly what
// the equivalent PromQL returns, and the values are the ones the fixture
// was built with.
func TestServicesMatchDirectPromQL(t *testing.T) {
	ctx := context.Background()
	from, to := dataEnd.Add(-20*time.Minute), dataEnd
	services, err := client.Services(ctx, from, to)
	if err != nil {
		t.Fatalf("Services: %v", err)
	}
	if len(services) != 2 || services[0].Name != "catalog" || services[1].Name != "checkout" {
		t.Fatalf("services = %+v, want catalog and checkout, sorted", services)
	}

	window := promDuration(to.Sub(from))
	direct := map[string]string{
		"rate":  rateQuery("", window),
		"error": errorRatioQuery("", window),
		"p95":   quantileQuery(0.95, "", window),
	}
	for key, q := range direct {
		res, err := client.instant(ctx, q, to)
		if err != nil {
			t.Fatalf("direct %s: %v", key, err)
		}
		for _, s := range res {
			for _, svc := range services {
				if svc.Name != s.Labels["service_name"] {
					continue
				}
				got := map[string]*float64{"rate": svc.RequestRate, "error": svc.ErrorRate, "p95": svc.Latency.P95}[key]
				want := value(s)
				if (got == nil) != (want == nil) || (got != nil && *got != *want) {
					t.Errorf("%s %s: API %v, PromQL %v", svc.Name, key, got, want)
				}
			}
		}
	}

	checkout, catalog := services[1], services[0]
	approx(t, "checkout request rate", checkout.RequestRate, 10, 0.1)
	approx(t, "checkout error rate", checkout.ErrorRate, 0.05, 0.002)
	approx(t, "catalog error rate", catalog.ErrorRate, 0, 0)
	if p := checkout.Latency.P99; p == nil || *p <= 0.1 || *p > 0.25 {
		t.Errorf("checkout p99 = %v, want within the 0.1-0.25 s bucket", p)
	}
	if p := catalog.Latency.P50; p == nil || *p > 0.01 {
		t.Errorf("catalog p50 = %v, want at most 10 ms", p)
	}
}

// TestNoTrafficIsNoDataNotZero is SPEC AC-2.2.
func TestNoTrafficIsNoDataNotZero(t *testing.T) {
	ctx := context.Background()
	services, err := client.Services(ctx, dataStart.Add(-2*time.Hour), dataStart.Add(-time.Hour))
	if err != nil || len(services) != 0 {
		t.Errorf("window without data = %+v, %v; want no services", services, err)
	}

	m, err := client.ServiceMetrics(ctx, "checkout", dataStart.Add(-10*time.Minute), dataStart.Add(10*time.Minute), time.Minute)
	if err != nil {
		t.Fatalf("ServiceMetrics: %v", err)
	}
	if len(m.RequestRate) != 21 {
		t.Fatalf("got %d points, want 21 (one per minute, both ends included)", len(m.RequestRate))
	}
	if first := m.RequestRate[0]; first.Value != nil {
		t.Errorf("before any data: value %v, want no data (nil), not zero", *first.Value)
	}
	approx(t, "rate once data exists", m.RequestRate[len(m.RequestRate)-1].Value, 10, 0.5)
}

func TestServiceExists(t *testing.T) {
	ctx := context.Background()
	for service, want := range map[string]bool{"checkout": true, "ghost": false, `checkout"} or vector(1) #`: false} {
		got, err := client.ServiceExists(ctx, service, dataEnd, 7*24*time.Hour)
		if err != nil || got != want {
			t.Errorf("ServiceExists(%q) = %v, %v; want %v", service, got, err, want)
		}
	}
}

// TestPromQLInjectionIsInert: had the name escaped its quotes, vector(1)
// would make every point 1.
func TestPromQLInjectionIsInert(t *testing.T) {
	m, err := client.ServiceMetrics(context.Background(), `checkout"} or vector(1) #`, dataEnd.Add(-10*time.Minute), dataEnd, time.Minute)
	if err != nil {
		t.Fatalf("ServiceMetrics: %v", err)
	}
	for _, p := range m.RequestRate {
		if p.Value != nil {
			t.Fatalf("point %v = %v, want no data for a service that does not exist", p.Time, *p.Value)
		}
	}
}

func TestDeadlinesAndOutages(t *testing.T) {
	expired, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-expired.Done()
	if _, err := client.Services(expired, dataStart, dataEnd); !errors.Is(err, backend.ErrTimeout) {
		t.Errorf("expired deadline: err = %v, want ErrTimeout", err)
	}

	refused := New(Config{URL: "http://127.0.0.1:1", DialTimeout: 2 * time.Second, QueryTimeout: time.Second})
	if _, err := refused.Services(context.Background(), dataStart, dataEnd); !errors.Is(err, backend.ErrUnavailable) {
		t.Errorf("closed port: err = %v, want ErrUnavailable", err)
	}

	ctx := context.Background()
	stopped, err := testenv.StartPrometheus(ctx,
		testenv.HistogramOpenMetrics(fixture[:1], dataStart, dataStart.Add(time.Minute), 15*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stopped.Container.Terminate(ctx) })
	timeout := 5 * time.Second
	if err := stopped.Container.Stop(ctx, &timeout); err != nil {
		t.Fatal(err)
	}
	c := New(Config{URL: stopped.URL, DialTimeout: 2 * time.Second, QueryTimeout: time.Second})
	if _, err := c.Services(ctx, dataStart, dataEnd); !errors.Is(err, backend.ErrUnavailable) {
		t.Errorf("stopped server: err = %v, want ErrUnavailable", err)
	}
}
