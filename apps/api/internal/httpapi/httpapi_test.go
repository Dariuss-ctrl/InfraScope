package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"infrascope/api/internal/auth"
	"infrascope/api/internal/backend"
	"infrascope/api/internal/clickhouse"
	"infrascope/api/internal/health"
	"infrascope/api/internal/prometheus"
)

var now = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

const goodToken = "good-session-token"

type fakeAuth struct {
	err error // returned by Authenticate and Login when set (database outage)
}

func (f *fakeAuth) Login(_ context.Context, username, password string) (string, time.Time, error) {
	if f.err != nil {
		return "", time.Time{}, f.err
	}
	if username != "admin" || password != "correct-horse" {
		return "", time.Time{}, auth.ErrInvalidCredentials
	}
	return goodToken, now.Add(time.Hour), nil
}

func (f *fakeAuth) Authenticate(_ context.Context, token string) (auth.Session, error) {
	if f.err != nil {
		return auth.Session{}, f.err
	}
	if token != goodToken {
		return auth.Session{}, auth.ErrInvalidSession
	}
	return auth.Session{Username: "admin", ExpiresAt: now.Add(time.Hour)}, nil
}

func (f *fakeAuth) Logout(context.Context, string) error { return f.err }

type fakeTelemetry struct {
	err        error
	traceQuery clickhouse.TraceQuery
	logQuery   clickhouse.LogQuery
	traces     clickhouse.TracePage
	trace      clickhouse.Trace
	logs       clickhouse.LogPage
}

func (f *fakeTelemetry) SearchTraces(_ context.Context, q clickhouse.TraceQuery) (clickhouse.TracePage, error) {
	f.traceQuery = q
	return f.traces, f.err
}

func (f *fakeTelemetry) GetTrace(_ context.Context, traceID string, _ int) (clickhouse.Trace, error) {
	if f.err != nil {
		return clickhouse.Trace{}, f.err
	}
	if traceID != f.trace.TraceID {
		return clickhouse.Trace{}, backend.ErrNotFound
	}
	return f.trace, nil
}

func (f *fakeTelemetry) SearchLogs(_ context.Context, q clickhouse.LogQuery) (clickhouse.LogPage, error) {
	f.logQuery = q
	return f.logs, f.err
}

type fakeMetrics struct {
	err      error
	exists   bool
	services []prometheus.ServiceSummary
	metrics  prometheus.ServiceMetrics
	step     time.Duration
}

func (f *fakeMetrics) Services(context.Context, time.Time, time.Time) ([]prometheus.ServiceSummary, error) {
	return f.services, f.err
}

func (f *fakeMetrics) ServiceMetrics(_ context.Context, _ string, _, _ time.Time, step time.Duration) (prometheus.ServiceMetrics, error) {
	f.step = step
	return f.metrics, f.err
}

func (f *fakeMetrics) ServiceExists(context.Context, string, time.Time, time.Duration) (bool, error) {
	return f.exists, f.err
}

type fixture struct {
	auth      *fakeAuth
	telemetry *fakeTelemetry
	metrics   *fakeMetrics
	limiter   *auth.Limiter
	handler   http.Handler
}

func newFixture() *fixture {
	f := &fixture{
		auth:      &fakeAuth{},
		telemetry: &fakeTelemetry{},
		metrics:   &fakeMetrics{exists: true},
		limiter:   auth.NewLimiter(3, time.Minute),
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	f.handler = NewRouter(Deps{
		Health:    health.NewChecker(time.Second, logger),
		Auth:      f.auth,
		Limiter:   f.limiter,
		Telemetry: f.telemetry,
		Metrics:   f.metrics,
		Logger:    logger,
		Settings:  Settings{MaxSearchRange: 24 * time.Hour, MaxMetricsRange: 7 * 24 * time.Hour, MaxSpans: 100},
		Now:       func() time.Time { return now },
	})
	return f
}

type request struct {
	method, target, body string
	token                string // session cookie; "" for none
	header               map[string]string
}

func (f *fixture) do(t *testing.T, r request) *httptest.ResponseRecorder {
	t.Helper()
	method := r.method
	if method == "" {
		method = http.MethodGet
	}
	req := httptest.NewRequest(method, r.target, strings.NewReader(r.body))
	if r.token != "" {
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: r.token})
	}
	for k, v := range r.header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	return rec
}

func decodeBody[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.NewDecoder(rec.Body).Decode(&v); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	return v
}

