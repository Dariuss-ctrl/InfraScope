//go:build integration

package clickhouse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"infrascope/api/internal/backend"
	"infrascope/api/internal/testenv"
)

var (
	env    *testenv.ClickHouse
	client *Client
	fx     testenv.Telemetry
)

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	ctx := context.Background()
	var err error
	env, err = testenv.StartClickHouse(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "start clickhouse:", err)
		return 1
	}
	defer func() {
		_ = env.Collector.Terminate(ctx)
		_ = env.Container.Terminate(ctx)
		_ = env.Network.Remove(ctx)
	}()
	client = New(Config{URL: env.URL, User: env.User, Password: env.Password, DialTimeout: 2 * time.Second, QueryTimeout: 10 * time.Second})
	if fx, err = testenv.SeedTelemetry(ctx, env, time.Now().UTC().Truncate(time.Second).Add(-time.Hour)); err != nil {
		fmt.Fprintln(os.Stderr, "seed:", err)
		return 1
	}
	return m.Run()
}

func window(from, to time.Duration) (time.Time, time.Time) {
	return fx.Base.Add(from), fx.Base.Add(to)
}

func ids(page TracePage) []string {
	out := make([]string, len(page.Traces))
	for i, t := range page.Traces {
		out[i] = t.TraceID
	}
	return out
}

// TestSchemaMatchesSnapshot is the drift check of ADR-0004: the schema the
// pinned collector creates must match testdata/schema. A collector upgrade
// that changes it fails here and has to be reviewed deliberately.
func TestSchemaMatchesSnapshot(t *testing.T) {
	ctx := context.Background()
	for _, table := range []string{"otel_traces", "otel_traces_trace_id_ts", "otel_traces_trace_id_ts_mv", "otel_logs"} {
		want, err := os.ReadFile(filepath.Join("testdata", "schema", table+".sql"))
		if err != nil {
			t.Fatalf("read snapshot: %v", err)
		}
		got, err := env.QueryRaw(ctx, "SHOW CREATE TABLE otel."+table+" FORMAT TSVRaw")
		if err != nil {
			t.Fatalf("SHOW CREATE TABLE %s: %v", table, err)
		}
		if strings.TrimSpace(got) != strings.TrimSpace(string(want)) {
			t.Errorf("schema drift in otel.%s.\n got: %s\nwant: %s\n"+
				"If the change is intended, review the queries that rely on it and update testdata/schema/%s.sql.",
				table, got, want, table)
		}
	}
}

func TestSearchTracesFilters(t *testing.T) {
	ms := time.Millisecond
	dur := func(d time.Duration) *time.Duration { return &d }
	from, to := window(0, 5*time.Minute)
	tests := []struct {
		name string
		q    TraceQuery
		want []string
	}{
		{"service, newest first", TraceQuery{Service: "checkout"}, []string{fx.OrdersFailed, fx.OrdersRead}},
		{"service and operation", TraceQuery{Service: "checkout", Operation: "GET /orders"}, []string{fx.OrdersRead}},
		{"failed requests", TraceQuery{Status: "error"}, []string{fx.OrdersFailed}},
		{"successful requests", TraceQuery{Status: "ok"}, []string{fx.Wide, fx.Products, fx.OrdersRead}},
		{"minimum duration", TraceQuery{MinDuration: dur(200 * ms)}, []string{fx.Wide, fx.OrdersFailed}},
		{"maximum duration", TraceQuery{MaxDuration: dur(50 * ms)}, []string{fx.Products}},
		{"slowest first", TraceQuery{Sort: SortDuration}, []string{fx.Wide, fx.OrdersFailed, fx.OrdersRead, fx.Products}},
		{"unknown service", TraceQuery{Service: "nobody"}, nil},
		{"injection in service", TraceQuery{Service: "' OR 1=1 --"}, nil},
		{"injection in operation", TraceQuery{Operation: "x') OR ('1'='1"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.q.From, tt.q.To, tt.q.Limit = from, to, 100
			page, err := client.SearchTraces(context.Background(), tt.q)
			if err != nil {
				t.Fatalf("SearchTraces: %v", err)
			}
			if got := ids(page); !slices.Equal(got, tt.want) {
				t.Errorf("traces = %v, want %v", got, tt.want)
			}
			if page.NextCursor != "" {
				t.Error("a single page should have no next cursor")
			}
		})
	}
}

