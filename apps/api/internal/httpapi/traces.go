package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"infrascope/api/internal/backend"
	"infrascope/api/internal/clickhouse"
)

// maxDurationMs bounds duration filters to a day, far beyond any real request.
const maxDurationMs = 24 * 60 * 60 * 1000

func (a *api) searchTraces(w http.ResponseWriter, r *http.Request) {
	q := parseQuery(r, "service", "operation", "min_duration_ms", "max_duration_ms",
		"status", "sort", "from", "to", "limit", "cursor")
	from, to := q.timeRange(a.now(), a.settings.MaxSearchRange)
	tq := clickhouse.TraceQuery{
		From:      from,
		To:        to,
		Service:   q.text("service", maxTextLength),
		Operation: q.text("operation", maxTextLength),
		Status:    q.enum("status", "any", "any", "ok", "error"),
		Sort:      q.enum("sort", clickhouse.SortStartTime, clickhouse.SortStartTime, clickhouse.SortDuration),
		Limit:     q.integer("limit", defaultLimit, 1, maxLimit),
		Cursor:    q.cursor(),
	}
	if tq.Status == "any" {
		tq.Status = ""
	}
	minMs, hasMin := q.optionalInt("min_duration_ms", 0, maxDurationMs)
	maxMs, hasMax := q.optionalInt("max_duration_ms", 0, maxDurationMs)
	if hasMin {
		d := time.Duration(minMs) * time.Millisecond
		tq.MinDuration = &d
	}
	if hasMax {
		d := time.Duration(maxMs) * time.Millisecond
		tq.MaxDuration = &d
	}
	if hasMin && hasMax && minMs > maxMs {
		q.fail("min_duration_ms", "must not exceed max_duration_ms")
	}
	if err := q.err(); err != nil {
		a.fail(w, r, err, storage)
		return
	}

	page, err := a.telemetry.SearchTraces(r.Context(), tq)
	if err != nil {
		a.fail(w, r, err, storage)
		return
	}
	out := traceSearchJSON{Traces: make([]traceSummaryJSON, 0, len(page.Traces)), NextCursor: optional(page.NextCursor)}
	for _, t := range page.Traces {
		out.Traces = append(out.Traces, traceSummaryJSON{
			TraceID:       t.TraceID,
			RootService:   t.RootService,
			RootOperation: t.RootOperation,
			StartTime:     timestamp(t.StartTime),
			DurationNS:    int64(t.Duration),
			SpanCount:     t.SpanCount,
			HasError:      t.HasError,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *api) getTrace(w http.ResponseWriter, r *http.Request) {
	q := parseQuery(r)
	traceID := strings.ToLower(r.PathValue("traceId"))
	if !isHex(traceID, 32) {
		q.fail("traceId", "must be 32 hexadecimal characters")
	}
	if err := q.err(); err != nil {
		a.fail(w, r, err, storage)
		return
	}

	t, err := a.telemetry.GetTrace(r.Context(), traceID, a.settings.MaxSpans)
	if errors.Is(err, backend.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found",
			"No trace with this ID. It may not have been exported yet, or it may have expired.")
		return
	}
	if err != nil {
		a.fail(w, r, err, storage)
		return
	}
	out := traceJSON{
		TraceID:   t.TraceID,
		StartTime: timestamp(t.Start),
		EndTime:   timestamp(t.End),
		Spans:     make([]spanJSON, 0, len(t.Spans)),
		Truncated: t.Truncated,
	}
	for _, s := range t.Spans {
		span := spanJSON{
			SpanID:             s.SpanID,
			ParentSpanID:       optional(s.ParentSpanID),
			Service:            s.Service,
			Name:               s.Name,
			Kind:               s.Kind,
			StartTime:          timestamp(s.StartTime),
			DurationNS:         int64(s.Duration),
			Status:             s.Status,
			StatusMessage:      s.StatusMessage,
			Attributes:         nonNil(s.Attributes),
			ResourceAttributes: nonNil(s.ResourceAttributes),
			Events:             make([]eventJSON, 0, len(s.Events)),
		}
		for _, e := range s.Events {
			span.Events = append(span.Events, eventJSON{Time: timestamp(e.Time), Name: e.Name, Attributes: nonNil(e.Attributes)})
		}
		out.Spans = append(out.Spans, span)
	}
	writeJSON(w, http.StatusOK, out)
}

type traceSearchJSON struct {
	Traces     []traceSummaryJSON `json:"traces"`
	NextCursor *string            `json:"next_cursor"`
}

type traceSummaryJSON struct {
	TraceID       string `json:"trace_id"`
	RootService   string `json:"root_service"`
	RootOperation string `json:"root_operation"`
	StartTime     string `json:"start_time"`
	DurationNS    int64  `json:"duration_ns"`
	SpanCount     int    `json:"span_count"`
	HasError      bool   `json:"has_error"`
}

type traceJSON struct {
	TraceID   string     `json:"trace_id"`
	StartTime string     `json:"start_time"`
	EndTime   string     `json:"end_time"`
	Spans     []spanJSON `json:"spans"`
	Truncated bool       `json:"truncated"`
}

type spanJSON struct {
	SpanID             string            `json:"span_id"`
	ParentSpanID       *string           `json:"parent_span_id"`
	Service            string            `json:"service"`
	Name               string            `json:"name"`
	Kind               string            `json:"kind"`
	StartTime          string            `json:"start_time"`
	DurationNS         int64             `json:"duration_ns"`
	Status             string            `json:"status"`
	StatusMessage      string            `json:"status_message"`
	Attributes         map[string]string `json:"attributes"`
	ResourceAttributes map[string]string `json:"resource_attributes"`
	Events             []eventJSON       `json:"events"`
}

type eventJSON struct {
	Time       string            `json:"time"`
	Name       string            `json:"name"`
	Attributes map[string]string `json:"attributes"`
}

func timestamp(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

// optional returns nil for "", so JSON shows null instead of an empty string.
func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func nonNil(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}