func errorOf(t *testing.T, rec *httptest.ResponseRecorder) errorBody {
	t.Helper()
	return decodeBody[errorEnvelope](t, rec).Error
}

func TestEveryProtectedRouteNeedsASession(t *testing.T) {
	f := newFixture()
	routes := []struct{ method, target string }{
		{"POST", "/api/v1/auth/logout"},
		{"GET", "/api/v1/auth/me"},
		{"GET", "/api/v1/services"},
		{"GET", "/api/v1/services/checkout/metrics"},
		{"GET", "/api/v1/traces"},
		{"GET", "/api/v1/traces/0123456789abcdef0123456789abcdef"},
		{"GET", "/api/v1/logs"},
		{"GET", "/api/v1/alert-rules"},
		{"GET", "/api/v1/alerts"},
		{"GET", "/api/v1/no-such-route"},
	}
	for _, rt := range routes {
		for _, token := range []string{"", "forged-or-expired"} {
			t.Run(fmt.Sprintf("%s %s token=%q", rt.method, rt.target, token), func(t *testing.T) {
				rec := f.do(t, request{method: rt.method, target: rt.target, token: token})
				if rec.Code != http.StatusUnauthorized {
					t.Fatalf("status = %d, want 401", rec.Code)
				}
				if got := errorOf(t, rec).Code; got != "unauthenticated" {
					t.Errorf("code = %q, want unauthenticated", got)
				}
			})
		}
	}
}

func TestPublicRoutesNeedNoSession(t *testing.T) {
	f := newFixture()
	for _, target := range []string{"/healthz", "/readyz", "/api/v1/status"} {
		if rec := f.do(t, request{target: target}); rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", target, rec.Code)
		}
	}
}

func TestInvalidSessionClearsTheCookie(t *testing.T) {
	rec := newFixture().do(t, request{target: "/api/v1/auth/me", token: "expired"})
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != sessionCookieName || cookies[0].MaxAge >= 0 {
		t.Errorf("cookies = %+v, want the session cookie expired", cookies)
	}
}

func TestDatabaseOutageIsNot401(t *testing.T) {
	f := newFixture()
	f.auth.err = fmt.Errorf("%w: connection refused", backend.ErrUnavailable)
	rec := f.do(t, request{target: "/api/v1/traces", token: goodToken})
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: an outage must not log anyone out", rec.Code)
	}
	if got := errorOf(t, rec).Code; got != "database_unavailable" {
		t.Errorf("code = %q, want database_unavailable", got)
	}
	if len(rec.Result().Cookies()) != 0 {
		t.Error("the session cookie was cleared during an outage")
	}
}

func TestLogin(t *testing.T) {
	jsonType := map[string]string{"Content-Type": "application/json"}
	tests := []struct {
		name   string
		req    request
		status int
		code   string
	}{
		{"success", request{body: `{"username":"admin","password":"correct-horse"}`, header: jsonType}, http.StatusNoContent, ""},
		{"wrong password", request{body: `{"username":"admin","password":"wrong"}`, header: jsonType}, http.StatusUnauthorized, "unauthenticated"},
		{"unknown user", request{body: `{"username":"root","password":"correct-horse"}`, header: jsonType}, http.StatusUnauthorized, "unauthenticated"},
		{"not JSON", request{body: `username=admin`, header: map[string]string{"Content-Type": "application/x-www-form-urlencoded"}}, http.StatusBadRequest, "invalid_argument"},
		{"malformed JSON", request{body: `{"username":`, header: jsonType}, http.StatusBadRequest, "invalid_argument"},
		{"unknown field", request{body: `{"username":"admin","password":"x","admin":true}`, header: jsonType}, http.StatusBadRequest, "invalid_argument"},
		{"missing password", request{body: `{"username":"admin"}`, header: jsonType}, http.StatusBadRequest, "invalid_argument"},
		{"over-long password", request{body: `{"username":"admin","password":"` + strings.Repeat("x", 73) + `"}`, header: jsonType}, http.StatusBadRequest, "invalid_argument"},
		{"cross-origin", request{body: `{"username":"admin","password":"correct-horse"}`,
			header: map[string]string{"Content-Type": "application/json", "Origin": "http://evil.example"}}, http.StatusForbidden, "forbidden"},
		{"same host, other port", request{body: `{"username":"admin","password":"correct-horse"}`,
			header: map[string]string{"Content-Type": "application/json", "Origin": "http://example.com:3000"}}, http.StatusForbidden, "forbidden"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.req.method, tt.req.target = http.MethodPost, "/api/v1/auth/login"
			rec := newFixture().do(t, tt.req)
			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tt.status, rec.Body)
			}
			if tt.code != "" {
				if got := errorOf(t, rec).Code; got != tt.code {
					t.Errorf("code = %q, want %q", got, tt.code)
				}
			}
		})
	}
}