func TestEmptyWindowFindsNothing(t *testing.T) {
	page, err := client.SearchTraces(context.Background(), TraceQuery{From: fx.Base.Add(-2 * time.Hour), To: fx.Base.Add(-time.Hour), Limit: 10})
	if err != nil || len(page.Traces) != 0 || page.NextCursor != "" {
		t.Errorf("empty window = %+v, %v; want no traces and no cursor", page, err)
	}
}

func TestTraceSummariesCountSpansAndErrors(t *testing.T) {
	from, to := window(0, 5*time.Minute)
	page, err := client.SearchTraces(context.Background(), TraceQuery{From: from, To: to, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]struct {
		spans    int
		hasError bool
	}{
		fx.OrdersRead: {2, false}, fx.OrdersFailed: {2, true}, fx.Products: {1, false}, fx.Wide: {30, false},
	}
	for _, tr := range page.Traces {
		w := want[tr.TraceID]
		if tr.SpanCount != w.spans || tr.HasError != w.hasError {
			t.Errorf("%s: spans=%d hasError=%v, want %d %v", tr.RootOperation, tr.SpanCount, tr.HasError, w.spans, w.hasError)
		}
	}
}

// TestSearchTracesPaginatesLargeResultSets walks 1,200 traces in pages of
// 500. Three traces share every timestamp, so a cursor without a tie-breaker
// would skip or repeat rows at page boundaries.
func TestSearchTracesPaginatesLargeResultSets(t *testing.T) {
	for _, sortBy := range []string{SortStartTime, SortDuration} {
		t.Run(sortBy, func(t *testing.T) {
			q := TraceQuery{From: fx.Base, To: fx.Base.Add(time.Hour), Service: "bulk", Sort: sortBy, Limit: maxPage}
			seen := map[string]bool{}
			var prev *TraceSummary
			pages := 0
			for {
				page, err := client.SearchTraces(context.Background(), q)
				if err != nil {
					t.Fatalf("page %d: %v", pages+1, err)
				}
				pages++
				for _, tr := range page.Traces {
					if seen[tr.TraceID] {
						t.Fatalf("trace %s returned twice", tr.TraceID)
					}
					seen[tr.TraceID] = true
					if prev != nil && !before(sortBy, tr, *prev) {
						t.Fatalf("order broken between %s and %s", prev.TraceID, tr.TraceID)
					}
					prev = &tr
				}
				if page.NextCursor == "" {
					break
				}
				q.Cursor = page.NextCursor
			}
			if len(seen) != fx.BulkTraces || pages != 3 {
				t.Errorf("saw %d traces in %d pages, want %d in 3", len(seen), pages, fx.BulkTraces)
			}
		})
	}
}

const maxPage = 500

// before reports whether a sorts strictly after b in descending order.
func before(sortBy string, a, b TraceSummary) bool {
	ka, kb := a.StartTime.UnixNano(), b.StartTime.UnixNano()
	if sortBy == SortDuration {
		ka, kb = int64(a.Duration), int64(b.Duration)
	}
	if ka != kb {
		return ka < kb
	}
	return a.TraceID < b.TraceID
}

func TestGetTrace(t *testing.T) {
	ctx := context.Background()
	tr, err := client.GetTrace(ctx, fx.OrdersFailed, 100)
	if err != nil {
		t.Fatalf("GetTrace: %v", err)
	}
	if len(tr.Spans) != 2 || tr.Truncated {
		t.Fatalf("spans = %d (truncated %v), want 2", len(tr.Spans), tr.Truncated)
	}
	root, child := tr.Spans[0], tr.Spans[1]
	if root.ParentSpanID != "" || child.ParentSpanID != root.SpanID {
		t.Errorf("tree broken: root parent %q, child parent %q", root.ParentSpanID, child.ParentSpanID)
	}
	if root.Kind != "server" || child.Kind != "client" || child.Status != "error" {
		t.Errorf("kinds/status = %s/%s/%s", root.Kind, child.Kind, child.Status)
	}
	if len(child.Events) != 1 || child.Events[0].Name != "exception" || child.Events[0].Attributes["exception.message"] != "deadlock detected" {
		t.Errorf("events = %+v, want the recorded exception", child.Events)
	}
	if !tr.Start.Equal(fx.Base.Add(2*time.Minute)) || !tr.End.Equal(fx.Base.Add(2*time.Minute+300*time.Millisecond)) {
		t.Errorf("bounds = %v..%v", tr.Start, tr.End)
	}

	if _, err := client.GetTrace(ctx, testenv.TraceID(999_999), 100); !errors.Is(err, backend.ErrNotFound) {
		t.Errorf("unknown trace: err = %v, want ErrNotFound", err)
	}
}

func TestGetTraceTruncatesLargeTraces(t *testing.T) {
	tr, err := client.GetTrace(context.Background(), fx.Wide, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.Spans) != 10 || !tr.Truncated {
		t.Errorf("spans = %d, truncated = %v; want 10 and true", len(tr.Spans), tr.Truncated)
	}
	full, err := client.GetTrace(context.Background(), fx.Wide, 100)
	if err != nil || len(full.Spans) != 30 || full.Truncated {
		t.Errorf("full trace = %d spans, truncated %v, %v; want 30", len(full.Spans), full.Truncated, err)
	}
}

// TestTraceLookupUsesItsIndexes checks the claim in SPEC AC-3.4: the span
// query is bounded by partition pruning and the TraceId skip index.
func TestTraceLookupUsesItsIndexes(t *testing.T) {
	start := fx.Base.Add(time.Minute).Unix()
	var plan []string
	err := client.query(context.Background(), "EXPLAIN indexes = 1 "+traceSpansSQL,
		spanParams(fx.OrdersRead, start, start, 100),
		func(row json.RawMessage) error {
			var r struct {
				Explain string `json:"explain"`
			}
			if err := json.Unmarshal(row, &r); err != nil {
				return err
			}
			plan = append(plan, r.Explain)
			return nil
		})
	if err != nil {
		t.Fatalf("EXPLAIN: %v", err)
	}
	text := strings.Join(plan, "\n")
	for _, want := range []string{"Partition", "idx_trace_id"} {
		if !strings.Contains(text, want) {
			t.Errorf("plan does not use %s:\n%s", want, text)
		}
	}
}

// TestMissingIndexDegradesButStaysCorrect drops a skip index: the schema
// check must name it, and searches must still return the right results.
func TestMissingIndexDegradesButStaysCorrect(t *testing.T) {
	ctx := context.Background()
	if err := client.CheckSchema(ctx); err != nil {
		t.Fatalf("CheckSchema on the intact schema: %v", err)
	}
	if err := env.Exec(ctx, "ALTER TABLE otel.otel_traces DROP INDEX idx_duration"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// idx_duration is the table's last index, so re-adding it restores
		// the exact DDL the drift test compares against.
		if err := env.Exec(ctx, "ALTER TABLE otel.otel_traces ADD INDEX idx_duration Duration TYPE minmax GRANULARITY 1"); err != nil {
			t.Errorf("restore index: %v", err)
		}
	})

	err := client.CheckSchema(ctx)
	if err == nil || !strings.Contains(err.Error(), "otel.otel_traces.idx_duration") {
		t.Fatalf("CheckSchema = %v, want it to name the missing index", err)
	}
	from, to := window(0, 5*time.Minute)
	d := 200 * time.Millisecond
	page, err := client.SearchTraces(ctx, TraceQuery{From: from, To: to, MinDuration: &d, Limit: 10})
	if err != nil || !slices.Equal(ids(page), []string{fx.Wide, fx.OrdersFailed}) {
		t.Errorf("search without the index = %v, %v; want the same results as with it", ids(page), err)
	}
}

