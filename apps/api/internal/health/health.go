// Package health probes the API's dependencies and summarises their state.
//
// Probes reach each dependency directly instead of going through the
// telemetry pipeline, so the report stays truthful when that pipeline is the
// thing that is broken.
package health

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"
)

// Overall report states.
const (
	StatusOK          = "ok"          // every dependency is up
	StatusDegraded    = "degraded"    // an optional dependency is down
	StatusUnavailable = "unavailable" // a required dependency is down
)

// Dependency states.
const (
	StatusUp   = "up"
	StatusDown = "down"
)

// Probe reports whether a dependency is reachable. It should honour ctx; the
// Checker enforces the timeout even when it does not.
type Probe func(ctx context.Context) error

// Dependency is a service the API talks to.
type Dependency struct {
	Name string
	// Required dependencies make the API unready when they are down. Optional
	// ones only disable the features that use them (NFR-1).
	Required bool
	Probe    Probe
}

// DependencyStatus is one dependency's entry in a Report.
type DependencyStatus struct {
	Name      string `json:"name"`
	Status    string `json:"status"`
	Required  bool   `json:"required"`
	LatencyMS int64  `json:"latency_ms"`
}

// Report summarises a round of probes. It is served without authentication,
// so it carries no error details; those go to the log.
type Report struct {
	Status       string             `json:"status"`
	CheckedAt    time.Time          `json:"checked_at"`
	Dependencies []DependencyStatus `json:"dependencies"`
}

// Checker probes dependencies concurrently, each bounded by the same timeout.
type Checker struct {
	deps    []Dependency
	timeout time.Duration
	logger  *slog.Logger
}

// NewChecker returns a Checker for deps. Reports list dependencies in the
// order given.
func NewChecker(timeout time.Duration, logger *slog.Logger, deps ...Dependency) *Checker {
	return &Checker{deps: deps, timeout: timeout, logger: logger}
}

// Check probes every dependency and returns the summary. It returns within
// roughly one timeout regardless of how many dependencies are slow.
func (c *Checker) Check(ctx context.Context) Report {
	results := make([]DependencyStatus, len(c.deps))
	var wg sync.WaitGroup
	for i, dep := range c.deps {
		wg.Go(func() {
			results[i] = c.probe(ctx, dep)
		})
	}
	wg.Wait()

	return Report{
		Status:       summarise(results),
		CheckedAt:    time.Now().UTC(),
		Dependencies: results,
	}
}

func (c *Checker) probe(ctx context.Context, dep Dependency) DependencyStatus {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	start := time.Now()
	// Run the probe separately so that one which ignores ctx still cannot
	// hold the report past the timeout.
	done := make(chan error, 1)
	go func() { done <- dep.Probe(ctx) }()

	var err error
	select {
	case err = <-done:
	case <-ctx.Done():
		err = ctx.Err()
	}
	latency := time.Since(start)

	status := DependencyStatus{
		Name:      dep.Name,
		Status:    StatusUp,
		Required:  dep.Required,
		LatencyMS: latency.Milliseconds(),
	}
	if err != nil {
		status.Status = StatusDown
		c.logger.Warn("dependency check failed",
			"dependency", dep.Name, "required", dep.Required, "latency", latency, "error", err)
	}
	return status
}

func summarise(results []DependencyStatus) string {
	overall := StatusOK
	for _, r := range results {
		if r.Status == StatusUp {
			continue
		}
		if r.Required {
			return StatusUnavailable
		}
		overall = StatusDegraded
	}
	return overall
}

// HTTPProbe returns a probe that expects a 2xx response from a GET to url.
func HTTPProbe(client *http.Client, url string) Probe {
	return func(ctx context.Context) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		// Drain a bounded amount so the connection can be reused.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			return fmt.Errorf("unexpected status %d", resp.StatusCode)
		}
		return nil
	}
}

// TCPProbe returns a probe that opens and closes a TCP connection to addr.
// It stands in for a PostgreSQL ping until the API gains a database driver
// in Phase 2.
func TCPProbe(addr string) Probe {
	return func(ctx context.Context) error {
		var d net.Dialer
		conn, err := d.DialContext(ctx, "tcp", addr)
		if err != nil {
			return err
		}
		return conn.Close()
	}
}
