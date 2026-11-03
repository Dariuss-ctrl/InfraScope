//go:build integration

package httpapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pb33f/libopenapi"
	validator "github.com/pb33f/libopenapi-validator"
	"golang.org/x/crypto/bcrypt"

	"infrascope/api/internal/auth"
	"infrascope/api/internal/clickhouse"
	"infrascope/api/internal/health"
	"infrascope/api/internal/postgres"
	"infrascope/api/internal/prometheus"
	"infrascope/api/internal/testenv"
)

const (
	adminUser     = "admin"
	adminPassword = "correct-horse-battery"
)

var (
	pgEnv    *testenv.Postgres
	chEnv    *testenv.ClickHouse
	promEnv  *testenv.Prometheus
	fx       testenv.Telemetry
	db       *postgres.DB
	authSvc  *auth.Service
	chStore  *clickhouse.Client
	prom     *prometheus.Client
	contract validator.Validator
	logger   = slog.New(slog.NewTextHandler(io.Discard, nil))
)

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	ctx := context.Background()
	fail := func(what string, err error) int {
		fmt.Fprintf(os.Stderr, "%s: %v\n", what, err)
		return 1
	}

	// The three databases start in parallel.
	var wg sync.WaitGroup
	var pgErr, chErr, promErr error
	now := time.Now().UTC().Truncate(15 * time.Second)
	wg.Go(func() { pgEnv, pgErr = testenv.StartPostgres(ctx) })
	wg.Go(func() { chEnv, chErr = testenv.StartClickHouse(ctx) })
	wg.Go(func() {
		promEnv, promErr = testenv.StartPrometheus(ctx, testenv.HistogramOpenMetrics([]testenv.RequestSeries{
			{Service: "checkout", Route: "/orders", Method: "GET", Status: 200, Rate: 9.5, Shares: map[float64]float64{0.05: 1}},
			{Service: "checkout", Route: "/orders", Method: "GET", Status: 500, Rate: 0.5, Shares: map[float64]float64{0.25: 1}},
			{Service: "catalog", Route: "/products", Method: "GET", Status: 200, Rate: 4, Shares: map[float64]float64{0.01: 1}},
		}, now.Add(-41*time.Minute), now.Add(-time.Minute), 15*time.Second))
	})
	wg.Wait()
	defer func() {
		if pgEnv != nil {
			_ = pgEnv.Container.Terminate(ctx)
		}
		if chEnv != nil {
			_ = chEnv.Collector.Terminate(ctx)
			_ = chEnv.Container.Terminate(ctx)
			_ = chEnv.Network.Remove(ctx)
		}
		if promEnv != nil {
			_ = promEnv.Container.Terminate(ctx)
		}
	}()
	for what, err := range map[string]error{"postgres": pgErr, "clickhouse": chErr, "prometheus": promErr} {
		if err != nil {
			return fail("start "+what, err)
		}
	}

	var err error
	if db, err = postgres.Open(ctx, pgEnv.URL, postgres.Options{ConnectTimeout: 2 * time.Second}); err != nil {
		return fail("open postgres", err)
	}
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		return fail("migrate", err)
	}
	if authSvc, err = auth.NewService(db, time.Hour, bcrypt.MinCost); err != nil {
		return fail("auth service", err)
	}
	if _, err := authSvc.Bootstrap(ctx, adminUser, adminPassword); err != nil {
		return fail("bootstrap admin", err)
	}
	if fx, err = testenv.SeedTelemetry(ctx, chEnv, now.Add(-time.Hour)); err != nil {
		return fail("seed telemetry", err)
	}
	chStore = clickhouse.New(clickhouse.Config{URL: chEnv.URL, User: chEnv.User, Password: chEnv.Password,
		DialTimeout: 2 * time.Second, QueryTimeout: 10 * time.Second})
	prom = prometheus.New(prometheus.Config{URL: promEnv.URL, DialTimeout: 2 * time.Second, QueryTimeout: 10 * time.Second})

	spec, err := os.ReadFile("../../openapi.yaml")
	if err != nil {
		return fail("read contract", err)
	}
	doc, err := libopenapi.NewDocument(spec)
	if err != nil {
		return fail("parse contract", err)
	}
	v, errs := validator.NewValidator(doc)
	if len(errs) > 0 {
		return fail("build contract validator", errs[0])
	}
	contract = v
	return m.Run()
}

