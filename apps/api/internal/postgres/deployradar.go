package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"infrascope/api/internal/deployradar"
)

// The DeployRadar store (deployradar.Store). Metric aggregates are never
// written here: only deployment facts, their status history and the values a
// comparison measured, with the queries behind them.

// RecordDelivery implements deployradar.Store. The delivery is stored before
// anything else happens to it, so a duplicate delivery ID is recognised even
// when the first attempt failed half way through.
func (db *DB) RecordDelivery(ctx context.Context, d deployradar.Delivery) (deployradar.Receipt, error) {
	if err := d.Validate(); err != nil {
		return deployradar.Receipt{}, err
	}
	var (
		r     deployradar.Receipt
		prior string
	)
	// The outer SELECT runs on the statement's snapshot, so it does not see a
	// row the CTE inserted: a new delivery reports the column default.
	err := db.pool.QueryRow(ctx, `
		WITH ins AS (
			INSERT INTO webhook_deliveries (delivery_id, event, payload, received_at)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (delivery_id) DO NOTHING
			RETURNING delivery_id
		)
		SELECT EXISTS (SELECT 1 FROM ins),
		       COALESCE((SELECT outcome FROM webhook_deliveries WHERE delivery_id = $1), 'pending')`,
		d.ID, d.Event, d.Payload, d.ReceivedAt,
	).Scan(&r.New, &prior)
	r.PriorOutcome = deployradar.Outcome(prior)
	return r, classify(err)
}

// FinishDelivery implements deployradar.Store.
func (db *DB) FinishDelivery(ctx context.Context, deliveryID string, outcome deployradar.Outcome, message string, at time.Time) error {
	tag, err := db.pool.Exec(ctx, `
		UPDATE webhook_deliveries
		SET outcome = $2, error = NULLIF($3, ''), processed_at = $4
		WHERE delivery_id = $1`,
		deliveryID, string(outcome), message, at)
	if err == nil && tag.RowsAffected() == 0 {
		err = pgx.ErrNoRows
	}
	return classify(err)
}

// ApplyStatus implements deployradar.Store. Everything a status update does
// commits together, and concurrent deliveries for one deployment take turns
// on its row lock, so the precedence rule sees every earlier status.
func (db *DB) ApplyStatus(ctx context.Context, u deployradar.StatusUpdate, w deployradar.Windows, now time.Time) (deployradar.ApplyResult, error) {
	if err := u.Validate(); err != nil {
		return deployradar.ApplyResult{}, err
	}
	if err := w.Validate(); err != nil {
		return deployradar.ApplyResult{}, err
	}
	var finishedAt *time.Time
	if u.State.Terminal() {
		t := u.StateAt
		finishedAt = &t
	}

	var res deployradar.ApplyResult
	err := pgx.BeginFunc(ctx, db.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			INSERT INTO deployments (provider, repository, provider_deployment_id, service, environment,
			                         ref, sha, creator, description, created_at,
			                         state, state_at, finished_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
			ON CONFLICT (provider, provider_deployment_id) DO NOTHING`,
			u.Provider, u.Repository, u.ProviderDeploymentID, u.Service, u.Environment,
			u.Ref, u.SHA, u.Creator, u.Description, u.CreatedAt,
			string(u.State), u.StateAt, finishedAt, now)
		if err != nil {
			return err
		}
		inserted := tag.RowsAffected() == 1

		// Lock the row. A concurrent transaction that inserted it is waited
		// for, and the re-read after the lock sees its committed state.
		var (
			id        int64
			current   string
			currentAt time.Time
		)
		if err := tx.QueryRow(ctx, `
			SELECT id, state, state_at FROM deployments
			WHERE provider = $1 AND provider_deployment_id = $2
			FOR UPDATE`, u.Provider, u.ProviderDeploymentID,
		).Scan(&id, &current, &currentAt); err != nil {
			return err
		}

		applied := inserted || deployradar.Supersedes(deployradar.State(current), currentAt, u.State, u.StateAt)
		if applied && !inserted {
			if _, err := tx.Exec(ctx, `
				UPDATE deployments
				SET state = $2, state_at = $3, finished_at = COALESCE(finished_at, $4), updated_at = $5
				WHERE id = $1`, id, string(u.State), u.StateAt, finishedAt, now); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO deployment_changes (deployment_id, delivery_id, state, state_at, applied, recorded_at)
			VALUES ($1, $2, $3, $4, $5, $6)`,
			id, u.DeliveryID, string(u.State), u.StateAt, applied, now); err != nil {
			return err
		}

		queued := false
		if applied && u.State == deployradar.StateSuccess {
			before, after := w.Around(u.StateAt)
			tag, err := tx.Exec(ctx, `
				INSERT INTO deployment_comparisons (deployment_id, before_from, before_to, after_from, after_to, created_at, updated_at)
				VALUES ($1, $2, $3, $4, $5, $6, $6)
				ON CONFLICT (deployment_id) DO NOTHING`,
				id, before.From, before.To, after.From, after.To, now)
			if err != nil {
				return err
			}
			queued = tag.RowsAffected() == 1
		}

		if _, err := tx.Exec(ctx, `
			UPDATE webhook_deliveries SET outcome = 'applied', error = NULL, processed_at = $2
			WHERE delivery_id = $1`, u.DeliveryID, now); err != nil {
			return err
		}

		d, err := scanDeployment(tx.QueryRow(ctx, selectDeployment+` WHERE id = $1`, id))
		if err != nil {
			return err
		}
		res = deployradar.ApplyResult{Deployment: d, Applied: applied, ComparisonQueued: queued}
		return nil
	})
	return res, classify(err)
}

