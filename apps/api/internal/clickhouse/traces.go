package clickhouse

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"infrascope/api/internal/backend"
)

// Trace search sort orders.
const (
	SortStartTime = "start_time"
	SortDuration  = "duration"
)

// TraceQuery filters a trace search. Empty or nil fields do not filter.
type TraceQuery struct {
	From, To    time.Time
	Service     string
	Operation   string
	MinDuration *time.Duration
	MaxDuration *time.Duration
	// Status filters on the root span's status: "ok", "error" or "" (any).
	// The root span records whether the request as a whole failed.
	Status string
	Sort   string // SortStartTime or SortDuration
	Limit  int
	Cursor string
}

// TraceSummary is one trace in search results.
type TraceSummary struct {
	TraceID       string
	RootService   string
	RootOperation string
	StartTime     time.Time
	Duration      time.Duration
	SpanCount     int
	HasError      bool // any span in the trace has status Error
}

// TracePage is one page of search results.
type TracePage struct {
	Traces     []TraceSummary
	NextCursor string // empty on the last page
}

type rootRow struct {
	TraceID    string `json:"trace_id"`
	Service    string `json:"service"`
	Operation  string `json:"operation"`
	StartNS    int64  `json:"start_ns"`
	DurationNS uint64 `json:"duration_ns"`
}

// SearchTraces finds traces whose root span matches q, newest or slowest
// first. Pagination is keyset-based on (sort value, trace ID), so a page
// boundary never skips or repeats a trace.
func (c *Client) SearchTraces(ctx context.Context, q TraceQuery) (TracePage, error) {
	sortBy := SortStartTime
	if q.Sort == SortDuration {
		sortBy = SortDuration
	}
	where := []string{
		"Timestamp >= fromUnixTimestamp64Nano({from:Int64})",
		"Timestamp < fromUnixTimestamp64Nano({to:Int64})",
		"ParentSpanId = ''",
	}
	params := map[string]string{
		"from":  nanos(q.From),
		"to":    nanos(q.To),
		"limit": strconv.Itoa(q.Limit + 1),
	}
	if q.Service != "" {
		where = append(where, "ServiceName = {service:String}")
		params["service"] = q.Service
	}
	if q.Operation != "" {
		where = append(where, "SpanName = {operation:String}")
		params["operation"] = q.Operation
	}
	if q.MinDuration != nil {
		where = append(where, "Duration >= {min_duration:UInt64}")
		params["min_duration"] = strconv.FormatInt(int64(*q.MinDuration), 10)
	}
	if q.MaxDuration != nil {
		where = append(where, "Duration <= {max_duration:UInt64}")
		params["max_duration"] = strconv.FormatInt(int64(*q.MaxDuration), 10)
	}
	switch q.Status {
	case "error":
		where = append(where, "StatusCode = 'Error'")
	case "ok":
		where = append(where, "StatusCode != 'Error'")
	}

	order := "Timestamp DESC, TraceId DESC"
	after := "(Timestamp, TraceId) < (fromUnixTimestamp64Nano({after_value:Int64}), {after_key:String})"
	if sortBy == SortDuration {
		order = "Duration DESC, TraceId DESC"
		after = "(Duration, TraceId) < ({after_value:UInt64}, {after_key:String})"
	}
	if q.Cursor != "" {
		cur, err := decodeCursor(q.Cursor, "traces", sortBy)
		if err != nil {
			return TracePage{}, err
		}
		where = append(where, after)
		params["after_value"] = strconv.FormatInt(cur.Value, 10)
		params["after_key"] = cur.Key
	}

	sql := `SELECT TraceId AS trace_id, ServiceName AS service, SpanName AS operation,
       toUnixTimestamp64Nano(Timestamp) AS start_ns, Duration AS duration_ns
FROM otel.otel_traces
WHERE ` + strings.Join(where, "\n  AND ") + `
ORDER BY ` + order + `
LIMIT {limit:UInt32}`

	var roots []rootRow
	err := c.query(ctx, sql, params, func(row json.RawMessage) error {
		var r rootRow
		if err := json.Unmarshal(row, &r); err != nil {
			return fmt.Errorf("clickhouse: decode trace: %w", err)
		}
		roots = append(roots, r)
		return nil
	})
	if err != nil {
		return TracePage{}, err
	}

	var page TracePage
	if len(roots) > q.Limit {
		roots = roots[:q.Limit]
		last := roots[len(roots)-1]
		value := last.StartNS
		if sortBy == SortDuration {
			value = int64(last.DurationNS)
		}
		page.NextCursor = cursor{Kind: "traces", Sort: sortBy, Value: value, Key: last.TraceID}.encode()
	}

	counts, err := c.spanCounts(ctx, roots)
	if err != nil {
		return TracePage{}, err
	}
	seen := make(map[string]bool, len(roots))
	for _, r := range roots {
		// A trace can, rarely, have two root spans; report it once.
		if seen[r.TraceID] {
			continue
		}
		seen[r.TraceID] = true
		count := counts[r.TraceID]
		page.Traces = append(page.Traces, TraceSummary{
			TraceID:       r.TraceID,
			RootService:   r.Service,
			RootOperation: r.Operation,
			StartTime:     time.Unix(0, r.StartNS).UTC(),
			Duration:      time.Duration(r.DurationNS),
			SpanCount:     max(int(count.Spans), 1),
			HasError:      count.HasError,
		})
	}
	return page, nil
}