// stack is the real router over the real stores; fields override parts of it.
type stack struct {
	telemetry TelemetryStore
	metrics   MetricStore
	auth      Authenticator
	limiter   *auth.Limiter
	health    *health.Checker
}

func (s stack) handler() http.Handler {
	d := Deps{
		Health: health.NewChecker(2*time.Second, logger), Auth: authSvc, Limiter: auth.NewLimiter(100, time.Second),
		Telemetry: chStore, Metrics: prom, Logger: logger,
		Settings: Settings{MaxSearchRange: 24 * time.Hour, MaxMetricsRange: 7 * 24 * time.Hour, MaxSpans: 10_000},
	}
	if s.telemetry != nil {
		d.Telemetry = s.telemetry
	}
	if s.metrics != nil {
		d.Metrics = s.metrics
	}
	if s.auth != nil {
		d.Auth = s.auth
	}
	if s.limiter != nil {
		d.Limiter = s.limiter
	}
	if s.health != nil {
		d.Health = s.health
	}
	return NewRouter(d)
}

type apiCall struct {
	method, path, body string
	token              string
	header             map[string]string
	// offContract marks requests to routes the contract does not define.
	offContract bool
}

// call sends c through h and checks the response against openapi.yaml, so
// every test doubles as a contract test.
func call(t *testing.T, h http.Handler, c apiCall) (*http.Response, []byte) {
	t.Helper()
	method := c.method
	if method == "" {
		method = http.MethodGet
	}
	req := httptest.NewRequest(method, "http://localhost:8080"+c.path, strings.NewReader(c.body))
	if c.body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range c.header {
		req.Header.Set(k, v)
	}
	if c.token != "" {
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: c.token})
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	resp := rec.Result()
	body, _ := io.ReadAll(resp.Body)

	if !c.offContract {
		resp.Body = io.NopCloser(bytes.NewReader(body))
		if ok, errs := contract.ValidateHttpResponse(req, resp); !ok {
			for _, e := range errs {
				t.Errorf("contract violation: %s %s -> %d: %s: %s", method, c.path, resp.StatusCode, e.Message, e.Reason)
				for _, s := range e.SchemaValidationErrors {
					t.Errorf("    %s (at %s)", s.Reason, s.FieldPath)
				}
			}
		}
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	return resp, body
}

func login(t *testing.T, h http.Handler) string {
	t.Helper()
	resp, body := call(t, h, apiCall{method: "POST", path: "/api/v1/auth/login",
		body: fmt.Sprintf(`{"username":%q,"password":%q}`, adminUser, adminPassword)})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("login = %d: %s", resp.StatusCode, body)
	}
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookieName {
			return c.Value
		}
	}
	t.Fatal("login set no session cookie")
	return ""
}

func fixtureRange() string {
	return "from=" + url.QueryEscape(fx.Base.Format(time.RFC3339)) + "&to=" + url.QueryEscape(fx.Base.Add(time.Hour).Format(time.RFC3339))
}

func errorCode(t *testing.T, body []byte) string {
	t.Helper()
	var e errorEnvelope
	if err := json.Unmarshal(body, &e); err != nil {
		t.Fatalf("not an error envelope: %s", body)
	}
	return e.Error.Code
}

