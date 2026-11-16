//go:build integration

package prometheus

import (
	"context"
	"testing"
	"time"

	"infrascope/api/internal/deployradar"
)

func TestWindowStats(t *testing.T) {
	ctx := context.Background()
	w := deployradar.Window{From: dataEnd.Add(-3 * time.Minute), To: dataEnd}

	checkout, err := client.WindowStats(ctx, "checkout", "local", w)
	if err != nil {
		t.Fatalf("WindowStats(checkout): %v", err)
	}
	// 10 req/s for 180 s, 5% of them 5xx. increase() extrapolates a little.
	approx(t, "checkout requests", checkout.Requests.Value, 1800, 30)
	approx(t, "checkout errors", checkout.Errors.Value, 90, 3)
	// p95 leaves out the 5xx responses (all 250 ms here). Of the 2xx, 80% are
	// under 50 ms and 95% under 100 ms, so p95 is 100 ms. With the 5xx
	// counted it would interpolate to about 173 ms.
	approx(t, "checkout p95", checkout.P95.Value, 0.1, 0.005)

	want := `sum(increase(http_server_request_duration_seconds_count{service_name="checkout",deployment_environment_name="local"}[180s]))`
	if q := checkout.Requests.Query; q.PromQL != want || q.Range != "180s" || !q.Time.Equal(dataEnd) {
		t.Errorf("requests query = %+v\nwant %s", q, want)
	}
	wantErrors := `sum(increase(http_server_request_duration_seconds_count{service_name="checkout",deployment_environment_name="local",http_response_status_code=~"5.."}[180s]))`
	if q := checkout.Errors.Query.PromQL; q != wantErrors {
		t.Errorf("errors query = %s\nwant %s", q, wantErrors)
	}
	wantP95 := `histogram_quantile(0.95, sum by (le) (increase(http_server_request_duration_seconds_bucket{service_name="checkout",deployment_environment_name="local",http_response_status_code!~"5.."}[180s])))`
	if q := checkout.P95.Query.PromQL; q != wantP95 {
		t.Errorf("p95 query = %s\nwant %s", q, wantP95)
	}

	// catalog never answered 5xx, so it has no 5xx series: zero errors, not
	// unknown. Half its requests are under 10 ms and the rest under 25 ms.
	catalog, err := client.WindowStats(ctx, "catalog", "local", w)
	if err != nil {
		t.Fatalf("WindowStats(catalog): %v", err)
	}
	approx(t, "catalog requests", catalog.Requests.Value, 720, 12)
	if catalog.Errors.Value == nil || *catalog.Errors.Value != 0 {
		t.Errorf("catalog errors = %v, want 0", catalog.Errors.Value)
	}
	approx(t, "catalog p95", catalog.P95.Value, 0.0235, 0.001)

	// An unknown service, or the wrong environment, has no series at all.
	for _, tc := range [][2]string{{"nobody", "local"}, {"checkout", "staging"}} {
		stats, err := client.WindowStats(ctx, tc[0], tc[1], w)
		if err != nil {
			t.Fatalf("WindowStats(%s, %s): %v", tc[0], tc[1], err)
		}
		if stats.Requests.Value != nil || stats.Errors.Value != nil || stats.P95.Value != nil {
			t.Errorf("%s/%s = %+v, want no data anywhere", tc[0], tc[1], stats.Sample())
		}
	}

	if _, err := client.WindowStats(ctx, "checkout", "local", deployradar.Window{From: dataEnd, To: dataEnd}); err == nil {
		t.Error("empty window accepted")
	}
}