func TestSearchLogs(t *testing.T) {
	from, to := window(0, 5*time.Minute)
	body := func(page LogPage) []string {
		out := make([]string, len(page.Logs))
		for i, l := range page.Logs {
			out[i] = l.Body
		}
		return out
	}
	tests := []struct {
		name string
		q    LogQuery
		want []string
	}{
		{"one trace, newest first", LogQuery{TraceID: fx.OrdersRead},
			[]string{"request handled", "slow query detected", "cache miss"}},
		{"warnings and above", LogQuery{MinSeverity: 13}, []string{"order failed: deadlock detected", "slow query detected"}},
		{"words, any case", LogQuery{Tokens: Tokens("SLOW Query")}, []string{"slow query detected"}},
		{"one span", LogQuery{SpanID: testenv.SpanID(4)}, []string{"order failed: deadlock detected"}},
		{"trace with no logs", LogQuery{TraceID: fx.Products}, nil},
		{"records outside any trace", LogQuery{Service: "cron"}, []string{"nightly job finished"}},
		{"injection in service", LogQuery{Service: "' OR 1=1 --"}, nil},
		{"injection in search text", LogQuery{Tokens: Tokens("'); DROP TABLE otel.otel_logs; --")}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.q.From, tt.q.To, tt.q.Limit = from, to, 100
			page, err := client.SearchLogs(context.Background(), tt.q)
			if err != nil {
				t.Fatalf("SearchLogs: %v", err)
			}
			got := body(page)
			// Records sharing a timestamp come back in a stable but
			// hash-defined order, so compare those as a set.
			if !sameMultiset(got, tt.want) || (len(got) > 0 && got[0] != tt.want[0]) {
				t.Errorf("logs = %q, want %q", got, tt.want)
			}
		})
	}
	if out, err := env.Query(context.Background(), "EXISTS TABLE otel.otel_logs"); err != nil || strings.TrimSpace(out) != "1" {
		t.Errorf("otel_logs no longer exists after the injection attempt: %q, %v", out, err)
	}
}

