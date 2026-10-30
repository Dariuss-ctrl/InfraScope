package httpapi

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	defaultLimit  = 50
	maxLimit      = 500
	maxTextLength = 256
	defaultRange  = time.Hour
	// clockSkew is how far in the future "to" may be, to tolerate clients
	// whose clocks run slightly ahead.
	clockSkew = 5 * time.Minute
)

// query reads and validates URL query parameters. It collects every problem
// instead of stopping at the first, so a client can fix them all at once.
type query struct {
	values url.Values
	errs   []FieldError
}

// parseQuery rejects parameters other than allowed, and parameters given
// more than once: a typo such as "servcie" should fail loudly, not be ignored.
func parseQuery(r *http.Request, allowed ...string) *query {
	q := &query{values: r.URL.Query()}
	for name, values := range q.values {
		switch {
		case !slices.Contains(allowed, name):
			q.fail(name, "is not a parameter of this endpoint")
		case len(values) > 1:
			q.fail(name, "must be given only once")
		}
	}
	return q
}

func (q *query) fail(field, message string) {
	q.errs = append(q.errs, FieldError{Field: field, Message: message})
}

func (q *query) err() error {
	if len(q.errs) == 0 {
		return nil
	}
	return invalid(q.errs...)
}

// text returns a free-text parameter, which must be printable and at most
// maxLen bytes long.
func (q *query) text(name string, maxLen int) string {
	v := q.values.Get(name)
	if !validText(v, maxLen) {
		q.fail(name, fmt.Sprintf("must be printable text of at most %d bytes", maxLen))
		return ""
	}
	return v
}

// enum returns a parameter that must be one of options, or def when absent.
func (q *query) enum(name, def string, options ...string) string {
	v := q.values.Get(name)
	if v == "" {
		return def
	}
	if !slices.Contains(options, v) {
		q.fail(name, "must be one of: "+strings.Join(options, ", "))
		return def
	}
	return v
}

// integer returns an integer parameter in [lo, hi], or def when absent.
func (q *query) integer(name string, def, lo, hi int) int {
	n, ok := q.optionalInt(name, lo, hi)
	if !ok {
		return def
	}
	return n
}

// optionalInt returns an integer parameter in [lo, hi] and whether it was
// given (and valid).
func (q *query) optionalInt(name string, lo, hi int) (int, bool) {
	raw := q.values.Get(name)
	if raw == "" {
		return 0, false
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < lo || n > hi {
		q.fail(name, fmt.Sprintf("must be an integer from %d to %d", lo, hi))
		return 0, false
	}
	return n, true
}

// hexID returns a lowercase hexadecimal identifier of exactly length
// characters, or "" when absent.
func (q *query) hexID(name string, length int) string {
	v := strings.ToLower(q.values.Get(name))
	if v != "" && !isHex(v, length) {
		q.fail(name, fmt.Sprintf("must be %d hexadecimal characters", length))
		return ""
	}
	return v
}

// cursor returns the opaque pagination cursor, or "" when absent. Its
// contents are validated by the store that issued it.
func (q *query) cursor() string {
	v := q.values.Get("cursor")
	if len(v) > 512 {
		q.fail("cursor", "is not a cursor returned by this search")
		return ""
	}
	return v
}

// timeRange returns the [from, to) window: "to" defaults to now and "from" to
// one hour before "to". The window must be ordered, at most maxRange long,
// and not in the future.
func (q *query) timeRange(now time.Time, maxRange time.Duration) (time.Time, time.Time) {
	to, toOK := q.timestamp("to", now)
	from, fromOK := q.timestamp("from", to.Add(-defaultRange))
	if !toOK || !fromOK {
		return from, to
	}
	switch {
	case to.After(now.Add(clockSkew)):
		q.fail("to", "is in the future")
	case !from.Before(to):
		q.fail("from", "must be before to")
	case to.Sub(from) > maxRange:
		q.fail("from", "is too far before to: the range may span at most "+humanDuration(maxRange))
	}
	return from, to
}

func (q *query) timestamp(name string, def time.Time) (time.Time, bool) {
	raw := q.values.Get(name)
	if raw == "" {
		return def, true
	}
	t, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		q.fail(name, "must be an RFC 3339 timestamp, such as 2026-10-30T09:00:00Z")
		return def, false
	}
	return t.UTC(), true
}

func validText(s string, maxLen int) bool {
	return len(s) <= maxLen && utf8.ValidString(s) &&
		strings.IndexFunc(s, func(r rune) bool { return !unicode.IsPrint(r) }) < 0
}

func isHex(s string, length int) bool {
	if len(s) != length {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// humanDuration formats whole-hour durations as "24h" or "7 days".
func humanDuration(d time.Duration) string {
	if d >= 48*time.Hour && d%(24*time.Hour) == 0 {
		return strconv.Itoa(int(d/(24*time.Hour))) + " days"
	}
	if d%time.Hour == 0 {
		return strconv.Itoa(int(d/time.Hour)) + "h"
	}
	return d.String()
}
