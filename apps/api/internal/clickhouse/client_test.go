package clickhouse

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"infrascope/api/internal/backend"
)

func newTestClient(url string) *Client {
	return New(Config{URL: url, User: "reader", Password: "secret", DialTimeout: time.Second, QueryTimeout: 3 * time.Second})
}

func TestQuerySendsReadOnlyParameterisedRequests(t *testing.T) {
	var got *http.Request
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		_, _ = io.WriteString(w, "{\"n\":1}\n{\"n\":2}\n")
	}))
	defer srv.Close()

	var rows []int
	err := newTestClient(srv.URL).query(context.Background(), "SELECT {x:String} AS n",
		map[string]string{"x": "' OR 1=1 --"},
		func(row json.RawMessage) error {
			var r struct{ N int }
			if err := json.Unmarshal(row, &r); err != nil {
				return err
			}
			rows = append(rows, r.N)
			return nil
		})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if !slices.Equal(rows, []int{1, 2}) {
		t.Errorf("rows = %v, want [1 2]", rows)
	}

	q := got.URL.Query()
	for key, want := range map[string]string{
		"readonly":           "2",
		"max_execution_time": "3",
		"wait_end_of_query":  "1",
		"param_x":            "' OR 1=1 --",
	} {
		if q.Get(key) != want {
			t.Errorf("URL parameter %s = %q, want %q", key, q.Get(key), want)
		}
	}
	if strings.Contains(body, "OR 1=1") {
		t.Error("a parameter value leaked into the SQL text")
	}
	if !strings.HasSuffix(body, "FORMAT JSONEachRow") {
		t.Errorf("SQL does not request JSONEachRow: %q", body)
	}
	if user, pass, ok := got.BasicAuth(); !ok || user != "reader" || pass != "secret" {
		t.Error("credentials were not sent as basic auth")
	}
}

func TestServerErrorsAreClassified(t *testing.T) {
	tests := []struct {
		name   string
		status int
		code   string
		want   error // nil: an internal error, neither timeout nor unavailable
	}{
		{"timeout", http.StatusInternalServerError, codeTimeoutExceeded, backend.ErrTimeout},
		{"too slow", http.StatusInternalServerError, codeTooSlow, backend.ErrTimeout},
		{"schema not created yet", http.StatusNotFound, codeUnknownTable, backend.ErrUnavailable},
		{"bad credentials", http.StatusForbidden, codeAuthenticationError, backend.ErrUnavailable},
		{"server overloaded", http.StatusServiceUnavailable, "", backend.ErrUnavailable},
		{"syntax error is our bug", http.StatusBadRequest, "62", nil},
		{"write refused in read-only mode", http.StatusInternalServerError, codeReadonly, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tt.code != "" {
					w.Header().Set("X-ClickHouse-Exception-Code", tt.code)
				}
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, "Code: "+tt.code+". DB::Exception: something\nstack trace")
			}))
			defer srv.Close()

			err := newTestClient(srv.URL).query(context.Background(), "SELECT 1", nil, func(json.RawMessage) error { return nil })
			if err == nil {
				t.Fatal("want an error, got nil")
			}
			isTimeout, isUnavailable := errors.Is(err, backend.ErrTimeout), errors.Is(err, backend.ErrUnavailable)
			switch tt.want {
			case nil:
				if isTimeout || isUnavailable {
					t.Errorf("err = %v, want an internal error", err)
				}
			default:
				if !errors.Is(err, tt.want) {
					t.Errorf("err = %v, want %v", err, tt.want)
				}
			}
			if strings.Contains(err.Error(), "stack trace") {
				t.Errorf("error includes more than the first line: %v", err)
			}
		})
	}
}

func TestUnreachableServerIsUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()

	err := newTestClient(url).query(context.Background(), "SELECT 1", nil, func(json.RawMessage) error { return nil })
	if !errors.Is(err, backend.ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
}

func TestCursorRoundTrip(t *testing.T) {
	c := cursor{Kind: "traces", Sort: SortDuration, Value: 1234, Key: "abc"}
	got, err := decodeCursor(c.encode(), "traces", SortDuration)
	if err != nil || got != c {
		t.Fatalf("decodeCursor = %+v, %v; want %+v", got, err, c)
	}
}

func TestCursorRejectsForeignOrTamperedValues(t *testing.T) {
	traces := cursor{Kind: "traces", Sort: SortStartTime, Value: 1, Key: "abc"}.encode()
	tests := []struct {
		name, raw, kind, sort string
	}{
		{"not base64", "%%%", "traces", SortStartTime},
		{"not JSON", "bm90IGpzb24", "traces", SortStartTime},
		{"another search's cursor", traces, "logs", "timestamp"},
		{"another sort order's cursor", traces, "traces", SortDuration},
		{"negative position", cursor{Kind: "traces", Sort: SortStartTime, Value: -1, Key: "abc"}.encode(), "traces", SortStartTime},
		{"missing tie-breaker", cursor{Kind: "traces", Sort: SortStartTime, Value: 1}.encode(), "traces", SortStartTime},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := decodeCursor(tt.raw, tt.kind, tt.sort); !errors.Is(err, ErrInvalidCursor) {
				t.Errorf("err = %v, want ErrInvalidCursor", err)
			}
		})
	}
}

func TestTokens(t *testing.T) {
	tests := []struct {
		text string
		want []string
	}{
		{"Slow query: DB timeout!", []string{"slow", "query", "db", "timeout"}},
		{"'; DROP TABLE otel.otel_logs; --", []string{"drop", "table", "otel", "otel", "logs"}},
		{"order_42", []string{"order", "42"}},
		{"!!!", nil},
	}
	for _, tt := range tests {
		if got := Tokens(tt.text); !slices.Equal(got, tt.want) {
			t.Errorf("Tokens(%q) = %q, want %q", tt.text, got, tt.want)
		}
	}
}

func TestStringArrayEscapesQuotes(t *testing.T) {
	got := stringArray([]string{"plain", `it's`, `back\slash`})
	want := `['plain','it\'s','back\\slash']`
	if got != want {
		t.Errorf("stringArray = %s, want %s", got, want)
	}
}

func TestNormalisation(t *testing.T) {
	for in, want := range map[string]string{"Server": "server", "Client": "client", "Internal": "internal", "SPAN_KIND_X": "unspecified", "": "unspecified"} {
		if got := normalizeKind(in); got != want {
			t.Errorf("normalizeKind(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{"Ok": "ok", "Error": "error", "Unset": "unset", "": "unset"} {
		if got := normalizeStatus(in); got != want {
			t.Errorf("normalizeStatus(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSeverityFloor(t *testing.T) {
	for name, want := range map[string]int{"trace": 1, "debug": 5, "info": 9, "warn": 13, "error": 17, "fatal": 21} {
		if got, ok := SeverityFloor(name); !ok || got != want {
			t.Errorf("SeverityFloor(%q) = %d, %v; want %d", name, got, ok, want)
		}
	}
	if _, ok := SeverityFloor("WARNING"); ok {
		t.Error("SeverityFloor accepted a name outside the API's enum")
	}
}