func TestLoginSetsAHardenedCookie(t *testing.T) {
	rec := newFixture().do(t, request{method: "POST", target: "/api/v1/auth/login",
		body: `{"username":"admin","password":"correct-horse"}`, header: map[string]string{"Content-Type": "application/json"}})
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("got %d cookies, want 1", len(cookies))
	}
	c := cookies[0]
	if c.Name != sessionCookieName || c.Value != goodToken || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" {
		t.Errorf("cookie = %+v, want HttpOnly, SameSite=Strict, Path=/", c)
	}
}

func TestLoginIsRateLimited(t *testing.T) {
	f := newFixture() // burst of 3
	wrong := request{method: "POST", target: "/api/v1/auth/login",
		body: `{"username":"admin","password":"wrong"}`, header: map[string]string{"Content-Type": "application/json"}}
	for range 3 {
		if rec := f.do(t, wrong); rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", rec.Code)
		}
	}
	rec := f.do(t, wrong)
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("status = %d, Retry-After = %q; want 429 with Retry-After", rec.Code, rec.Header().Get("Retry-After"))
	}
	// Another client is not affected.
	other := wrong
	other.header = map[string]string{"Content-Type": "application/json", "X-Forwarded-For": "203.0.113.9"}
	if rec := f.do(t, other); rec.Code != http.StatusUnauthorized {
		t.Errorf("another client got %d, want 401", rec.Code)
	}
}

func TestStateChangingRequestsRejectCrossOrigin(t *testing.T) {
	rec := newFixture().do(t, request{method: "POST", target: "/api/v1/auth/logout", token: goodToken,
		header: map[string]string{"Origin": "http://evil.example"}})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestMeAndLogout(t *testing.T) {
	f := newFixture()
	rec := f.do(t, request{target: "/api/v1/auth/me", token: goodToken})
	if rec.Code != http.StatusOK {
		t.Fatalf("me = %d, want 200", rec.Code)
	}
	if got := decodeBody[map[string]string](t, rec)["username"]; got != "admin" {
		t.Errorf("username = %q, want admin", got)
	}
	rec = f.do(t, request{method: "POST", target: "/api/v1/auth/logout", token: goodToken})
	if rec.Code != http.StatusNoContent || len(rec.Result().Cookies()) != 1 {
		t.Fatalf("logout = %d with cookies %v, want 204 clearing the cookie", rec.Code, rec.Result().Cookies())
	}
}

func TestSearchParameterValidation(t *testing.T) {
	tests := []struct {
		target string
		field  string
	}{
		{"/api/v1/traces?servcie=checkout", "servcie"},
		{"/api/v1/traces?limit=1&limit=2", "limit"},
		{"/api/v1/traces?limit=0", "limit"},
		{"/api/v1/traces?limit=501", "limit"},
		{"/api/v1/traces?from=yesterday", "from"},
		{"/api/v1/traces?from=2026-10-30T11:00:00Z&to=2026-10-30T10:00:00Z", "from"},
		{"/api/v1/traces?from=2026-10-30T10:00:00Z&to=2026-10-30T10:00:00Z", "from"},
		{"/api/v1/traces?from=2026-10-28T00:00:00Z&to=2026-10-30T00:00:00Z", "from"},
		{"/api/v1/traces?to=2026-10-31T00:00:00Z", "to"},
		{"/api/v1/traces?status=failed", "status"},
		{"/api/v1/traces?sort=random", "sort"},
		{"/api/v1/traces?min_duration_ms=-1", "min_duration_ms"},
		{"/api/v1/traces?min_duration_ms=500&max_duration_ms=100", "min_duration_ms"},
		{"/api/v1/traces?service=" + url.QueryEscape("tab\there"), "service"},
		{"/api/v1/traces/not-a-trace-id", "traceId"},
		{"/api/v1/traces/0123456789abcdef0123456789abcdef?verbose=1", "verbose"},
		{"/api/v1/logs?trace_id=" + url.QueryEscape("abc' OR '1'='1"), "trace_id"},
		{"/api/v1/logs?span_id=xyz", "span_id"},
		{"/api/v1/logs?severity=WARNING", "severity"},
		{"/api/v1/logs?q=" + url.QueryEscape("!!!"), "q"},
		{"/api/v1/logs?q=" + url.QueryEscape("a b c d e f g h i"), "q"},
		{"/api/v1/services?from=2026-10-01T00:00:00Z", "from"},
		{"/api/v1/services/checkout/metrics?step_seconds=1", "step_seconds"},
		{"/api/v1/services/checkout/metrics?step_seconds=5&from=2026-10-29T12:00:00Z", "step_seconds"},
	}
	for _, tt := range tests {
		t.Run(tt.target, func(t *testing.T) {
			rec := newFixture().do(t, request{target: tt.target, token: goodToken})
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body)
			}
			body := errorOf(t, rec)
			if body.Code != "invalid_argument" {
				t.Errorf("code = %q, want invalid_argument", body.Code)
			}
			found := false
			for _, f := range body.Fields {
				found = found || f.Field == tt.field
			}
			if !found {
				t.Errorf("fields = %+v, want one for %q", body.Fields, tt.field)
			}
		})
	}
}

