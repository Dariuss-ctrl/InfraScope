package httpapi

import (
	"fmt"
	"net/http"
	"time"

	"infrascope/api/internal/prometheus"
)

// maxPoints caps each metrics series, keeping responses and Prometheus work
// bounded.
const maxPoints = 1000

// niceSteps are the resolutions picked automatically, finest first. 15 s is
// the metric export interval, so finer steps would only repeat values.
var niceSteps = []time.Duration{
	15 * time.Second, 30 * time.Second, time.Minute, 2 * time.Minute, 5 * time.Minute,
	10 * time.Minute, 15 * time.Minute, 30 * time.Minute, time.Hour,
}

func (a *api) listServices(w http.ResponseWriter, r *http.Request) {
	q := parseQuery(r, "from", "to")
	from, to := q.timeRange(a.now(), a.settings.MaxMetricsRange)
	if err := q.err(); err != nil {
		a.fail(w, r, err, metrics)
		return
	}
	services, err := a.metrics.Services(r.Context(), from, to)
	if err != nil {
		a.fail(w, r, err, metrics)
		return
	}
	out := struct {
		Services []serviceJSON `json:"services"`
	}{Services: make([]serviceJSON, 0, len(services))}
	for _, s := range services {
		out.Services = append(out.Services, serviceJSON{
			Name:        s.Name,
			RequestRate: s.RequestRate,
			ErrorRate:   s.ErrorRate,
			Latency:     latencyJSON{P50: s.Latency.P50, P95: s.Latency.P95, P99: s.Latency.P99},
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *api) serviceMetrics(w http.ResponseWriter, r *http.Request) {
	q := parseQuery(r, "from", "to", "step_seconds")
	service := r.PathValue("service")
	if service == "" || !validText(service, maxTextLength) {
		q.fail("service", fmt.Sprintf("must be printable text of at most %d bytes", maxTextLength))
	}
	from, to := q.timeRange(a.now(), a.settings.MaxMetricsRange)
	stepSeconds, hasStep := q.optionalInt("step_seconds", 5, 3600)
	step := autoStep(to.Sub(from))
	if hasStep {
		step = time.Duration(stepSeconds) * time.Second
	}
	if points := int(to.Sub(from)/step) + 1; q.err() == nil && points > maxPoints {
		q.fail("step_seconds", fmt.Sprintf("gives %d points for this range; at most %d are allowed, so use a larger step", points, maxPoints))
	}
	if err := q.err(); err != nil {
		a.fail(w, r, err, metrics)
		return
	}

	exists, err := a.metrics.ServiceExists(r.Context(), service, to, a.settings.MaxMetricsRange)
	if err != nil {
		a.fail(w, r, err, metrics)
		return
	}
	if !exists {
		writeError(w, http.StatusNotFound, "not_found", "No metrics have been recorded for this service.")
		return
	}
	m, err := a.metrics.ServiceMetrics(r.Context(), service, from, to, step)
	if err != nil {
		a.fail(w, r, err, metrics)
		return
	}
	writeJSON(w, http.StatusOK, serviceMetricsJSON{
		Service:     m.Service,
		StepSeconds: int(m.Step / time.Second),
		Series: seriesJSON{
			RequestRate: points(m.RequestRate),
			ErrorRate:   points(m.ErrorRate),
			P50:         points(m.P50),
			P95:         points(m.P95),
			P99:         points(m.P99),
		},
	})
}

// autoStep picks the finest nice step that keeps a range near 240 points.
func autoStep(span time.Duration) time.Duration {
	for _, s := range niceSteps {
		if span/s <= 240 {
			return s
		}
	}
	return niceSteps[len(niceSteps)-1]
}

type serviceJSON struct {
	Name        string      `json:"name"`
	RequestRate *float64    `json:"request_rate"`
	ErrorRate   *float64    `json:"error_rate"`
	Latency     latencyJSON `json:"latency_seconds"`
}

type latencyJSON struct {
	P50 *float64 `json:"p50"`
	P95 *float64 `json:"p95"`
	P99 *float64 `json:"p99"`
}

type serviceMetricsJSON struct {
	Service     string     `json:"service"`
	StepSeconds int        `json:"step_seconds"`
	Series      seriesJSON `json:"series"`
}

type seriesJSON struct {
	RequestRate []pointJSON `json:"request_rate"`
	ErrorRate   []pointJSON `json:"error_rate"`
	P50         []pointJSON `json:"latency_p50_seconds"`
	P95         []pointJSON `json:"latency_p95_seconds"`
	P99         []pointJSON `json:"latency_p99_seconds"`
}

type pointJSON struct {
	T string   `json:"t"`
	V *float64 `json:"v"`
}

func points(ps []prometheus.Point) []pointJSON {
	out := make([]pointJSON, 0, len(ps))
	for _, p := range ps {
		out = append(out, pointJSON{T: timestamp(p.Time), V: p.Value})
	}
	return out
}