// ListDeployments implements deployradar.Store. Newest first, by record ID,
// which is also the paging key. Each row carries its comparison's status and
// verdict from one join, so a list never needs a query per deployment.
func (db *DB) ListDeployments(ctx context.Context, f deployradar.DeploymentFilter) ([]deployradar.DeploymentSummary, error) {
	var (
		where []string
		args  []any
	)
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, fmt.Sprintf(cond, len(args)))
	}
	if f.Service != "" {
		add("d.service = $%d", f.Service)
	}
	if f.Environment != "" {
		add("d.environment = $%d", f.Environment)
	}
	if !f.From.IsZero() {
		add("d.created_at >= $%d", f.From)
	}
	if !f.To.IsZero() {
		add("d.created_at < $%d", f.To)
	}
	if f.BeforeID > 0 {
		add("d.id < $%d", f.BeforeID)
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	args = append(args, limit)

	q := `SELECT ` + qualify("d", deploymentColumns) + `, c.status, c.verdict, c.confidence
		FROM deployments d LEFT JOIN deployment_comparisons c ON c.deployment_id = d.id`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += fmt.Sprintf(" ORDER BY d.id DESC LIMIT $%d", len(args))

	rows, err := db.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, classify(err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (deployradar.DeploymentSummary, error) {
		var (
			dr              deploymentRow
			status, verdict *string
			confidence      *float64
		)
		if err := row.Scan(append(dr.dests(), &status, &verdict, &confidence)...); err != nil {
			return deployradar.DeploymentSummary{}, err
		}
		s := deployradar.DeploymentSummary{Deployment: dr.value(), Confidence: confidence}
		if status != nil {
			cs := deployradar.ComparisonStatus(*status)
			s.ComparisonStatus = &cs
		}
		if verdict != nil {
			v := deployradar.Verdict(*verdict)
			s.Verdict = &v
		}
		return s, nil
	})
	if err != nil {
		return nil, classify(err)
	}
	if out == nil {
		out = []deployradar.DeploymentSummary{}
	}
	return out, nil
}

// Deployment implements deployradar.Store.
func (db *DB) Deployment(ctx context.Context, id int64) (deployradar.DeploymentDetail, error) {
	var det deployradar.DeploymentDetail
	err := pgx.BeginTxFunc(ctx, db.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		d, err := scanDeployment(tx.QueryRow(ctx, selectDeployment+` WHERE id = $1`, id))
		if err != nil {
			return err
		}
		det.Deployment = d

		rows, err := tx.Query(ctx, `
			SELECT id, deployment_id, delivery_id, state, state_at, applied, recorded_at
			FROM deployment_changes WHERE deployment_id = $1
			ORDER BY state_at, id`, id)
		if err != nil {
			return err
		}
		det.Changes, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (deployradar.Change, error) {
			var (
				c     deployradar.Change
				state string
			)
			err := row.Scan(&c.ID, &c.DeploymentID, &c.DeliveryID, &state, &c.StateAt, &c.Applied, &c.RecordedAt)
			c.State = deployradar.State(state)
			return c, err
		})
		if err != nil {
			return err
		}
		if det.Changes == nil {
			det.Changes = []deployradar.Change{}
		}

		var cr comparisonRow
		err = tx.QueryRow(ctx, `SELECT `+comparisonColumns+` FROM deployment_comparisons WHERE deployment_id = $1`, id).
			Scan(cr.dests()...)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return nil
		case err != nil:
			return err
		}
		c := cr.value()
		det.Comparison = &c
		return nil
	})
	return det, classify(err)
}

