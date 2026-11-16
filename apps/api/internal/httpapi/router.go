// Package httpapi defines the API's HTTP routes and response conventions.
// apps/api/openapi.yaml is the contract these routes implement.
package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"infrascope/api/internal/auth"
	"infrascope/api/internal/clickhouse"
	"infrascope/api/internal/deployradar"
	"infrascope/api/internal/health"
	"infrascope/api/internal/prometheus"
)

// Authenticator logs users in and checks their sessions (*auth.Service).
type Authenticator interface {
	Login(ctx context.Context, username, password string) (string, time.Time, error)
	Authenticate(ctx context.Context, token string) (auth.Session, error)
	Logout(ctx context.Context, token string) error
}

// TelemetryStore searches traces and logs (*clickhouse.Client).
type TelemetryStore interface {
	SearchTraces(ctx context.Context, q clickhouse.TraceQuery) (clickhouse.TracePage, error)
	GetTrace(ctx context.Context, traceID string, maxSpans int) (clickhouse.Trace, error)
	SearchLogs(ctx context.Context, q clickhouse.LogQuery) (clickhouse.LogPage, error)
	InspectSchema(ctx context.Context) (clickhouse.SchemaReport, error)
}

// MetricStore reads service metrics (*prometheus.Client).
type MetricStore interface {
	Services(ctx context.Context, from, to time.Time) ([]prometheus.ServiceSummary, error)
	ServiceMetrics(ctx context.Context, service string, from, to time.Time, step time.Duration) (prometheus.ServiceMetrics, error)
	ServiceExists(ctx context.Context, service string, at time.Time, lookback time.Duration) (bool, error)
}

// Settings are the request limits and cookie policy.
type Settings struct {
	MaxSearchRange  time.Duration // longest trace or log search window
	MaxMetricsRange time.Duration // longest metrics window (Prometheus retention)
	MaxSpans        int           // spans returned per trace before truncating
	CookieSecure    bool          // mark the session cookie Secure (HTTPS only)

	// WebhookSecret authenticates GitHub deployment webhooks (FR-9). Empty
	// turns the webhook route off.
	WebhookSecret string
	// DeployWindows places the before/after comparison around a deployment.
	DeployWindows deployradar.Windows
}

// Deps are everything the router needs.
type Deps struct {
	Health      *health.Checker
	Auth        Authenticator
	Limiter     *auth.Limiter
	Telemetry   TelemetryStore
	Metrics     MetricStore
	Deployments deployradar.Store // nil turns DeployRadar's routes off
	Logger      *slog.Logger
	Settings    Settings
	Now         func() time.Time // defaults to time.Now
}

type api struct {
	health      *health.Checker
	auth        Authenticator
	limiter     *auth.Limiter
	telemetry   TelemetryStore
	metrics     MetricStore
	deployments deployradar.Store
	logger      *slog.Logger
	settings    Settings
	now         func() time.Time
}

// NewRouter returns the API's HTTP handler.
func NewRouter(d Deps) http.Handler {
	a := &api{
		health:      d.Health,
		auth:        d.Auth,
		limiter:     d.Limiter,
		telemetry:   d.Telemetry,
		metrics:     d.Metrics,
		deployments: d.Deployments,
		logger:      d.Logger,
		settings:    d.Settings,
		now:         d.Now,
	}
	if a.now == nil {
		a.now = time.Now
	}

	mux := http.NewServeMux()

	// Public: health checks, the dashboard's status banner, and login.
	mux.HandleFunc("GET /healthz", a.liveness)
	mux.HandleFunc("GET /readyz", a.readiness)
	mux.HandleFunc("GET /api/v1/status", a.status)
	mux.HandleFunc("POST /api/v1/auth/login", a.login)
	// Public too, but authenticated by its HMAC signature (FR-9).
	mux.HandleFunc("POST /api/v1/webhooks/github", a.githubWebhook)

	// Everything else needs a session.
	mux.Handle("POST /api/v1/auth/logout", a.session(a.logout))
	mux.Handle("GET /api/v1/auth/me", a.session(a.me))
	mux.Handle("GET /api/v1/services", a.session(a.listServices))
	mux.Handle("GET /api/v1/services/{service}/metrics", a.session(a.serviceMetrics))
	mux.Handle("GET /api/v1/traces", a.session(a.searchTraces))
	mux.Handle("GET /api/v1/traces/{traceId}", a.session(a.getTrace))
	mux.Handle("GET /api/v1/logs", a.session(a.searchLogs))
	mux.Handle("GET /api/v1/diagnostics/schema", a.session(a.schemaDiagnostics))
	if a.deployments != nil {
		mux.Handle("GET /api/v1/deployments", a.session(a.listDeployments))
		mux.Handle("GET /api/v1/deployments/{deploymentId}", a.session(a.getDeployment))
	}
	// Alerting arrives in Phase 5.
	mux.Handle("/api/v1/alert-rules", a.session(notImplemented))
	mux.Handle("/api/v1/alert-rules/", a.session(notImplemented))
	mux.Handle("/api/v1/alerts", a.session(notImplemented))
	// Unknown API routes also need a session, so they reveal nothing.
	mux.Handle("/api/", a.session(notFound))
	mux.HandleFunc("/", notFound)

	return a.recoverPanics(mux)
}

// liveness reports that the process is serving. It deliberately checks no
// dependencies, so a database outage never gets the API restarted.
func (a *api) liveness(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": health.StatusOK})
}

// readiness answers 503 when a required dependency is down, for orchestrators.
func (a *api) readiness(w http.ResponseWriter, r *http.Request) {
	report := a.health.Check(r.Context())
	code := http.StatusOK
	if report.Status == health.StatusUnavailable {
		code = http.StatusServiceUnavailable
	}
	writeJSON(w, code, report)
}

// status always answers 200 so the dashboard can show which dependencies are
// down instead of failing to load the status itself.
func (a *api) status(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.health.Check(r.Context()))
}

func notImplemented(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusNotImplemented, "not_implemented",
		"This endpoint is part of the API contract but has not been built yet. See docs/SPEC.md, section 12.")
}

func notFound(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusNotFound, "not_found", "No such route.")
}

// recoverPanics answers 500 instead of dropping the connection when a
// handler panics, and logs the stack.
func (a *api) recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			v := recover()
			if v == nil {
				return
			}
			if v == http.ErrAbortHandler {
				panic(v)
			}
			a.logger.ErrorContext(r.Context(), "handler panicked", "panic", v, "stack", string(debug.Stack()))
			writeError(w, http.StatusInternalServerError, "internal", "Something went wrong. The error has been logged.")
		}()
		next.ServeHTTP(w, r)
	})
}
