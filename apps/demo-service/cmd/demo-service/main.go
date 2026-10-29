// Command demo-service is the toy shop that InfraScope observes: an
// OpenTelemetry-instrumented HTTP API over PostgreSQL that can reproduce the
// failure scenarios in docs/SPEC.md, section 9.
//
// Usage:
//
//	demo-service              run the server
//	demo-service healthcheck  probe the local /healthz endpoint and exit 0 or 1
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

	"infrascope/demo-service/internal/config"
	"infrascope/demo-service/internal/shop"
	"infrascope/demo-service/internal/telemetry"
)

const serviceVersion = "0.2.0"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		addr := os.Getenv("DEMO_HTTP_ADDR")
		if addr == "" {
			addr = config.DefaultHTTPAddr
		}
		os.Exit(healthcheck(addr))
	}

	if err := run(); err != nil {
		slog.Error("demo-service stopped", "error", err)
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

	shutdownTelemetry, err := telemetry.Setup(ctx, "demo-service", serviceVersion)
	if err != nil {
		return err
	}
	defer func() {
		// ctx is already cancelled at this point; flush with a fresh deadline.
		flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := shutdownTelemetry(flushCtx); err != nil {
			slog.Error("flushing telemetry", "error", err)
		}
	}()

	logger := telemetry.Logger("infrascope/demo-service")
	slog.SetDefault(logger)

	pool, err := shop.Connect(ctx, cfg.DatabaseURL, cfg.SlowQueryThreshold, logger)
	if err != nil {
		return err
	}
	defer pool.Close()

	store := shop.NewStore(pool)
	if err := store.Migrate(ctx); err != nil {
		return err
	}
	seedStart := time.Now()
	seeded, err := store.Seed(ctx, shop.SeedSize{
		Products:  cfg.SeedProducts,
		Customers: cfg.SeedCustomers,
		Orders:    cfg.SeedOrders,
	})
	if err != nil {
		return err
	}
	if seeded {
		logger.Info("seeded demo data",
			"products", cfg.SeedProducts, "orders", cfg.SeedOrders, "duration", time.Since(seedStart))
	}

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           shop.Instrument(shop.NewHandler(store, logger)),
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

// healthcheck probes the server's own liveness endpoint and returns an exit
// code. The image is distroless, so the binary checks itself.
func healthcheck(addr string) int {
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

// loopbackURL turns a listen address such as ":8082" into a loopback URL.
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