// ClaimComparisons implements deployradar.Store. Due rows are locked with
// SKIP LOCKED, so workers in several API instances take different rows, and
// the claim is recorded on the row so a crashed worker's rows free up after
// the lease.
func (db *DB) ClaimComparisons(ctx context.Context, o deployradar.ClaimOptions) ([]deployradar.ClaimedComparison, error) {
	if o.Limit <= 0 || o.MaxAttempts <= 0 || o.Lease <= 0 || o.Grace < 0 || o.Now.IsZero() {
		return nil, errors.New("claim options need a positive limit, max attempts and lease, a non-negative grace and a time")
	}
	rows, err := db.pool.Query(ctx, `
		WITH due AS (
			SELECT id FROM deployment_comparisons
			WHERE status = 'pending' AND after_to <= $5 AND attempts < $2
			  AND (claimed_at IS NULL OR claimed_at <= $3)
			ORDER BY after_to
			LIMIT $4
			FOR UPDATE SKIP LOCKED
		), claimed AS (
			UPDATE deployment_comparisons c
			SET attempts = c.attempts + 1, claimed_at = $1, updated_at = $1
			FROM due WHERE c.id = due.id
			RETURNING `+qualify("c", comparisonColumns)+`
		)
		SELECT `+qualify("claimed", comparisonColumns)+`, `+qualify("d", deploymentColumns)+`
		FROM claimed JOIN deployments d ON d.id = claimed.deployment_id
		ORDER BY claimed.after_to`,
		o.Now, o.MaxAttempts, o.Now.Add(-o.Lease), o.Limit, o.Now.Add(-o.Grace))
	if err != nil {
		return nil, classify(err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (deployradar.ClaimedComparison, error) {
		var (
			cr comparisonRow
			dr deploymentRow
		)
		if err := row.Scan(append(cr.dests(), dr.dests()...)...); err != nil {
			return deployradar.ClaimedComparison{}, err
		}
		return deployradar.ClaimedComparison{Comparison: cr.value(), Deployment: dr.value()}, nil
	})
	return out, classify(err)
}

// FinishComparison implements deployradar.Store.
func (db *DB) FinishComparison(ctx context.Context, id int64, o deployradar.ComparisonOutcome) error {
	if err := o.Validate(); err != nil {
		return err
	}
	var err error
	if o.Result == nil {
		_, err = db.pool.Exec(ctx, `
			UPDATE deployment_comparisons
			SET status = $2, error = $3, updated_at = $4
			WHERE id = $1`, id, string(o.Status), o.Error, o.At)
	} else {
		r := o.Result
		reasons := r.Reasons
		if reasons == nil {
			reasons = []string{}
		}
		queries := r.Queries
		if queries == nil {
			queries = map[string]deployradar.Query{}
		}
		routes := r.Routes
		if routes == nil {
			routes = []deployradar.RouteStats{}
		}
		_, err = db.pool.Exec(ctx, `
			UPDATE deployment_comparisons
			SET status = 'done', error = NULL, updated_at = $2,
			    before_requests = $3, before_errors = $4, after_requests = $5, after_errors = $6,
			    before_p95_seconds = $7, after_p95_seconds = $8,
			    error_rate_verdict = $9, p95_verdict = $10, verdict = $11, confidence = $12,
			    reasons = $13, queries = $14, method_version = $15, computed_at = $16, routes = $17
			WHERE id = $1`,
			id, o.At,
			r.BeforeRequests, r.BeforeErrors, r.AfterRequests, r.AfterErrors,
			r.BeforeP95, r.AfterP95,
			string(r.ErrorRateVerdict), string(r.P95Verdict), string(r.Verdict), r.Confidence,
			reasons, queries, r.MethodVersion, r.ComputedAt, routes)
	}
	return classify(err)
}

// SuccessesBetween implements deployradar.Store. A comparison's before window
// ends at its deployment's first success, so before_to is that moment, and
// only deployments that ever succeeded have a comparison.
func (db *DB) SuccessesBetween(ctx context.Context, service, environment string, exclude int64, from, to time.Time) ([]deployradar.Success, error) {
	rows, err := db.pool.Query(ctx, `
		SELECT d.id, c.before_to
		FROM deployment_comparisons c JOIN deployments d ON d.id = c.deployment_id
		WHERE d.service = $1 AND d.environment = $2 AND d.id <> $3
		  AND c.before_to > $4 AND c.before_to < $5
		ORDER BY c.before_to, d.id`, service, environment, exclude, from, to)
	if err != nil {
		return nil, classify(err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (deployradar.Success, error) {
		var s deployradar.Success
		err := row.Scan(&s.DeploymentID, &s.At)
		return s, err
	})
	return out, classify(err)
}

const deploymentColumns = `id, provider, repository, provider_deployment_id, service, environment,
	ref, sha, creator, description, created_at, state, state_at, finished_at, updated_at`

const selectDeployment = `SELECT ` + deploymentColumns + ` FROM deployments`

const comparisonColumns = `id, deployment_id, status, attempts, claimed_at,
	before_from, before_to, after_from, after_to,
	before_requests, before_errors, after_requests, after_errors,
	before_p95_seconds, after_p95_seconds,
	error_rate_verdict, p95_verdict, verdict, confidence, reasons, queries,
	method_version, error, computed_at, created_at, updated_at, routes`

// qualify prefixes every column in a comma-separated list with an alias.
func qualify(alias, columns string) string {
	parts := strings.Split(columns, ",")
	for i, p := range parts {
		parts[i] = alias + "." + strings.TrimSpace(p)
	}
	return strings.Join(parts, ", ")
}

type scanner interface {
	Scan(dest ...any) error
}

// deploymentRow receives the deploymentColumns of one row.
type deploymentRow struct {
	d     deployradar.Deployment
	state string
}

func (r *deploymentRow) dests() []any {
	d := &r.d
	return []any{&d.ID, &d.Provider, &d.Repository, &d.ProviderDeploymentID, &d.Service, &d.Environment,
		&d.Ref, &d.SHA, &d.Creator, &d.Description, &d.CreatedAt, &r.state, &d.StateAt, &d.FinishedAt, &d.UpdatedAt}
}

func (r *deploymentRow) value() deployradar.Deployment {
	r.d.State = deployradar.State(r.state)
	return r.d
}

func scanDeployment(s scanner) (deployradar.Deployment, error) {
	var r deploymentRow
	if err := s.Scan(r.dests()...); err != nil {
		return deployradar.Deployment{}, err
	}
	return r.value(), nil
}

// comparisonRow receives the comparisonColumns of one row. Result columns
// are nullable until the comparison is done.
type comparisonRow struct {
	c                                                 deployradar.Comparison
	status                                            string
	beforeRequests, beforeErrors                      *float64
	afterRequests, afterErrors                        *float64
	beforeP95, afterP95, confidence                   *float64
	errorRateVerdict, p95Verdict, verdict, errMessage *string
	reasons                                           []string
	queries                                           map[string]deployradar.Query
	routes                                            []deployradar.RouteStats
	methodVersion                                     int
	computedAt                                        *time.Time
}

func (r *comparisonRow) dests() []any {
	c := &r.c
	return []any{&c.ID, &c.DeploymentID, &r.status, &c.Attempts, &c.ClaimedAt,
		&c.Before.From, &c.Before.To, &c.After.From, &c.After.To,
		&r.beforeRequests, &r.beforeErrors, &r.afterRequests, &r.afterErrors,
		&r.beforeP95, &r.afterP95,
		&r.errorRateVerdict, &r.p95Verdict, &r.verdict, &r.confidence, &r.reasons, &r.queries,
		&r.methodVersion, &r.errMessage, &r.computedAt, &c.CreatedAt, &c.UpdatedAt, &r.routes}
}

func (r *comparisonRow) value() deployradar.Comparison {
	c := r.c
	c.Status = deployradar.ComparisonStatus(r.status)
	if r.errMessage != nil {
		c.Error = *r.errMessage
	}
	if c.Status == deployradar.ComparisonDone && r.computedAt != nil {
		reasons := r.reasons
		if reasons == nil {
			reasons = []string{}
		}
		queries := r.queries
		if queries == nil {
			queries = map[string]deployradar.Query{}
		}
		routes := r.routes
		if routes == nil {
			routes = []deployradar.RouteStats{}
		}
		c.Result = &deployradar.Result{
			BeforeRequests:   zeroIfNil(r.beforeRequests),
			BeforeErrors:     zeroIfNil(r.beforeErrors),
			AfterRequests:    zeroIfNil(r.afterRequests),
			AfterErrors:      zeroIfNil(r.afterErrors),
			BeforeP95:        r.beforeP95,
			AfterP95:         r.afterP95,
			ErrorRateVerdict: deployradar.Verdict(emptyIfNil(r.errorRateVerdict)),
			P95Verdict:       deployradar.Verdict(emptyIfNil(r.p95Verdict)),
			Verdict:          deployradar.Verdict(emptyIfNil(r.verdict)),
			Confidence:       zeroIfNil(r.confidence),
			Reasons:          reasons,
			Queries:          queries,
			Routes:           routes,
			MethodVersion:    r.methodVersion,
			ComputedAt:       *r.computedAt,
		}
	}
	return c
}

func zeroIfNil(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

func emptyIfNil(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
