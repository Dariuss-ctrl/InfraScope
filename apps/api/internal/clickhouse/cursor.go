package clickhouse

import (
	"encoding/base64"
	"encoding/json"
	"errors"
)

// ErrInvalidCursor is returned for a cursor that was not produced by this
// API for the same kind of search and sort order.
var ErrInvalidCursor = errors.New("invalid cursor")

// cursor is the keyset position after the last row of a page: the sort value
// plus a tie-breaker, so pages never skip or repeat rows that share a value.
type cursor struct {
	Kind  string `json:"k"` // "traces" or "logs"
	Sort  string `json:"s"` // the sort order the cursor belongs to
	Value int64  `json:"v"` // timestamp (ns) or duration (ns)
	Key   string `json:"t"` // tie-breaker: trace ID or log row key
}

func (c cursor) encode() string {
	raw, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeCursor(s, kind, sort string) (cursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return cursor{}, ErrInvalidCursor
	}
	var c cursor
	if err := json.Unmarshal(raw, &c); err != nil || c.Kind != kind || c.Sort != sort || c.Key == "" || c.Value < 0 {
		return cursor{}, ErrInvalidCursor
	}
	return c, nil
}
