package pipeline

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// budget is how long each signal may take to become queryable. It is a test
// timeout (AC-1.4), not a latency claim: measured lag lives in benchmark reports.
const budget = 60 * time.Second

// TestRequestIsObservable is the Phase 1 exit criterion (docs/SPEC.md, FR-1):
// one request to the demo service becomes a trace with an HTTP server span and
// a database span, log records that carry the same trace ID, and request
// metrics in Prometheus.
func TestRequestIsObservable(t *testing.T) {
	s := stackFromEnv(t)
	traceID := s.createOrder(t)
	t.Logf("created an order in trace %s", traceID)

	eventually(t, "trace with an HTTP server span and a database span", func() error {
		rows, err := s.clickhouse(`
			SELECT SpanName,
			       ParentSpanId = '' AS is_root,
			       mapContains(SpanAttributes, 'db.system.name') AS is_db
			FROM otel.otel_traces
			WHERE TraceId = {trace_id:String} AND ServiceName = 'demo-service'`, traceID)
		if err != nil {
			return err
		}
		var root, db bool
		for _, r := range rows {
			root = root || (r[0] == "POST /orders" && r[1] == "1")
			db = db || r[2] == "1"
		}
		if !root || !db {
			return fmt.Errorf("want a root span %q and a database span; have %v", "POST /orders", rows)
		}
		return nil
	})

	eventually(t, "log records carrying the trace ID", func() error {
		rows, err := s.clickhouse(`SELECT count() FROM otel.otel_logs WHERE TraceId = {trace_id:String}`, traceID)
		if err != nil {
			return err
		}
		if len(rows) != 1 || rows[0][0] == "0" {
			return errors.New("no log records with this trace ID yet")
		}
		return nil
	})

	eventually(t, "request-duration metrics for the route", func() error {
		count, err := s.prometheusValue(`sum(http_server_request_duration_seconds_count{` +
			`service_name="demo-service", http_route="/orders", http_request_method="POST"})`)
		if err != nil {
			return err
		}
		if count < 1 {
			return fmt.Errorf("request count is %v", count)
		}
		return nil
	})
}

// stack describes a running deployment with internal ports published
// (make up-debug). `make test-pipeline` fills the environment from .env.
type stack struct {
	demoURL, clickhouseURL, prometheusURL string
	clickhouseUser, clickhousePassword    string
	client                                *http.Client
}

func stackFromEnv(t *testing.T) stack {
	t.Helper()
	if os.Getenv("INFRASCOPE_PIPELINE") == "" {
		t.Skip("needs a running stack: make up-debug, then make test-pipeline")
	}
	return stack{
		demoURL:            envOr("DEMO_URL", "http://localhost:"+envOr("DEMO_SERVICE_PORT", "8082")),
		clickhouseURL:      envOr("CLICKHOUSE_URL", "http://localhost:8123"),
		prometheusURL:      envOr("PROMETHEUS_URL", "http://localhost:9090"),
		clickhouseUser:     envOr("CLICKHOUSE_USER", "infrascope"),
		clickhousePassword: os.Getenv("CLICKHOUSE_PASSWORD"),
		client:             &http.Client{Timeout: 10 * time.Second},
	}
}

var traceIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// createOrder places an order for a unique customer and returns the trace ID
// the demo service reports in X-Trace-Id.
func (s stack) createOrder(t *testing.T) string {
	t.Helper()
	body := fmt.Sprintf(`{"customer_email":"pipeline-%d@example.com","product_id":1,"quantity":1}`, time.Now().UnixNano())
	resp, err := s.client.Post(s.demoURL+"/orders", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /orders: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST /orders = %d: %s", resp.StatusCode, b)
	}
	traceID := resp.Header.Get("X-Trace-Id")
	if !traceIDPattern.MatchString(traceID) {
		t.Fatalf("X-Trace-Id = %q, want 32 lowercase hex characters", traceID)
	}
	return traceID
}

// clickhouse runs a read-only query over ClickHouse's HTTP interface, with
// traceID bound to {trace_id:String}, and returns the tab-separated rows.
func (s stack) clickhouse(query, traceID string) ([][]string, error) {
	params := url.Values{
		"query":          {query + "\nFORMAT TabSeparated"},
		"param_trace_id": {traceID},
	}
	req, err := http.NewRequest(http.MethodGet, s.clickhouseURL+"/?"+params.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(s.clickhouseUser, s.clickhousePassword)
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("clickhouse answered %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var rows [][]string
	for line := range strings.Lines(string(body)) {
		if line = strings.TrimRight(line, "\n"); line != "" {
			rows = append(rows, strings.Split(line, "\t"))
		}
	}
	return rows, nil
}

// prometheusValue runs an instant query that returns one series and parses
// its value.
func (s stack) prometheusValue(query string) (float64, error) {
	resp, err := s.client.Get(s.prometheusURL + "/api/v1/query?" + url.Values{"query": {query}}.Encode())
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	var out struct {
		Status string `json:"status"`
		Error  string `json:"error"`
		Data   struct {
			Result []struct {
				Value [2]any `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0, fmt.Errorf("decode prometheus response: %w", err)
	}
	if out.Status != "success" {
		return 0, fmt.Errorf("prometheus: %s", out.Error)
	}
	if len(out.Data.Result) == 0 {
		return 0, errors.New("no matching series yet")
	}
	raw, _ := out.Data.Result[0].Value[1].(string)
	return strconv.ParseFloat(raw, 64)
}

// eventually retries check until it succeeds or the budget runs out.
func eventually(t *testing.T, what string, check func() error) {
	t.Helper()
	deadline := time.Now().Add(budget)
	for {
		err := check()
		if err == nil {
			t.Logf("observed: %s", what)
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: not observed within %v: %v", what, budget, err)
		}
		time.Sleep(2 * time.Second)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
