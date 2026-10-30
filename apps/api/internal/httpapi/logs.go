package httpapi

import (
	"net/http"

	"infrascope/api/internal/clickhouse"
)

const (
	maxQueryLength = 200
	maxQueryTokens = 8
)

func (a *api) searchLogs(w http.ResponseWriter, r *http.Request) {
	q := parseQuery(r, "service", "severity", "q", "trace_id", "span_id", "from", "to", "limit", "cursor")
	from, to := q.timeRange(a.now(), a.settings.MaxSearchRange)
	lq := clickhouse.LogQuery{
		From:    from,
		To:      to,
		Service: q.text("service", maxTextLength),
		TraceID: q.hexID("trace_id", 32),
		SpanID:  q.hexID("span_id", 16),
		Limit:   q.integer("limit", defaultLimit, 1, maxLimit),
		Cursor:  q.cursor(),
	}
	if severity := q.enum("severity", "", "trace", "debug", "info", "warn", "error", "fatal"); severity != "" {
		lq.MinSeverity, _ = clickhouse.SeverityFloor(severity)
	}
	if text := q.text("q", maxQueryLength); text != "" {
		lq.Tokens = clickhouse.Tokens(text)
		switch {
		case len(lq.Tokens) == 0:
			q.fail("q", "must contain at least one letter or digit")
		case len(lq.Tokens) > maxQueryTokens:
			q.fail("q", "may contain at most 8 words")
		}
	}
	if err := q.err(); err != nil {
		a.fail(w, r, err, storage)
		return
	}

	page, err := a.telemetry.SearchLogs(r.Context(), lq)
	if err != nil {
		a.fail(w, r, err, storage)
		return
	}
	out := logSearchJSON{Logs: make([]logJSON, 0, len(page.Logs)), NextCursor: optional(page.NextCursor)}
	for _, l := range page.Logs {
		out.Logs = append(out.Logs, logJSON{
			Timestamp:      timestamp(l.Timestamp),
			Service:        l.Service,
			SeverityNumber: l.SeverityNumber,
			SeverityText:   l.SeverityText,
			Body:           l.Body,
			TraceID:        hexOrNil(l.TraceID, 32),
			SpanID:         hexOrNil(l.SpanID, 16),
			Attributes:     nonNil(l.Attributes),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

type logSearchJSON struct {
	Logs       []logJSON `json:"logs"`
	NextCursor *string   `json:"next_cursor"`
}

type logJSON struct {
	Timestamp      string            `json:"timestamp"`
	Service        string            `json:"service"`
	SeverityNumber int               `json:"severity_number"`
	SeverityText   string            `json:"severity_text"`
	Body           string            `json:"body"`
	TraceID        *string           `json:"trace_id"`
	SpanID         *string           `json:"span_id"`
	Attributes     map[string]string `json:"attributes"`
}

// hexOrNil returns id when it is a well-formed W3C identifier, else nil: a
// record logged outside any span has no trace to link to.
func hexOrNil(id string, length int) *string {
	if !isHex(id, length) {
		return nil
	}
	return &id
}
