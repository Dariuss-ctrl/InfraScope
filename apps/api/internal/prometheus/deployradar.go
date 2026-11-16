package prometheus

import (
	"context"
	"errors"
	"fmt"
	"time"

	"infrascope/api/internal/deployradar"
)

// WindowStats implements deployradar.Metrics: the request count, 5xx count
// and p95 latency of one service in one environment over w. Each is an
// instant query at the window's end with increase() over its length, so the
// stored query, time and range reproduce the value exactly.
//
// p95 leaves out 5xx responses. Failures are often fast (an error returned
// before the real work), so including them pulls p95 down exactly when a
// release breaks, and hides a slowdown behind the errors.
func (c *Client) WindowStats(ctx context.Context, service, environment string, w deployradar.Window) (deployradar.WindowStats, error) {
	if !w.To.After(w.From) {
		return deployradar.WindowStats{}, errors.New("prometheus: comparison window must end after it starts")
	}
	rng := promDuration(max(w.To.Sub(w.From), time.Second))
	selector := matchers("service_name", service, "deployment_environment_name", environment)
	queries := map[string]string{
		"requests": fmt.Sprintf("sum(increase(%s_count%s[%s]))", durationMetric, selector, rng),
		"errors": fmt.Sprintf("sum(increase(%s_count%s[%s]))", durationMetric,
			addMatcher(selector, `http_response_status_code=~"5.."`), rng),
		"p95": fmt.Sprintf("histogram_quantile(0.95, sum by (le) (increase(%s_bucket%s[%s])))", durationMetric,
			addMatcher(selector, `http_response_status_code!~"5.."`), rng),
	}
	results, err := all(queries, func(q string) ([]series, error) { return c.instant(ctx, q, w.To) })
	if err != nil {
		return deployradar.WindowStats{}, err
	}

	measure := func(key string) deployradar.Measurement {
		m := deployradar.Measurement{Query: deployradar.Query{PromQL: queries[key], Time: w.To.UTC(), Range: rng}}
		if s := results[key]; len(s) > 0 {
			m.Value = value(s[0])
		}
		return m
	}
	stats := deployradar.WindowStats{Requests: measure("requests"), Errors: measure("errors"), P95: measure("p95")}
	// A service that never answered 5xx has no such series at all. With
	// traffic in the window that means zero errors, not unknown.
	if stats.Errors.Value == nil && stats.Requests.Value != nil {
		zero := 0.0
		stats.Errors.Value = &zero
	}
	return stats, nil
}