func TestSearchParametersReachTheStore(t *testing.T) {
	f := newFixture()
	rec := f.do(t, request{token: goodToken, target: "/api/v1/traces?service=checkout&operation=GET+%2Forders" +
		"&min_duration_ms=100&max_duration_ms=900&status=error&sort=duration&limit=25" +
		"&from=2026-10-30T11:00:00Z&to=2026-10-30T11:30:00Z"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (body %s)", rec.Code, rec.Body)
	}
	q := f.telemetry.traceQuery
	if q.Service != "checkout" || q.Operation != "GET /orders" || q.Status != "error" || q.Sort != "duration" || q.Limit != 25 ||
		*q.MinDuration != 100*time.Millisecond || *q.MaxDuration != 900*time.Millisecond ||
		!q.From.Equal(now.Add(-time.Hour)) || !q.To.Equal(now.Add(-30*time.Minute)) {
		t.Errorf("trace query = %+v", q)
	}

	rec = f.do(t, request{token: goodToken, target: "/api/v1/logs?severity=warn&q=Slow+Query&trace_id=0123456789ABCDEF0123456789ABCDEF"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (body %s)", rec.Code, rec.Body)
	}
	lq := f.telemetry.logQuery
	if lq.MinSeverity != 13 || strings.Join(lq.Tokens, ",") != "slow,query" || lq.TraceID != "0123456789abcdef0123456789abcdef" {
		t.Errorf("log query = %+v", lq)
	}
	if !lq.To.Equal(now) || !lq.From.Equal(now.Add(-time.Hour)) {
		t.Errorf("default range = %v..%v, want the last hour", lq.From, lq.To)
	}
}

func TestEmptyResultsAreEmptyArrays(t *testing.T) {
	f := newFixture()
	for target, want := range map[string]string{
		"/api/v1/traces":   `{"traces":[],"next_cursor":null}`,
		"/api/v1/logs":     `{"logs":[],"next_cursor":null}`,
		"/api/v1/services": `{"services":[]}`,
	} {
		rec := f.do(t, request{target: target, token: goodToken})
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d", target, rec.Code)
		}
		if got := strings.TrimSpace(rec.Body.String()); got != want {
			t.Errorf("GET %s = %s, want %s", target, got, want)
		}
	}
}

func TestTraceJSONShape(t *testing.T) {
	f := newFixture()
	start := now.Add(-time.Minute)
	f.telemetry.trace = clickhouse.Trace{
		TraceID: "0123456789abcdef0123456789abcdef", Start: start, End: start.Add(time.Second),
		Spans: []clickhouse.Span{
			{SpanID: "aaaaaaaaaaaaaaaa", Service: "shop", Name: "GET /orders", Kind: "server", StartTime: start, Duration: time.Second, Status: "unset"},
			{SpanID: "bbbbbbbbbbbbbbbb", ParentSpanID: "aaaaaaaaaaaaaaaa", Service: "shop", Name: "SELECT", Kind: "client",
				StartTime: start, Duration: 900 * time.Millisecond, Status: "error",
				Events: []clickhouse.SpanEvent{{Time: start, Name: "exception"}}},
		},
	}
	rec := f.do(t, request{target: "/api/v1/traces/0123456789ABCDEF0123456789ABCDEF", token: goodToken})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (body %s)", rec.Code, rec.Body)
	}
	got := decodeBody[traceJSON](t, rec)
	if got.Spans[0].ParentSpanID != nil {
		t.Error("root span parent_span_id should be null")
	}
	if p := got.Spans[1].ParentSpanID; p == nil || *p != "aaaaaaaaaaaaaaaa" {
		t.Errorf("child parent_span_id = %v", p)
	}
	if got.Spans[0].Attributes == nil || got.Spans[0].Events == nil || got.Spans[1].Events[0].Attributes == nil {
		t.Error("attributes and events must be empty values, not null")
	}

	rec = f.do(t, request{target: "/api/v1/traces/ffffffffffffffffffffffffffffffff", token: goodToken})
	if rec.Code != http.StatusNotFound || errorOf(t, rec).Code != "not_found" {
		t.Errorf("unknown trace = %d, want 404 not_found", rec.Code)
	}
}

