// Command api serves InfraScope's query and configuration API.
//
// Usage:
//
//	api              run the server
//	api healthcheck  probe the local /healthz endpoint and exit 0 or 1
//
// The healthcheck subcommand exists because the container image is
// distroless: there is no shell or curl for a container healthcheck to use.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/exaring/otelpgx"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"infrascope/api/internal/auth"
	"infrascope/api/internal/clickhouse"
	"infrascope/api/internal/config"
	"infrascope/api/internal/health"
	"infrascope/api/internal/httpapi"
	"infrascope/api/internal/otelsetup"
	"infrascope/api/internal/postgres"
	"infrascope/api/internal/prometheus"
)

const (
	serviceVersion = "0.3.0"
	// bcryptCost is the password hashing work factor (about 250 ms per hash).
	bcryptCost = 12
	// maxSpansPerTrace caps one trace's response; larger traces are truncated.
	maxSpansPerTrace = 10_000
	// metricsRetention matches Prometheus's retention in telemetry/prometheus.yaml.
	metricsRetention = 7 * 24 * time.Hour
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck(os.Getenv("INFRASCOPE_HTTP_ADDR")))
	}
	if err := run(); err != nil {
		slog.Error("api stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	shutdownTelemetry, err := otelsetup.Setup(ctx, "infrascope-api", serviceVersion)
	if err != nil {
		return err
	}
	defer func() {
		// ctx is already cancelled here; flush with a fresh deadline.
		flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := shutdownTelemetry(flushCtx); err != nil {
			slog.Error("flushing telemetry", "error", err)
		}
	}()
	logger := otelsetup.Logger("infrascope/api")
	slog.SetDefault(logger)

	db, err := postgres.Open(ctx, cfg.DatabaseURL, postgres.Options{
		ConnectTimeout: cfg.DependencyTimeout,
		Tracer:         otelpgx.NewTracer(),
	})
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		return err
	}

	authService, err := auth.NewService(db, cfg.SessionTTL, bcryptCost)
	if err != nil {
		return err
	}
	outcome, err := authService.Bootstrap(ctx, cfg.AdminUser, cfg.AdminPassword)
	if err != nil {
		return fmt.Errorf("admin account: %w", err)
	}
	logger.Info("admin account ready", "user", cfg.AdminUser, "outcome", outcome)

	traced := func(rt http.RoundTripper) http.RoundTripper { return otelhttp.NewTransport(rt) }
	telemetryStore := clickhouse.New(clickhouse.Config{
		URL:          cfg.ClickHouseURL,
		User:         cfg.ClickHouseUser,
		Password:     cfg.ClickHousePassword,
		DialTimeout:  cfg.DependencyTimeout,
		QueryTimeout: cfg.QueryTimeout,
		Wrap:         traced,
	})
	metricStore := prometheus.New(prometheus.Config{
		URL:          cfg.PrometheusURL,
		DialTimeout:  cfg.DependencyTimeout,
		QueryTimeout: cfg.QueryTimeout,
		Wrap:         traced,
	})

	// Health probes reach each dependency directly and untraced, so they
	// stay truthful (and quiet) when the telemetry pipeline is the problem.
	probeClient := &http.Client{}
	checker := health.NewChecker(cfg.DependencyTimeout, logger,
		health.Dependency{Name: "postgres", Required: true, Probe: db.Ping},
		health.Dependency{Name: "clickhouse", Probe: health.HTTPProbe(probeClient, cfg.ClickHouseURL+"/ping")},
		health.Dependency{Name: "clickhouse_schema", Probe: telemetryStore.CheckSchema},
		health.Dependency{Name: "prometheus", Probe: health.HTTPProbe(probeClient, cfg.PrometheusURL+"/-/ready")},
		health.Dependency{Name: "otel-collector", Probe: health.HTTPProbe(probeClient, cfg.CollectorHealthURL+"/")},
	)

	router := httpapi.NewRouter(httpapi.Deps{
		Health:    checker,
		Auth:      authService,
		Limiter:   auth.NewLimiter(5, 12*time.Second),
		Telemetry: telemetryStore,
		Metrics:   metricStore,
		Logger:    logger,
		Settings: httpapi.Settings{
			MaxSearchRange:  cfg.MaxSearchRange,
			MaxMetricsRange: metricsRetention,
			MaxSpans:        maxSpansPerTrace,
			CookieSecure:    cfg.CookieSecure,
		},
	})
	handler := otelhttp.NewHandler(router, "infrascope-api",
		otelhttp.WithFilter(func(r *http.Request) bool {
			switch r.URL.Path {
			case "/healthz", "/readyz", "/api/v1/status":
				return false // polled constantly; tracing them is noise
			}
			return true
		}))

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      cfg.QueryTimeout + 15*time.Second,
		IdleTimeout:       60 * time.Second,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", cfg.HTTPAddr)
		serveErr <- srv.ListenAndServe()
	}()

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
	}

	logger.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	if err := <-serveErr; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// healthcheck probes the server's own liveness endpoint and returns an exit code.
func healthcheck(addr string) int {
	if addr == "" {
		addr = config.DefaultHTTPAddr
	}
	url, err := loopbackURL(addr, "/healthz")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintln(os.Stderr, "unexpected status:", resp.Status)
		return 1
	}
	return 0
}

// loopbackURL turns a listen address such as ":8081" or "0.0.0.0:8081" into a
// URL on the loopback interface.
func loopbackURL(addr, path string) (string, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("invalid listen address %q: %w", addr, err)
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + path, nil
}