func TestJourneyThroughTheContract(t *testing.T) {
	h := stack{}.handler()
	token := login(t, h)

	if resp, body := call(t, h, apiCall{path: "/api/v1/auth/me", token: token}); resp.StatusCode != 200 || !strings.Contains(string(body), `"username":"admin"`) {
		t.Errorf("me = %d %s", resp.StatusCode, body)
	}

	resp, body := call(t, h, apiCall{path: "/api/v1/services", token: token})
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"name":"checkout"`) || !strings.Contains(string(body), `"name":"catalog"`) {
		t.Errorf("services = %d %s", resp.StatusCode, body)
	}
	if resp, body := call(t, h, apiCall{path: "/api/v1/services/checkout/metrics", token: token}); resp.StatusCode != 200 {
		t.Errorf("service metrics = %d %s", resp.StatusCode, body)
	}

	resp, body = call(t, h, apiCall{path: "/api/v1/traces?service=checkout&" + fixtureRange(), token: token})
	var traces traceSearchJSON
	if resp.StatusCode != 200 || json.Unmarshal(body, &traces) != nil || len(traces.Traces) != 2 {
		t.Fatalf("trace search = %d %s", resp.StatusCode, body)
	}

	resp, body = call(t, h, apiCall{path: "/api/v1/traces/" + fx.OrdersFailed, token: token})
	var tr traceJSON
	if resp.StatusCode != 200 || json.Unmarshal(body, &tr) != nil || len(tr.Spans) != 2 || len(tr.Spans[1].Events) != 1 {
		t.Fatalf("trace = %d %s", resp.StatusCode, body)
	}

	resp, body = call(t, h, apiCall{path: "/api/v1/logs?trace_id=" + fx.OrdersRead + "&" + fixtureRange(), token: token})
	var logs logSearchJSON
	if resp.StatusCode != 200 || json.Unmarshal(body, &logs) != nil || len(logs.Logs) != 3 {
		t.Fatalf("logs = %d %s", resp.StatusCode, body)
	}
	for _, l := range logs.Logs {
		if l.TraceID == nil || *l.TraceID != fx.OrdersRead {
			t.Errorf("log %q does not link back to its trace", l.Body)
		}
	}

	// The schema the collector created is complete, so nothing is missing.
	resp, body = call(t, h, apiCall{path: "/api/v1/diagnostics/schema", token: token})
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"complete":true`) || !strings.Contains(string(body), `"missing":[]`) {
		t.Errorf("schema diagnostics = %d %s, want a complete schema", resp.StatusCode, body)
	}

	if resp, _ := call(t, h, apiCall{method: "POST", path: "/api/v1/auth/logout", token: token}); resp.StatusCode != 204 {
		t.Errorf("logout = %d", resp.StatusCode)
	}
	if resp, _ := call(t, h, apiCall{path: "/api/v1/auth/me", token: token}); resp.StatusCode != 401 {
		t.Errorf("me after logout = %d, want 401", resp.StatusCode)
	}
}

