package prometheus

import (
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"infrascope/api/internal/backend"
)

func TestQueryBuilders(t *testing.T) {
	tests := []struct {
		name, got, want string
	}{
		{
			"rate, all services",
			rateQuery("", "60s"),
			`sum by (service_name) (rate(http_server_request_duration_seconds_count[60s]))`,
		},
		{
			"error ratio, one service",
			errorRatioQuery(matchers("service_name", "checkout"), "120s"),
			`(sum by (service_name) (rate(http_server_request_duration_seconds_count{service_name="checkout",http_response_status_code=~"5.."}[120s]))` +
				` or (0 * sum by (service_name) (rate(http_server_request_duration_seconds_count{service_name="checkout"}[120s]))))` +
				` / sum by (service_name) (rate(http_server_request_duration_seconds_count{service_name="checkout"}[120s]))`,
		},
		{
			"p95",
			quantileQuery(0.95, "", "300s"),
			`histogram_quantile(0.95, sum by (service_name, le) (rate(http_server_request_duration_seconds_bucket[300s])))`,
		},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("%s:\n got %s\nwant %s", tt.name, tt.got, tt.want)
		}
	}
}

func TestMatchersCannotBeEscaped(t *testing.T) {
	// A service name trying to close the string and append its own query.
	got := matchers("service_name", `x"} or vector(1) # \`)
	want := `{service_name="x\"} or vector(1) # \\"}`
	if got != want {
		t.Errorf("matchers = %s, want %s", got, want)
	}
}

func TestAlignFillsGapsWithNoData(t *testing.T) {
	start := time.Unix(1_800_000_000, 0).UTC()
	step := 15 * time.Second
	in := []series{{Points: []point{
		{Time: start, Value: 1},
		{Time: start.Add(2 * step), Value: math.NaN()},
		{Time: start.Add(3 * step), Value: 4},
	}}}

	got := align(in, start, start.Add(3*step), step)
	if len(got) != 4 {
		t.Fatalf("got %d points, want 4", len(got))
	}
	wantNil := []bool{false, true, true, false} // a gap, then NaN
	for i, p := range got {
		if (p.Value == nil) != wantNil[i] {
			t.Errorf("point %d value = %v, want nil=%v", i, p.Value, wantNil[i])
		}
		if !p.Time.Equal(start.Add(time.Duration(i) * step)) {
			t.Errorf("point %d at %v, not on the grid", i, p.Time)
		}
	}
	if got := align(nil, start, start.Add(step), step); len(got) != 2 || got[0].Value != nil {
		t.Errorf("no series should give a grid of nil points, got %+v", got)
	}
}

func TestInstantQueryParsesValues(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil || r.Form.Get("query") == "" || r.Form.Get("timeout") != "3s" {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		_, _ = io.WriteString(w, `{"status":"success","data":{"resultType":"vector","result":[
			{"metric":{"service_name":"checkout"},"value":[1800000000.5,"12.5"]},
			{"metric":{"service_name":"idle"},"value":[1800000000.5,"NaN"]}]}}`)
	}))
	defer srv.Close()

	c := New(Config{URL: srv.URL, DialTimeout: time.Second, QueryTimeout: 3 * time.Second})
	got, err := c.instant(context.Background(), "up", time.Unix(1_800_000_000, 0))
	if err != nil {
		t.Fatalf("instant: %v", err)
	}
	if len(got) != 2 || got[0].Labels["service_name"] != "checkout" {
		t.Fatalf("got %+v", got)
	}
	if v := value(got[0]); v == nil || *v != 12.5 {
		t.Errorf("value = %v, want 12.5", v)
	}
	if v := value(got[1]); v != nil {
		t.Errorf("NaN value = %v, want nil (no data)", *v)
	}
	if want := time.Unix(1_800_000_000, 500_000_000).UTC(); !got[0].Points[0].Time.Equal(want) {
		t.Errorf("time = %v, want %v", got[0].Points[0].Time, want)
	}
}

func TestErrorsAreClassified(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   error // nil: an internal error
	}{
		{"evaluation timeout", http.StatusServiceUnavailable, `{"status":"error","errorType":"timeout","error":"query timed out"}`, backend.ErrTimeout},
		{"unavailable", http.StatusServiceUnavailable, `{"status":"error","errorType":"unavailable","error":"shutting down"}`, backend.ErrUnavailable},
		{"proxy error page", http.StatusBadGateway, `<html>bad gateway</html>`, backend.ErrUnavailable},
		{"bad query is our bug", http.StatusBadRequest, `{"status":"error","errorType":"bad_data","error":"parse error"}`, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			}))
			defer srv.Close()

			c := New(Config{URL: srv.URL, DialTimeout: time.Second, QueryTimeout: time.Second})
			_, err := c.instant(context.Background(), "up", time.Now())
			if err == nil {
				t.Fatal("want an error, got nil")
			}
			if tt.want == nil {
				if errors.Is(err, backend.ErrTimeout) || errors.Is(err, backend.ErrUnavailable) {
					t.Errorf("err = %v, want an internal error", err)
				}
				return
			}
			if !errors.Is(err, tt.want) {
				t.Errorf("err = %v, want %v", err, tt.want)
			}
		})
	}
}

// TestLabelValuesUsesGET guards a real bug: Prometheus answers POST on the
// label values endpoint with 405 Method Not Allowed.
func TestLabelValuesUsesGET(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Query().Get("match[]") == "" {
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		_, _ = io.WriteString(w, `{"status":"success","data":["catalog","checkout"]}`)
	}))
	defer srv.Close()

	c := New(Config{URL: srv.URL, DialTimeout: time.Second, QueryTimeout: time.Second})
	ok, err := c.ServiceExists(context.Background(), "checkout", time.Now(), time.Hour)
	if err != nil || !ok {
		t.Fatalf("ServiceExists = %v, %v; want true", ok, err)
	}
}

func TestUnreachableServerIsUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()

	c := New(Config{URL: url, DialTimeout: time.Second, QueryTimeout: time.Second})
	if _, err := c.instant(context.Background(), "up", time.Now()); !errors.Is(err, backend.ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
}

func TestPromDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{time.Minute: "60s", 90 * time.Minute: "5400s", 1500 * time.Millisecond: "2s"} {
		if got := promDuration(d); got != want {
			t.Errorf("promDuration(%v) = %s, want %s", d, got, want)
		}
	}
	if !strings.HasSuffix(promDuration(24*time.Hour), "s") {
		t.Error("durations must be whole seconds")
	}
}
