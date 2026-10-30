//go:build integration

package testenv

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"
)

// Telemetry describes the standard fixture set loaded by SeedTelemetry.
type Telemetry struct {
	Base         time.Time // start of the fixture window; everything lies within Base+1h
	OrdersRead   string    // checkout GET /orders: a server span and a 100 ms SELECT, three logs
	OrdersFailed string    // checkout POST /orders: failed, with an exception event and an ERROR log
	Products     string    // catalog GET /products: one span and no logs
	Wide         string    // a 30-span trace, for truncation
	BulkTraces   int       // root spans of service "bulk", three per millisecond
	BulkLogs     int       // logs of service "bulk-logs", ten per millisecond
}

// SeedTelemetry loads the standard traces and logs, starting at base.
func SeedTelemetry(ctx context.Context, ch *ClickHouse, base time.Time) (Telemetry, error) {
	f := Telemetry{
		Base: base, OrdersRead: TraceID(1), OrdersFailed: TraceID(2), Products: TraceID(3), Wide: TraceID(4),
		BulkTraces: 1200, BulkLogs: 1050,
	}
	ms := time.Millisecond
	spans := []Span{
		{Time: base.Add(time.Minute), TraceID: f.OrdersRead, SpanID: SpanID(1), Service: "checkout", Name: "GET /orders",
			Kind: "Server", Duration: 120 * ms, Attributes: map[string]string{"http.request.method": "GET"}},
		{Time: base.Add(time.Minute + 5*ms), TraceID: f.OrdersRead, SpanID: SpanID(2), ParentID: SpanID(1), Service: "checkout",
			Name: "SELECT", Kind: "Client", Duration: 100 * ms,
			Attributes: map[string]string{"db.system.name": "postgresql", "db.query.text": "SELECT * FROM orders WHERE customer_email = $1"}},
		{Time: base.Add(2 * time.Minute), TraceID: f.OrdersFailed, SpanID: SpanID(3), Service: "checkout", Name: "POST /orders",
			Kind: "Server", Duration: 300 * ms, Status: "Error"},
		{Time: base.Add(2*time.Minute + 10*ms), TraceID: f.OrdersFailed, SpanID: SpanID(4), ParentID: SpanID(3), Service: "checkout",
			Name: "INSERT", Kind: "Client", Duration: 250 * ms, Status: "Error",
			Attributes: map[string]string{"db.system.name": "postgresql"},
			Events: []Event{{Time: base.Add(2*time.Minute + 260*ms), Name: "exception",
				Attributes: map[string]string{"exception.type": "pgconn.PgError", "exception.message": "deadlock detected"}}}},
		{Time: base.Add(3 * time.Minute), TraceID: f.Products, SpanID: SpanID(5), Service: "catalog", Name: "GET /products",
			Kind: "Server", Duration: 5 * ms},
		{Time: base.Add(4 * time.Minute), TraceID: f.Wide, SpanID: SpanID(6), Service: "wide", Name: "GET /export",
			Kind: "Server", Duration: time.Second},
	}
	for i := range 29 {
		spans = append(spans, Span{Time: base.Add(4*time.Minute + time.Duration(i+1)*10*ms), TraceID: f.Wide,
			SpanID: SpanID(100 + i), ParentID: SpanID(6), Service: "wide", Name: "chunk", Kind: "Internal", Duration: 10 * ms})
	}
	for i := range f.BulkTraces {
		spans = append(spans, Span{Time: base.Add(10*time.Minute + time.Duration(i/3)*ms), TraceID: TraceID(1000 + i),
			SpanID: SpanID(10_000 + i), Service: "bulk", Name: "GET /bulk", Kind: "Server",
			Duration: time.Duration(i%400+1) * ms})
	}

	logs := []Log{
		{Time: base.Add(time.Minute + 120*ms), TraceID: f.OrdersRead, SpanID: SpanID(1), Service: "checkout",
			SeverityText: "INFO", SeverityNumber: 9, Body: "request handled"},
		{Time: base.Add(time.Minute + 105*ms), TraceID: f.OrdersRead, SpanID: SpanID(2), Service: "checkout",
			SeverityText: "WARN", SeverityNumber: 13, Body: "slow query detected", Attributes: map[string]string{"duration_ms": "100"}},
		{Time: base.Add(time.Minute + 105*ms), TraceID: f.OrdersRead, SpanID: SpanID(2), Service: "checkout",
			SeverityText: "INFO", SeverityNumber: 9, Body: "cache miss"},
		{Time: base.Add(2*time.Minute + 260*ms), TraceID: f.OrdersFailed, SpanID: SpanID(4), Service: "checkout",
			SeverityText: "ERROR", SeverityNumber: 17, Body: "order failed: deadlock detected"},
		{Time: base.Add(30 * time.Second), Service: "cron", SeverityText: "INFO", SeverityNumber: 9, Body: "nightly job finished"},
	}
	for i := range f.BulkLogs {
		logs = append(logs, Log{Time: base.Add(20*time.Minute + time.Duration(i/10)*ms), Service: "bulk-logs",
			SeverityText: "INFO", SeverityNumber: 9, Body: fmt.Sprintf("event %d", i)})
	}

	spanRows := make([]map[string]any, len(spans))
	for i, s := range spans {
		spanRows[i] = s.Row()
	}
	if err := ch.Insert(ctx, "otel_traces", spanRows); err != nil {
		return f, fmt.Errorf("insert spans: %w", err)
	}
	logRows := make([]map[string]any, len(logs))
	for i, l := range logs {
		logRows[i] = l.Row()
	}
	if err := ch.Insert(ctx, "otel_logs", logRows); err != nil {
		return f, fmt.Errorf("insert logs: %w", err)
	}
	return f, nil
}