func TestBackendFailuresMapToStatusCodes(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		target string
		status int
		code   string
	}{
		{"storage timeout", fmt.Errorf("%w: slow", backend.ErrTimeout), "/api/v1/traces", http.StatusGatewayTimeout, "query_timeout"},
		{"storage down", fmt.Errorf("%w: refused", backend.ErrUnavailable), "/api/v1/logs", http.StatusServiceUnavailable, "storage_unavailable"},
		{"metrics down", fmt.Errorf("%w: refused", backend.ErrUnavailable), "/api/v1/services", http.StatusServiceUnavailable, "metrics_unavailable"},
		{"metrics timeout", fmt.Errorf("%w: slow", backend.ErrTimeout), "/api/v1/services/checkout/metrics", http.StatusGatewayTimeout, "query_timeout"},
		{"bad cursor", clickhouse.ErrInvalidCursor, "/api/v1/traces", http.StatusBadRequest, "invalid_argument"},
		{"unexpected", errors.New("clickhouse: Code: 62. Syntax error near SELECT"), "/api/v1/traces", http.StatusInternalServerError, "internal"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture()
			f.telemetry.err, f.metrics.err = tt.err, tt.err
			rec := f.do(t, request{target: tt.target, token: goodToken})
			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d", rec.Code, tt.status)
			}
			body := errorOf(t, rec)
			if body.Code != tt.code {
				t.Errorf("code = %q, want %q", body.Code, tt.code)
			}
			if strings.Contains(body.Message, "Syntax") || strings.Contains(body.Message, "refused") {
				t.Errorf("message leaks internals: %q", body.Message)
			}
		})
	}
}

func TestServiceMetrics(t *testing.T) {
	f := newFixture()
	f.metrics.exists = false
	rec := f.do(t, request{target: "/api/v1/services/unknown/metrics", token: goodToken})
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown service = %d, want 404", rec.Code)
	}

	f.metrics.exists = true
	v := 1.5
	f.metrics.metrics = prometheus.ServiceMetrics{Service: "checkout", Step: time.Minute,
		RequestRate: []prometheus.Point{{Time: now, Value: &v}, {Time: now.Add(time.Minute)}}}
	rec = f.do(t, request{target: "/api/v1/services/checkout/metrics?from=2026-10-30T06:00:00Z", token: goodToken})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (body %s)", rec.Code, rec.Body)
	}
	if f.metrics.step != 2*time.Minute {
		t.Errorf("automatic step for 6h = %v, want 2m (about 240 points)", f.metrics.step)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"v":1.5`) || !strings.Contains(body, `"v":null`) || !strings.Contains(body, `"error_rate":[]`) {
		t.Errorf("series JSON = %s", body)
	}
}

func TestPanicsBecome500(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	a := &api{logger: logger}
	h := a.recoverPanics(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusInternalServerError || errorOf(t, rec).Code != "internal" {
		t.Errorf("status = %d, want 500 internal", rec.Code)
	}
}

func TestClientIPUsesTheProxysEntry(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "172.18.0.5:41000"
	if got := clientIP(r); got != "172.18.0.5" {
		t.Errorf("without a proxy header: %q", got)
	}
	// A client-forged entry comes first; nginx appends the real peer.
	r.Header.Set("X-Forwarded-For", "1.1.1.1, 198.51.100.7")
	if got := clientIP(r); got != "198.51.100.7" {
		t.Errorf("clientIP = %q, want the entry nginx appended", got)
	}
}
