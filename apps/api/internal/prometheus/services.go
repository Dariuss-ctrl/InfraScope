package prometheus

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// durationMetric is the OpenTelemetry http.server.request.duration histogram
// after Prometheus's OTLP translation (SPEC §7.2).
const durationMetric = "http_server_request_duration_seconds"

// minRateWindow keeps rate() windows at least four metric export intervals
// (15 s) wide, so every window holds enough samples to compute a rate.
const minRateWindow = time.Minute

// Latency holds latency percentiles in seconds; nil means no data.
type Latency struct {
	P50, P95, P99 *float64
}

// ServiceSummary is one service's rate, error ratio and latency over a range.
// Nil values mean the service had no requests in the range ("no data", not
// zero).
type ServiceSummary struct {
	Name        string
	RequestRate *float64 // requests per second
	ErrorRate   *float64 // share of responses with a 5xx status, 0-1
	Latency     Latency
}

// Services summarises every service that reported requests between from and
// to, sorted by name.
func (c *Client) Services(ctx context.Context, from, to time.Time) ([]ServiceSummary, error) {
	window := promDuration(max(to.Sub(from), minRateWindow))
	queries := map[string]string{
		"rate":  rateQuery("", window),
		"error": errorRatioQuery("", window),
		"p50":   quantileQuery(0.50, "", window),
		"p95":   quantileQuery(0.95, "", window),
		"p99":   quantileQuery(0.99, "", window),
	}
	results, err := all(queries, func(q string) ([]series, error) { return c.instant(ctx, q, to) })
	if err != nil {
		return nil, err
	}

	byName := map[string]*ServiceSummary{}
	for _, s := range results["rate"] {
		name := s.Labels["service_name"]
		byName[name] = &ServiceSummary{Name: name, RequestRate: value(s)}
	}
	set := func(key string, assign func(*ServiceSummary, *float64)) {
		for _, s := range results[key] {
			if sum, ok := byName[s.Labels["service_name"]]; ok {
				assign(sum, value(s))
			}
		}
	}
	set("error", func(s *ServiceSummary, v *float64) { s.ErrorRate = v })
	set("p50", func(s *ServiceSummary, v *float64) { s.Latency.P50 = v })
	set("p95", func(s *ServiceSummary, v *float64) { s.Latency.P95 = v })
	set("p99", func(s *ServiceSummary, v *float64) { s.Latency.P99 = v })

	out := make([]ServiceSummary, 0, len(byName))
	for _, s := range byName {
		out = append(out, *s)
	}
	slices.SortFunc(out, func(a, b ServiceSummary) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

// Point is one value of a time series; a nil Value means no data at Time.
type Point struct {
	Time  time.Time
	Value *float64
}

// ServiceMetrics are one service's series over a range, aligned to Step.
type ServiceMetrics struct {
	Service     string
	Step        time.Duration
	RequestRate []Point
	ErrorRate   []Point
	P50         []Point
	P95         []Point
	P99         []Point
}

// ServiceMetrics returns the overview series for one service. Every series
// has a point per step from from to to; steps without data have nil values.
func (c *Client) ServiceMetrics(ctx context.Context, service string, from, to time.Time, step time.Duration) (ServiceMetrics, error) {
	selector := matchers("service_name", service)
	window := promDuration(max(2*step, minRateWindow))
	start, end := from.Truncate(step), to.Truncate(step)

	queries := map[string]string{
		"rate":  rateQuery(selector, window),
		"error": errorRatioQuery(selector, window),
		"p50":   quantileQuery(0.50, selector, window),
		"p95":   quantileQuery(0.95, selector, window),
		"p99":   quantileQuery(0.99, selector, window),
	}
	results, err := all(queries, func(q string) ([]series, error) { return c.rangeQuery(ctx, q, start, end, step) })
	if err != nil {
		return ServiceMetrics{}, err
	}

	grid := func(key string) []Point { return align(results[key], start, end, step) }
	return ServiceMetrics{
		Service:     service,
		Step:        step,
		RequestRate: grid("rate"),
		ErrorRate:   grid("error"),
		P50:         grid("p50"),
		P95:         grid("p95"),
		P99:         grid("p99"),
	}, nil
}

// ServiceExists reports whether service has reported request metrics in the
// lookback window before at, so an unknown service can be told apart from a
// known one without traffic in the requested range.
func (c *Client) ServiceExists(ctx context.Context, service string, at time.Time, lookback time.Duration) (bool, error) {
	names, err := c.labelValues(ctx, "service_name", durationMetric+"_count", at.Add(-lookback), at)
	if err != nil {
		return false, err
	}
	return slices.Contains(names, service), nil
}

// all runs fn for every query concurrently and returns the results by key,
// or the first error.
func all(queries map[string]string, fn func(query string) ([]series, error)) (map[string][]series, error) {
	results := make(map[string][]series, len(queries))
	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		firstErr error
	)
	for key, q := range queries {
		wg.Go(func() {
			s, err := fn(q)
			mu.Lock()
			defer mu.Unlock()
			if err != nil && firstErr == nil {
				firstErr = err
			}
			results[key] = s
		})
	}
	wg.Wait()
	return results, firstErr
}

// rateQuery is requests per second, per service.
func rateQuery(selector, window string) string {
	return fmt.Sprintf("sum by (service_name) (rate(%s_count%s[%s]))", durationMetric, selector, window)
}

// errorRatioQuery is the share of 5xx responses per service. Services that
// never returned a 5xx have no such series, so the numerator falls back to
// zero times the total: 0 when there is traffic, NaN (no data) when there
// is none.
func errorRatioQuery(selector, window string) string {
	total := rateQuery(selector, window)
	failures := rateQuery(addMatcher(selector, `http_response_status_code=~"5.."`), window)
	return fmt.Sprintf("(%s or (0 * %s)) / %s", failures, total, total)
}

// quantileQuery is the q-quantile of request duration per service, in seconds.
func quantileQuery(q float64, selector, window string) string {
	return fmt.Sprintf("histogram_quantile(%s, sum by (service_name, le) (rate(%s_bucket%s[%s])))",
		strconv.FormatFloat(q, 'f', -1, 64), durationMetric, selector, window)
}

// matchers builds a label selector such as {service_name="checkout"}.
// Values are quoted with Go string syntax, which is exactly how PromQL
// parses double-quoted strings, so no value can escape its quotes.
func matchers(pairs ...string) string {
	var parts []string
	for i := 0; i+1 < len(pairs); i += 2 {
		parts = append(parts, pairs[i]+"="+strconv.Quote(pairs[i+1]))
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// addMatcher appends a raw matcher to selector (which may be empty).
func addMatcher(selector, matcher string) string {
	if selector == "" {
		return "{" + matcher + "}"
	}
	return strings.TrimSuffix(selector, "}") + "," + matcher + "}"
}

// align places series values on a regular grid from start to end. Prometheus
// omits steps without data; those become nil points.
func align(s []series, start, end time.Time, step time.Duration) []Point {
	byTime := map[int64]float64{}
	if len(s) > 0 {
		for _, p := range s[0].Points {
			byTime[p.Time.Unix()] = p.Value
		}
	}
	var out []Point
	for t := start; !t.After(end); t = t.Add(step) {
		p := Point{Time: t.UTC()}
		if v, ok := byTime[t.Unix()]; ok && !math.IsNaN(v) && !math.IsInf(v, 0) {
			p.Value = &v
		}
		out = append(out, p)
	}
	return out
}

// value returns a series' single value, or nil for NaN/Inf ("no data").
func value(s series) *float64 {
	if len(s.Points) == 0 {
		return nil
	}
	v := s.Points[0].Value
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return nil
	}
	return &v
}

// promDuration formats d as a PromQL duration in whole seconds.
func promDuration(d time.Duration) string {
	return strconv.FormatInt(int64(d.Round(time.Second)/time.Second), 10) + "s"
}