type spanCount struct {
	TraceID  string `json:"trace_id"`
	Spans    uint64 `json:"spans"`
	HasError bool   `json:"has_error"`
}

// spanCounts returns span totals and error flags for the traces of a page,
// in one query bounded to the page's time span.
func (c *Client) spanCounts(ctx context.Context, roots []rootRow) (map[string]spanCount, error) {
	out := make(map[string]spanCount, len(roots))
	if len(roots) == 0 {
		return out, nil
	}
	ids := make([]string, 0, len(roots))
	lo, hi := roots[0].StartNS, roots[0].StartNS
	for _, r := range roots {
		ids = append(ids, r.TraceID)
		lo = min(lo, r.StartNS)
		hi = max(hi, r.StartNS+int64(r.DurationNS))
	}
	// Child spans start within their root's lifetime; the margin absorbs
	// clock skew between services.
	const skew = int64(time.Minute)
	err := c.query(ctx, `SELECT TraceId AS trace_id, count() AS spans,
       toBool(countIf(StatusCode = 'Error') > 0) AS has_error
FROM otel.otel_traces
WHERE TraceId IN {ids:Array(String)}
  AND Timestamp >= fromUnixTimestamp64Nano({lo:Int64})
  AND Timestamp <= fromUnixTimestamp64Nano({hi:Int64})
GROUP BY TraceId`,
		map[string]string{
			"ids": stringArray(ids),
			"lo":  strconv.FormatInt(lo-skew, 10),
			"hi":  strconv.FormatInt(hi+skew, 10),
		},
		func(row json.RawMessage) error {
			var sc spanCount
			if err := json.Unmarshal(row, &sc); err != nil {
				return fmt.Errorf("clickhouse: decode span count: %w", err)
			}
			out[sc.TraceID] = sc
			return nil
		})
	return out, err
}

// Span is one operation within a trace.
type Span struct {
	SpanID             string
	ParentSpanID       string // empty for a root span
	Service            string
	Name               string
	Kind               string // server, client, producer, consumer, internal or unspecified
	StartTime          time.Time
	Duration           time.Duration
	Status             string // unset, ok or error
	StatusMessage      string
	Attributes         map[string]string
	ResourceAttributes map[string]string
	Events             []SpanEvent
}

// SpanEvent is a timestamped annotation on a span, such as a recorded exception.
type SpanEvent struct {
	Time       time.Time
	Name       string
	Attributes map[string]string
}

// Trace is a trace's spans, earliest first.
type Trace struct {
	TraceID   string
	Start     time.Time
	End       time.Time
	Spans     []Span
	Truncated bool // the trace has more than the requested number of spans
}

type spanRow struct {
	SpanID             string              `json:"span_id"`
	ParentSpanID       string              `json:"parent_span_id"`
	Service            string              `json:"service"`
	Name               string              `json:"name"`
	Kind               string              `json:"kind"`
	StartNS            int64               `json:"start_ns"`
	DurationNS         uint64              `json:"duration_ns"`
	Status             string              `json:"status"`
	StatusMessage      string              `json:"status_message"`
	Attributes         map[string]string   `json:"attributes"`
	ResourceAttributes map[string]string   `json:"resource_attributes"`
	EventNS            []int64             `json:"event_ns"`
	EventNames         []string            `json:"event_names"`
	EventAttributes    []map[string]string `json:"event_attributes"`
}

// traceBoundsSQL reads a trace's time range from the lookup table, which is
// ordered by TraceId and therefore cheap to search.
const traceBoundsSQL = `SELECT count() AS n,
       toInt64(toUnixTimestamp(min(Start))) AS start_s,
       toInt64(toUnixTimestamp(max(End))) AS end_s
FROM otel.otel_traces_trace_id_ts
WHERE TraceId = {trace_id:String}`

