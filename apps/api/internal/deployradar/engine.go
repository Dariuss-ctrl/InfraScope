package deployradar

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"infrascope/api/internal/backend"
)

// Engine computes queued comparisons. It runs inside the API as a goroutine
// (the pattern ADR-0005 set for background work) and is safe to run in
// several API instances at once, because claims are row locks.
type Engine struct {
	Store      Store
	Metrics    Metrics
	Thresholds Thresholds
	Logger     *slog.Logger
	Now        func() time.Time // defaults to time.Now

	// Interval is how often due comparisons are looked for (default 15 s).
	Interval time.Duration
	// Grace is how long after a window closes the engine waits, so the last
	// metric export of the window has reached Prometheus (default 30 s).
	Grace time.Duration
	// Lease is how long a claim lasts (default 2 min); MaxAttempts is how
	// many times a comparison is tried before it is marked failed (default
	// 5); Batch is how many are claimed per tick (default 10).
	Lease       time.Duration
	MaxAttempts int
	Batch       int
}

func (e *Engine) defaults() {
	if e.Now == nil {
		e.Now = time.Now
	}
	if e.Logger == nil {
		e.Logger = slog.Default()
	}
	if e.Interval <= 0 {
		e.Interval = 15 * time.Second
	}
	if e.Grace <= 0 {
		e.Grace = 30 * time.Second
	}
	if e.Lease <= 0 {
		e.Lease = 2 * time.Minute
	}
	if e.MaxAttempts <= 0 {
		e.MaxAttempts = 5
	}
	if e.Batch <= 0 {
		e.Batch = 10
	}
}

// Run ticks until ctx is cancelled.
func (e *Engine) Run(ctx context.Context) {
	e.defaults()
	e.Tick(ctx)
	ticker := time.NewTicker(e.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			e.Tick(ctx)
		}
	}
}

// Tick claims due comparisons and computes them. It returns how many it
// claimed. A storage error ends the tick; the rows stay claimed until their
// lease passes, then another tick takes them.
func (e *Engine) Tick(ctx context.Context) int {
	e.defaults()
	now := e.Now()
	claimed, err := e.Store.ClaimComparisons(ctx, ClaimOptions{
		Now: now, Grace: e.Grace, Lease: e.Lease, MaxAttempts: e.MaxAttempts, Limit: e.Batch,
	})
	if err != nil {
		e.Logger.WarnContext(ctx, "deployradar: claiming comparisons", "error", err)
		return 0
	}
	for _, c := range claimed {
		outcome := e.Compare(ctx, c)
		if err := e.Store.FinishComparison(ctx, c.ID, outcome); err != nil {
			e.Logger.WarnContext(ctx, "deployradar: storing comparison", "comparison", c.ID, "error", err)
			continue
		}
		attrs := []any{"comparison", c.ID, "deployment", c.DeploymentID,
			"service", c.Deployment.Service, "environment", c.Deployment.Environment, "attempt", c.Attempts}
		switch outcome.Status {
		case ComparisonDone:
			e.Logger.InfoContext(ctx, "deployradar: comparison done",
				append(attrs, "verdict", outcome.Result.Verdict, "confidence", outcome.Result.Confidence)...)
		case ComparisonPending:
			e.Logger.WarnContext(ctx, "deployradar: comparison will be retried", append(attrs, "error", outcome.Error)...)
		default:
			e.Logger.ErrorContext(ctx, "deployradar: comparison failed", append(attrs, "error", outcome.Error)...)
		}
	}
	return len(claimed)
}

// Compare measures both windows and judges them. A dependency outage is
// retried on a later tick until MaxAttempts; any other error (for example a
// query Prometheus rejects) fails the comparison at once, because retrying
// would not change it.
func (e *Engine) Compare(ctx context.Context, c ClaimedComparison) ComparisonOutcome {
	e.defaults()
	now := e.Now()
	service, environment := c.Deployment.Service, c.Deployment.Environment

	// Other deployments of the service inside the windows, so the verdict can
	// say when a window measured more than this release.
	others, err := e.Store.SuccessesBetween(ctx, service, environment, c.DeploymentID, c.Before.From, c.After.To)
	var before, after WindowStats
	if err == nil {
		before, err = e.Metrics.WindowStats(ctx, service, environment, c.Before)
	}
	if err == nil {
		after, err = e.Metrics.WindowStats(ctx, service, environment, c.After)
	}
	if err != nil {
		message := fmt.Sprintf("attempt %d: %v", c.Attempts, err)
		transient := errors.Is(err, backend.ErrUnavailable) || errors.Is(err, backend.ErrTimeout)
		if transient && c.Attempts < e.MaxAttempts {
			return ComparisonOutcome{Status: ComparisonPending, Error: message, At: now}
		}
		return ComparisonOutcome{Status: ComparisonFailed, Error: message, At: now}
	}

	j := Judge(before.Sample(), after.Sample(), Overlaps(c.Comparison, others), e.Thresholds)
	result := &Result{
		BeforeRequests:   zeroIfNil(before.Requests.Value),
		BeforeErrors:     zeroIfNil(before.Errors.Value),
		AfterRequests:    zeroIfNil(after.Requests.Value),
		AfterErrors:      zeroIfNil(after.Errors.Value),
		BeforeP95:        before.P95.Value,
		AfterP95:         after.P95.Value,
		ErrorRateVerdict: j.ErrorRate,
		P95Verdict:       j.P95,
		Verdict:          j.Overall,
		Confidence:       j.Confidence,
		Reasons:          j.Reasons,
		Queries: map[string]Query{
			"before_requests": before.Requests.Query,
			"before_errors":   before.Errors.Query,
			"before_p95":      before.P95.Query,
			"after_requests":  after.Requests.Query,
			"after_errors":    after.Errors.Query,
			"after_p95":       after.P95.Query,
		},
		MethodVersion: MethodVersion,
		ComputedAt:    now,
	}
	return ComparisonOutcome{Status: ComparisonDone, Result: result, At: now}
}