func TestAuthenticationFailures(t *testing.T) {
	ctx := context.Background()
	h := stack{}.handler()

	for _, path := range []string{"/api/v1/auth/me", "/api/v1/services", "/api/v1/traces", "/api/v1/logs",
		"/api/v1/traces/" + fx.OrdersRead, "/api/v1/services/checkout/metrics", "/api/v1/diagnostics/schema"} {
		if resp, body := call(t, h, apiCall{path: path}); resp.StatusCode != 401 || errorCode(t, body) != "unauthenticated" {
			t.Errorf("GET %s without a session = %d %s", path, resp.StatusCode, body)
		}
	}

	if resp, _ := call(t, h, apiCall{method: "POST", path: "/api/v1/auth/login", body: `{"username":"admin","password":"guess"}`}); resp.StatusCode != 401 {
		t.Errorf("wrong password = %d, want 401", resp.StatusCode)
	}
	if resp, _ := call(t, h, apiCall{method: "POST", path: "/api/v1/auth/login", body: `{"username":"admin"`}); resp.StatusCode != 400 {
		t.Errorf("malformed body = %d, want 400", resp.StatusCode)
	}

	// An expired session, written straight into the database.
	user, err := db.UserByName(ctx, adminUser)
	if err != nil {
		t.Fatal(err)
	}
	expired := newToken(t)
	if err := db.CreateSession(ctx, hashOf(expired), user.ID, time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	resp, body := call(t, h, apiCall{path: "/api/v1/auth/me", token: expired})
	if resp.StatusCode != 401 || errorCode(t, body) != "unauthenticated" {
		t.Errorf("expired session = %d %s, want 401", resp.StatusCode, body)
	}
	if !strings.Contains(resp.Header.Get("Set-Cookie"), "Max-Age=0") {
		t.Errorf("expired session cookie not cleared: %q", resp.Header.Get("Set-Cookie"))
	}

	// A well-formed token that was never issued, or was revoked.
	if resp, _ := call(t, h, apiCall{path: "/api/v1/auth/me", token: newToken(t)}); resp.StatusCode != 401 {
		t.Errorf("unknown session = %d, want 401", resp.StatusCode)
	}

	limited := stack{limiter: auth.NewLimiter(2, time.Hour)}.handler()
	for attempt := 1; attempt <= 3; attempt++ {
		resp, _ := call(t, limited, apiCall{method: "POST", path: "/api/v1/auth/login", body: `{"username":"admin","password":"guess"}`})
		want := http.StatusUnauthorized
		if attempt == 3 {
			want = http.StatusTooManyRequests
		}
		if resp.StatusCode != want {
			t.Errorf("attempt %d = %d, want %d", attempt, resp.StatusCode, want)
		}
	}
}

func TestInvalidDateRanges(t *testing.T) {
	h := stack{}.handler()
	token := login(t, h)
	for name, query := range map[string]string{
		"from after to":   "from=2026-10-30T11:00:00Z&to=2026-10-30T10:00:00Z",
		"range too wide":  "from=2026-10-01T00:00:00Z&to=2026-10-03T00:00:00Z",
		"not a timestamp": "from=yesterday",
		"in the future":   "to=" + url.QueryEscape(time.Now().Add(24*time.Hour).UTC().Format(time.RFC3339)),
	} {
		for _, path := range []string{"/api/v1/traces", "/api/v1/logs"} {
			resp, body := call(t, h, apiCall{path: path + "?" + query, token: token})
			if resp.StatusCode != 400 || errorCode(t, body) != "invalid_argument" {
				t.Errorf("%s %s = %d %s, want 400 invalid_argument", name, path, resp.StatusCode, body)
			}
		}
	}
}

func TestEmptyResultsAndTracesWithoutLogs(t *testing.T) {
	h := stack{}.handler()
	token := login(t, h)
	empty := "from=" + url.QueryEscape(fx.Base.Add(-3*time.Hour).Format(time.RFC3339)) +
		"&to=" + url.QueryEscape(fx.Base.Add(-2*time.Hour).Format(time.RFC3339))

	if _, body := call(t, h, apiCall{path: "/api/v1/traces?" + empty, token: token}); strings.TrimSpace(string(body)) != `{"traces":[],"next_cursor":null}` {
		t.Errorf("empty trace search = %s", body)
	}
	if _, body := call(t, h, apiCall{path: "/api/v1/logs?trace_id=" + fx.Products + "&" + fixtureRange(), token: token}); strings.TrimSpace(string(body)) != `{"logs":[],"next_cursor":null}` {
		t.Errorf("logs of a trace without logs = %s", body)
	}
	if resp, body := call(t, h, apiCall{path: "/api/v1/traces/" + testenv.TraceID(424242), token: token}); resp.StatusCode != 404 || errorCode(t, body) != "not_found" {
		t.Errorf("unknown trace = %d %s, want 404", resp.StatusCode, body)
	}
}

func TestLargeResultSetsPaginate(t *testing.T) {
	h := stack{}.handler()
	token := login(t, h)
	if resp, _ := call(t, h, apiCall{path: "/api/v1/traces?limit=501", token: token}); resp.StatusCode != 400 {
		t.Errorf("limit above 500 = %d, want 400", resp.StatusCode)
	}

	seen := map[string]bool{}
	path := "/api/v1/traces?service=bulk&limit=500&" + fixtureRange()
	for pages := 1; ; pages++ {
		resp, body := call(t, h, apiCall{path: path, token: token})
		var page traceSearchJSON
		if resp.StatusCode != 200 || json.Unmarshal(body, &page) != nil {
			t.Fatalf("page %d = %d %s", pages, resp.StatusCode, body)
		}
		for _, tr := range page.Traces {
			if seen[tr.TraceID] {
				t.Fatalf("trace %s on two pages", tr.TraceID)
			}
			seen[tr.TraceID] = true
		}
		if page.NextCursor == nil {
			break
		}
		if pages > 5 {
			t.Fatal("pagination did not terminate")
		}
		path = "/api/v1/traces?service=bulk&limit=500&" + fixtureRange() + "&cursor=" + url.QueryEscape(*page.NextCursor)
	}
	if len(seen) != fx.BulkTraces {
		t.Errorf("paged through %d traces, want %d", len(seen), fx.BulkTraces)
	}
}

func TestInjectionAttemptsAreInert(t *testing.T) {
	ctx := context.Background()
	h := stack{}.handler()
	token := login(t, h)
	q := url.QueryEscape
	tests := []struct {
		path string
		want int
	}{
		{"/api/v1/traces?service=" + q("' OR 1=1 --") + "&" + fixtureRange(), 200},
		{"/api/v1/traces?operation=" + q("x') OR ('1'='1") + "&" + fixtureRange(), 200},
		{"/api/v1/logs?q=" + q("'); DROP TABLE otel.otel_logs; --") + "&" + fixtureRange(), 200},
		{"/api/v1/logs?service=" + q(`checkout' UNION SELECT * FROM system.users --`) + "&" + fixtureRange(), 200},
		{"/api/v1/logs?trace_id=" + q("abc' OR '1'='1"), 400},
		{"/api/v1/traces?cursor=" + q(`{"k":"traces","s":"start_time","v":0,"t":"' OR 1=1"}`), 400},
		{"/api/v1/services/" + url.PathEscape(`checkout"} or vector(1) #`) + "/metrics", 404},
	}
	for _, tt := range tests {
		resp, body := call(t, h, apiCall{path: tt.path, token: token})
		if resp.StatusCode != tt.want {
			t.Errorf("GET %s = %d %s, want %d", tt.path, resp.StatusCode, body, tt.want)
			continue
		}
		if tt.want == 200 && (strings.Contains(string(body), `"trace_id"`) || strings.Contains(string(body), `"body"`)) {
			t.Errorf("GET %s returned data: %s", tt.path, body)
		}
	}
	for _, table := range []string{"otel_logs", "otel_traces"} {
		if out, err := chEnv.Query(ctx, "EXISTS TABLE otel."+table); err != nil || strings.TrimSpace(out) != "1" {
			t.Errorf("otel.%s missing after the injection attempts: %q %v", table, out, err)
		}
	}
}

// TestDegradedModeAndOutages covers NFR-1: losing one backend disables only
// the features it serves, and every failure is a fast, typed error.
func TestDegradedModeAndOutages(t *testing.T) {
	ctx := context.Background()
	token := login(t, stack{}.handler())

	t.Run("ClickHouse unreachable", func(t *testing.T) {
		down := clickhouse.New(clickhouse.Config{URL: "http://127.0.0.1:1", DialTimeout: 2 * time.Second, QueryTimeout: time.Second})
		h := stack{telemetry: down, health: health.NewChecker(2*time.Second, logger,
			health.Dependency{Name: "postgres", Required: true, Probe: db.Ping},
			health.Dependency{Name: "clickhouse", Probe: health.HTTPProbe(http.DefaultClient, "http://127.0.0.1:1/ping")},
		)}.handler()
		if resp, body := call(t, h, apiCall{path: "/api/v1/traces", token: token}); resp.StatusCode != 503 || errorCode(t, body) != "storage_unavailable" {
			t.Errorf("traces = %d %s, want 503 storage_unavailable", resp.StatusCode, body)
		}
		if resp, _ := call(t, h, apiCall{path: "/api/v1/services", token: token}); resp.StatusCode != 200 {
			t.Errorf("services = %d, want 200: metrics do not depend on ClickHouse", resp.StatusCode)
		}
		if _, body := call(t, h, apiCall{path: "/api/v1/status"}); !strings.Contains(string(body), `"status":"degraded"`) {
			t.Errorf("status = %s, want degraded", body)
		}
	})

	t.Run("ClickHouse too slow", func(t *testing.T) {
		blackHole := listenAndHang(t)
		slow := clickhouse.New(clickhouse.Config{URL: "http://" + blackHole, DialTimeout: 2 * time.Second, QueryTimeout: 100 * time.Millisecond})
		start := time.Now()
		resp, body := call(t, stack{telemetry: slow}.handler(), apiCall{path: "/api/v1/logs", token: token})
		if resp.StatusCode != 504 || errorCode(t, body) != "query_timeout" {
			t.Errorf("logs = %d %s, want 504 query_timeout", resp.StatusCode, body)
		}
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Errorf("timeout took %v", elapsed)
		}
	})

	t.Run("Prometheus stopped", func(t *testing.T) {
		stopped, err := testenv.StartPrometheus(ctx, testenv.HistogramOpenMetrics([]testenv.RequestSeries{
			{Service: "x", Route: "/", Method: "GET", Status: 200, Rate: 1, Shares: map[float64]float64{0.01: 1}},
		}, time.Now().Add(-5*time.Minute), time.Now().Add(-4*time.Minute), 15*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = stopped.Container.Terminate(ctx) })
		timeout := 5 * time.Second
		if err := stopped.Container.Stop(ctx, &timeout); err != nil {
			t.Fatal(err)
		}
		h := stack{metrics: prometheus.New(prometheus.Config{URL: stopped.URL, DialTimeout: 2 * time.Second, QueryTimeout: time.Second})}.handler()
		if resp, body := call(t, h, apiCall{path: "/api/v1/services", token: token}); resp.StatusCode != 503 || errorCode(t, body) != "metrics_unavailable" {
			t.Errorf("services = %d %s, want 503 metrics_unavailable", resp.StatusCode, body)
		}
		if resp, _ := call(t, h, apiCall{path: "/api/v1/traces?" + fixtureRange(), token: token}); resp.StatusCode != 200 {
			t.Errorf("traces = %d, want 200: traces do not depend on Prometheus", resp.StatusCode)
		}
	})

	t.Run("PostgreSQL stopped", func(t *testing.T) {
		pg, err := testenv.StartPostgres(ctx)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = pg.Container.Terminate(ctx) })
		otherDB, err := postgres.Open(ctx, pg.URL, postgres.Options{ConnectTimeout: 2 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		defer otherDB.Close()
		if err := otherDB.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
		svc, err := auth.NewService(otherDB, time.Hour, bcrypt.MinCost)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := svc.Bootstrap(ctx, adminUser, adminPassword); err != nil {
			t.Fatal(err)
		}
		h := stack{auth: svc}.handler()
		otherToken := login(t, h)
		timeout := 5 * time.Second
		if err := pg.Container.Stop(ctx, &timeout); err != nil {
			t.Fatal(err)
		}
		resp, body := call(t, h, apiCall{path: "/api/v1/traces", token: otherToken})
		if resp.StatusCode != 503 || errorCode(t, body) != "database_unavailable" {
			t.Errorf("traces with the database down = %d %s, want 503 database_unavailable (not 401)", resp.StatusCode, body)
		}
	})
}

// listenAndHang accepts connections and never answers: a server that is up
// but too slow, which is how a real query timeout looks from the client.
func listenAndHang(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var conns []net.Conn
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		ln.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			c.Close()
		}
	})
	return ln.Addr().String()
}

func newToken(t *testing.T) string {
	t.Helper()
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// hashOf mirrors how auth stores session tokens.
func hashOf(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}
