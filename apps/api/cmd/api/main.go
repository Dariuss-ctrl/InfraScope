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

	"infrascope/api/internal/config"
	"infrascope/api/internal/health"
	"infrascope/api/internal/httpapi"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck(os.Getenv("INFRASCOPE_HTTP_ADDR")))
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.Load()
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(1)
	}
	if err := run(cfg, logger); err != nil {
		logger.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func run(cfg config.Config, logger *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	dbAddr, err := cfg.DatabaseAddr()
	if err != nil {
		return err
	}
	// Each probe is bounded by the checker's timeout through its context.
	client := &http.Client{}
	checker := health.NewChecker(cfg.DependencyTimeout, logger,
		health.Dependency{Name: "postgres", Required: true, Probe: health.TCPProbe(dbAddr)},
		health.Dependency{Name: "clickhouse", Probe: health.HTTPProbe(client, cfg.ClickHouseURL+"/ping")},
		health.Dependency{Name: "prometheus", Probe: health.HTTPProbe(client, cfg.PrometheusURL+"/-/ready")},
		health.Dependency{Name: "otel-collector", Probe: health.HTTPProbe(client, cfg.CollectorHealthURL+"/")},
	)

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           httpapi.NewRouter(checker),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
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