func sameMultiset(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

func TestSearchLogsPaginatesWithTies(t *testing.T) {
	q := LogQuery{From: fx.Base, To: fx.Base.Add(time.Hour), Service: "bulk-logs", Limit: 100}
	seen := map[string]bool{}
	for pages := 1; ; pages++ {
		page, err := client.SearchLogs(context.Background(), q)
		if err != nil {
			t.Fatalf("page %d: %v", pages, err)
		}
		for _, l := range page.Logs {
			if seen[l.Body] {
				t.Fatalf("%q returned twice", l.Body)
			}
			seen[l.Body] = true
		}
		if page.NextCursor == "" {
			break
		}
		if pages > 20 {
			t.Fatal("pagination did not terminate")
		}
		q.Cursor = page.NextCursor
	}
	if len(seen) != fx.BulkLogs {
		t.Errorf("saw %d records, want %d", len(seen), fx.BulkLogs)
	}
}

func TestCursorsAreBoundToTheirSearch(t *testing.T) {
	ctx := context.Background()
	logs, err := client.SearchLogs(ctx, LogQuery{From: fx.Base, To: fx.Base.Add(time.Hour), Service: "bulk-logs", Limit: 1})
	if err != nil || logs.NextCursor == "" {
		t.Fatalf("SearchLogs = %v, cursor %q", err, logs.NextCursor)
	}
	_, err = client.SearchTraces(ctx, TraceQuery{From: fx.Base, To: fx.Base.Add(time.Hour), Limit: 1, Cursor: logs.NextCursor})
	if !errors.Is(err, ErrInvalidCursor) {
		t.Errorf("trace search with a log cursor: err = %v, want ErrInvalidCursor", err)
	}
}

func TestQueriesCannotLeaveReadOnlyMode(t *testing.T) {
	err := client.query(context.Background(), "SELECT 1 AS x SETTINGS readonly = 0", nil, func(json.RawMessage) error { return nil })
	if err == nil || errors.Is(err, backend.ErrUnavailable) {
		t.Fatalf("err = %v, want the escalation refused", err)
	}
}

func TestDeadlinesAndConnectionFailures(t *testing.T) {
	expired, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-expired.Done()
	if _, err := client.SearchTraces(expired, TraceQuery{From: fx.Base, To: fx.Base.Add(time.Hour), Limit: 1}); !errors.Is(err, backend.ErrTimeout) {
		t.Errorf("expired deadline: err = %v, want ErrTimeout", err)
	}

	refused := New(Config{URL: "http://127.0.0.1:1", DialTimeout: 2 * time.Second, QueryTimeout: time.Second})
	start := time.Now()
	_, err := refused.SearchLogs(context.Background(), LogQuery{From: fx.Base, To: fx.Base.Add(time.Hour), Limit: 1})
	if !errors.Is(err, backend.ErrUnavailable) {
		t.Errorf("closed port: err = %v, want ErrUnavailable", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("connection failure took %v; the dial timeout should bound it", elapsed)
	}
}
