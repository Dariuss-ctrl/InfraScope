package clickhouse

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// severityFloors maps the API's severity names to the lowest OpenTelemetry
// SeverityNumber of each range (TRACE 1-4, DEBUG 5-8, INFO 9-12, WARN 13-16,
// ERROR 17-20, FATAL 21-24), so "warn" and "WARNING" filter the same way.
var severityFloors = map[string]int{
	"trace": 1, "debug": 5, "info": 9, "warn": 13, "error": 17, "fatal": 21,
}

// SeverityFloor returns the lowest SeverityNumber of a severity name.
func SeverityFloor(name string) (int, bool) {
	n, ok := severityFloors[name]
	return n, ok
}

// Tokens splits free text into the lowercase ASCII words that ClickHouse's
// token search matches; punctuation and spaces separate words.
func Tokens(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	})
}

// LogQuery filters a log search. Empty fields do not filter.
type LogQuery struct {
	From, To    time.Time
	Service     string
	MinSeverity int      // 0 means any severity
	Tokens      []string // every token must appear in the body, case-insensitively
	TraceID     string
	SpanID      string
	Limit       int
	Cursor      string
}

// LogRecord is one log entry.
type LogRecord struct {
	Timestamp      time.Time
	Service        string
	SeverityNumber int
	SeverityText   string
	Body           string
	TraceID        string
	SpanID         string
	Attributes     map[string]string
}

// LogPage is one page of search results.
type LogPage struct {
	Logs       []LogRecord
	NextCursor string // empty on the last page
}

type logRow struct {
	TimestampNS    int64             `json:"ts_ns"`
	Service        string            `json:"service"`
	SeverityNumber int               `json:"severity_number"`
	SeverityText   string            `json:"severity_text"`
	Body           string            `json:"body"`
	TraceID        string            `json:"trace_id"`
	SpanID         string            `json:"span_id"`
	Attributes     map[string]string `json:"attributes"`
	RowKey         uint64            `json:"row_key"`
}

// SearchLogs returns the log records matching q, newest first. Log records
// have no ID, so pages are keyed on (timestamp, hash of the record's
// identifying fields): records that share a timestamp still page stably.
func (c *Client) SearchLogs(ctx context.Context, q LogQuery) (LogPage, error) {
	where := []string{
		"Timestamp >= fromUnixTimestamp64Nano({from:Int64})",
		"Timestamp < fromUnixTimestamp64Nano({to:Int64})",
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
	if q.MinSeverity > 0 {
		where = append(where, "SeverityNumber >= {min_severity:UInt8}")
		params["min_severity"] = strconv.Itoa(q.MinSeverity)
	}
	if q.TraceID != "" {
		where = append(where, "TraceId = {trace_id:String}")
		params["trace_id"] = q.TraceID
	}
	if q.SpanID != "" {
		where = append(where, "SpanId = {span_id:String}")
		params["span_id"] = q.SpanID
	}
	for i, token := range q.Tokens {
		name := "token" + strconv.Itoa(i)
		// Matches the token index on lower(Body).
		where = append(where, "hasToken(lower(Body), {"+name+":String})")
		params[name] = token
	}
	if q.Cursor != "" {
		cur, err := decodeCursor(q.Cursor, "logs", "timestamp")
		if err != nil {
			return LogPage{}, err
		}
		if _, err := strconv.ParseUint(cur.Key, 10, 64); err != nil {
			return LogPage{}, ErrInvalidCursor
		}
		where = append(where, "(Timestamp, row_key) < (fromUnixTimestamp64Nano({after_value:Int64}), {after_key:UInt64})")
		params["after_value"] = strconv.FormatInt(cur.Value, 10)
		params["after_key"] = cur.Key
	}

	sql := `SELECT toUnixTimestamp64Nano(Timestamp) AS ts_ns, ServiceName AS service,
       SeverityNumber AS severity_number, SeverityText AS severity_text, Body AS body,
       TraceId AS trace_id, SpanId AS span_id, LogAttributes AS attributes,
       sipHash64(TraceId, SpanId, ServiceName, Body) AS row_key
FROM otel.otel_logs
WHERE ` + strings.Join(where, "\n  AND ") + `
ORDER BY Timestamp DESC, row_key DESC
LIMIT {limit:UInt32}`

	var rows []logRow
	err := c.query(ctx, sql, params, func(raw json.RawMessage) error {
		var r logRow
		if err := json.Unmarshal(raw, &r); err != nil {
			return fmt.Errorf("clickhouse: decode log record: %w", err)
		}
		rows = append(rows, r)
		return nil
	})
	if err != nil {
		return LogPage{}, err
	}

	var page LogPage
	if len(rows) > q.Limit {
		rows = rows[:q.Limit]
		last := rows[len(rows)-1]
		page.NextCursor = cursor{
			Kind: "logs", Sort: "timestamp",
			Value: last.TimestampNS, Key: strconv.FormatUint(last.RowKey, 10),
		}.encode()
	}
	for _, r := range rows {
		page.Logs = append(page.Logs, LogRecord{
			Timestamp:      time.Unix(0, r.TimestampNS).UTC(),
			Service:        r.Service,
			SeverityNumber: r.SeverityNumber,
			SeverityText:   r.SeverityText,
			Body:           r.Body,
			TraceID:        r.TraceID,
			SpanID:         r.SpanID,
			Attributes:     r.Attributes,
		})
	}
	return page, nil
}