// TraceID and SpanID return deterministic, valid W3C identifiers.
func TraceID(n int) string { return fmt.Sprintf("%032x", n) }

// SpanID returns a deterministic 16-character span identifier.
func SpanID(n int) string { return fmt.Sprintf("%016x", n) }

// Span is a row for otel.otel_traces.
type Span struct {
	Time       time.Time
	TraceID    string
	SpanID     string
	ParentID   string
	Service    string
	Name       string
	Kind       string // as the exporter stores it: "Server", "Client", ...
	Duration   time.Duration
	Status     string // "Unset", "Ok" or "Error"
	Attributes map[string]string
	Events     []Event
}

// Event is a span event.
type Event struct {
	Time       time.Time
	Name       string
	Attributes map[string]string
}

// Row converts the span to the exporter's column layout.
func (s Span) Row() map[string]any {
	evTimes, evNames, evAttrs := []string{}, []string{}, []map[string]string{}
	for _, e := range s.Events {
		evTimes = append(evTimes, chTime(e.Time))
		evNames = append(evNames, e.Name)
		evAttrs = append(evAttrs, orEmpty(e.Attributes))
	}
	kind, status := s.Kind, s.Status
	if kind == "" {
		kind = "Internal"
	}
	if status == "" {
		status = "Unset"
	}
	return map[string]any{
		"Timestamp":          chTime(s.Time),
		"TraceId":            s.TraceID,
		"SpanId":             s.SpanID,
		"ParentSpanId":       s.ParentID,
		"SpanName":           s.Name,
		"SpanKind":           kind,
		"ServiceName":        s.Service,
		"ResourceAttributes": map[string]string{"service.name": s.Service},
		"ScopeName":          "infrascope-tests",
		"SpanAttributes":     orEmpty(s.Attributes),
		"Duration":           s.Duration.Nanoseconds(),
		"StatusCode":         status,
		"Events.Timestamp":   evTimes,
		"Events.Name":        evNames,
		"Events.Attributes":  evAttrs,
	}
}

// Log is a row for otel.otel_logs.
type Log struct {
	Time           time.Time
	TraceID        string
	SpanID         string
	Service        string
	SeverityText   string
	SeverityNumber int
	Body           string
	Attributes     map[string]string
}

// Row converts the log record to the exporter's column layout.
func (l Log) Row() map[string]any {
	return map[string]any{
		"Timestamp":          chTime(l.Time),
		"TraceId":            l.TraceID,
		"SpanId":             l.SpanID,
		"SeverityText":       l.SeverityText,
		"SeverityNumber":     l.SeverityNumber,
		"ServiceName":        l.Service,
		"Body":               l.Body,
		"ResourceAttributes": map[string]string{"service.name": l.Service},
		"LogAttributes":      orEmpty(l.Attributes),
		"ScopeName":          "infrascope-tests",
	}
}

func chTime(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000000000Z")
}

func orEmpty(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

// bucketBounds are OpenTelemetry's default http.server.request.duration
// boundaries, shared by every series as in production.
var bucketBounds = []float64{0.005, 0.01, 0.025, 0.05, 0.075, 0.1, 0.25, 0.5, 0.75, 1, 2.5, 5, 7.5, 10}

// RequestSeries describes one labelled request stream for a histogram
// fixture: a constant request rate whose latencies fall into fixed buckets.
type RequestSeries struct {
	Service string
	Route   string
	Method  string
	Status  int
	Rate    float64 // requests per second
	// Shares maps a bucket's upper bound (one of bucketBounds) to the share
	// of requests that fall into it; shares sum to 1.
	Shares map[float64]float64
}

// HistogramOpenMetrics renders the http.server.request.duration histogram
// for every series from start to end every step, as an OpenMetrics
// exposition with timestamps, ready for promtool backfilling.
func HistogramOpenMetrics(series []RequestSeries, start, end time.Time, step time.Duration) []byte {
	const name = "http_server_request_duration_seconds"
	var b strings.Builder
	b.WriteString("# TYPE " + name + " histogram\n")
	for _, s := range series {
		labels := fmt.Sprintf(`service_name=%q,http_route=%q,http_request_method=%q,http_response_status_code="%d"`,
			s.Service, s.Route, s.Method, s.Status)
		for t := start; !t.After(end); t = t.Add(step) {
			total := math.Round(s.Rate * t.Sub(start).Seconds())
			ts := fmt.Sprintf("%.3f", float64(t.UnixMilli())/1000)
			cumulative, sum := 0.0, 0.0
			for _, le := range bucketBounds {
				cumulative += s.Shares[le]
				sum += s.Shares[le] * le * total
				fmt.Fprintf(&b, "%s_bucket{%s,le=\"%g\"} %g %s\n", name, labels, le, math.Round(cumulative*total), ts)
			}
			fmt.Fprintf(&b, "%s_bucket{%s,le=\"+Inf\"} %g %s\n", name, labels, total, ts)
			fmt.Fprintf(&b, "%s_count{%s} %g %s\n", name, labels, total, ts)
			fmt.Fprintf(&b, "%s_sum{%s} %g %s\n", name, labels, sum, ts)
		}
	}
	b.WriteString("# EOF\n")
	return []byte(b.String())
}