// traceSpansSQL reads a trace's spans within its time range, so partition
// pruning and the TraceId bloom filter limit what is read.
const traceSpansSQL = `SELECT SpanId AS span_id, ParentSpanId AS parent_span_id,
       ServiceName AS service, SpanName AS name, SpanKind AS kind,
       toUnixTimestamp64Nano(Timestamp) AS start_ns, Duration AS duration_ns,
       StatusCode AS status, StatusMessage AS status_message,
       SpanAttributes AS attributes, ResourceAttributes AS resource_attributes,
       arrayMap(t -> toUnixTimestamp64Nano(t), Events.Timestamp) AS event_ns,
       Events.Name AS event_names, Events.Attributes AS event_attributes
FROM otel.otel_traces
WHERE TraceId = {trace_id:String}
  AND Timestamp >= fromUnixTimestamp64Nano({start:Int64})
  AND Timestamp < fromUnixTimestamp64Nano({end:Int64})
ORDER BY Timestamp, SpanId
LIMIT {limit:UInt32}`

// GetTrace returns up to maxSpans spans of a trace, earliest first. It reads
// the trace's time bounds from the lookup table first, so the span query
// prunes partitions instead of scanning the whole retention window
// (SPEC AC-3.4). It returns backend.ErrNotFound for unknown or expired traces.
func (c *Client) GetTrace(ctx context.Context, traceID string, maxSpans int) (Trace, error) {
	var bounds struct {
		N     uint64 `json:"n"`
		Start int64  `json:"start_s"`
		End   int64  `json:"end_s"`
	}
	err := c.query(ctx, traceBoundsSQL, map[string]string{"trace_id": traceID},
		func(row json.RawMessage) error { return json.Unmarshal(row, &bounds) })
	if err != nil {
		return Trace{}, err
	}
	if bounds.N == 0 {
		return Trace{}, fmt.Errorf("%w: trace %s", backend.ErrNotFound, traceID)
	}

	trace := Trace{TraceID: traceID}
	err = c.query(ctx, traceSpansSQL, spanParams(traceID, bounds.Start, bounds.End, maxSpans),
		func(row json.RawMessage) error {
			var r spanRow
			if err := json.Unmarshal(row, &r); err != nil {
				return fmt.Errorf("clickhouse: decode span: %w", err)
			}
			trace.Spans = append(trace.Spans, r.span())
			return nil
		})
	if err != nil {
		return Trace{}, err
	}
	if len(trace.Spans) == 0 {
		return Trace{}, fmt.Errorf("%w: trace %s", backend.ErrNotFound, traceID)
	}
	if len(trace.Spans) > maxSpans {
		trace.Spans = trace.Spans[:maxSpans]
		trace.Truncated = true
	}
	trace.Start = trace.Spans[0].StartTime
	for _, s := range trace.Spans {
		trace.Start = minTime(trace.Start, s.StartTime)
		trace.End = maxTime(trace.End, s.StartTime.Add(s.Duration))
	}
	return trace, nil
}

// spanParams binds traceSpansSQL. The lookup table stores span start times
// truncated to whole seconds, hence the extra second at the end.
func spanParams(traceID string, startS, endS int64, maxSpans int) map[string]string {
	return map[string]string{
		"trace_id": traceID,
		"start":    strconv.FormatInt(startS*int64(time.Second), 10),
		"end":      strconv.FormatInt((endS+1)*int64(time.Second), 10),
		"limit":    strconv.Itoa(maxSpans + 1),
	}
}

func (r spanRow) span() Span {
	s := Span{
		SpanID:             r.SpanID,
		ParentSpanID:       r.ParentSpanID,
		Service:            r.Service,
		Name:               r.Name,
		Kind:               normalizeKind(r.Kind),
		StartTime:          time.Unix(0, r.StartNS).UTC(),
		Duration:           time.Duration(r.DurationNS),
		Status:             normalizeStatus(r.Status),
		StatusMessage:      r.StatusMessage,
		Attributes:         r.Attributes,
		ResourceAttributes: r.ResourceAttributes,
	}
	for i, ns := range r.EventNS {
		e := SpanEvent{Time: time.Unix(0, ns).UTC()}
		if i < len(r.EventNames) {
			e.Name = r.EventNames[i]
		}
		if i < len(r.EventAttributes) {
			e.Attributes = r.EventAttributes[i]
		}
		s.Events = append(s.Events, e)
	}
	return s
}

// normalizeKind maps the exporter's span kinds ("Server", "Client", ...) to
// the API's lowercase values.
func normalizeKind(kind string) string {
	switch k := strings.ToLower(kind); k {
	case "server", "client", "producer", "consumer", "internal":
		return k
	}
	return "unspecified"
}

// normalizeStatus maps the exporter's status codes ("Unset", "Ok", "Error").
func normalizeStatus(status string) string {
	switch s := strings.ToLower(status); s {
	case "ok", "error":
		return s
	}
	return "unset"
}

func minTime(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

func maxTime(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}
