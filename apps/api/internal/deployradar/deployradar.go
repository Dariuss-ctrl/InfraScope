// Package deployradar links deployments to changes in service health
// (SPEC FR-9). Deployments arrive as provider webhooks and become records in
// PostgreSQL. After each successful one, the comparison engine measures the
// service's error rate and p95 latency before and after it in Prometheus,
// and stores a verdict together with the evidence behind it.
//
// This file holds the domain types and the two pure rules: which status
// supersedes which, and where the comparison windows fall.
package deployradar

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// ProviderGitHub is the only deployment provider so far.
const ProviderGitHub = "github"

// State is a deployment's status, using GitHub's names.
type State string

// The states GitHub sends in deployment_status events.
const (
	StatePending    State = "pending"
	StateQueued     State = "queued"
	StateInProgress State = "in_progress"
	StateSuccess    State = "success"
	StateFailure    State = "failure"
	StateError      State = "error"
	// StateInactive is sent when a newer deployment to the same environment
	// supersedes this one. It is recorded but never un-finishes a deployment.
	StateInactive State = "inactive"
)

// Valid reports whether s is a state GitHub can send.
func (s State) Valid() bool {
	switch s {
	case StatePending, StateQueued, StateInProgress, StateSuccess, StateFailure, StateError, StateInactive:
		return true
	}
	return false
}

// Terminal reports whether s is a final outcome: the deployment is over.
func (s State) Terminal() bool {
	return s == StateSuccess || s == StateFailure || s == StateError
}

// Supersedes reports whether a status (next, at nextAt) replaces the current
// one. Webhooks arrive out of order and get redelivered, so the rule depends
// on the statuses and their own times, never on arrival order:
//   - a terminal state is never replaced by a non-terminal one, so a late
//     in_progress cannot un-finish a deployment;
//   - a terminal state always replaces a non-terminal one;
//   - between two terminal or two non-terminal states, the later status time
//     wins. Equal times favour next, which only matters for exact replays.
func Supersedes(current State, currentAt time.Time, next State, nextAt time.Time) bool {
	switch {
	case current.Terminal() && !next.Terminal():
		return false
	case next.Terminal() && !current.Terminal():
		return true
	}
	return !nextAt.Before(currentAt)
}

// Length limits, mirrored by the CHECK constraints in migration 0002.
const (
	maxDeliveryID   = 128
	maxEvent        = 64
	maxRepository   = 256
	maxProviderID   = 64
	maxLabelValue   = 128 // service and environment: Prometheus label values
	maxRef          = 256
	maxCreator      = 128
	maxDescription  = 1024
	maxReasonLength = 512
)

var shaPattern = regexp.MustCompile(`^[0-9a-f]{7,64}$`)

// Facts are what a deployment is, independent of its status.
type Facts struct {
	Provider             string `json:"provider"`
	Repository           string `json:"repository"` // owner/name
	ProviderDeploymentID string `json:"provider_deployment_id"`
	// Service and Environment are the values of the Prometheus labels
	// service_name and deployment_environment_name, which is how the
	// deployment is matched to its metrics.
	Service     string    `json:"service"`
	Environment string    `json:"environment"`
	Ref         string    `json:"ref"`
	SHA         string    `json:"sha"`
	Creator     string    `json:"creator"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"` // when the provider created the deployment
}

// Validate checks the facts against the limits the database enforces, so a
// bad payload is refused with a reason instead of a constraint error.
func (f Facts) Validate() error {
	var errs []error
	if f.Provider != ProviderGitHub {
		errs = append(errs, fmt.Errorf("provider %q is not supported", f.Provider))
	}
	name := func(field, value string, limit int) {
		if strings.TrimSpace(value) == "" || len(value) > limit {
			errs = append(errs, fmt.Errorf("%s must be 1 to %d characters", field, limit))
		}
	}
	name("repository", f.Repository, maxRepository)
	name("providerDeploymentId", f.ProviderDeploymentID, maxProviderID)
	name("service", f.Service, maxLabelValue)
	name("environment", f.Environment, maxLabelValue)
	name("ref", f.Ref, maxRef)
	if !shaPattern.MatchString(f.SHA) {
		errs = append(errs, errors.New("sha must be 7 to 64 lowercase hex characters"))
	}
	if len(f.Creator) > maxCreator {
		errs = append(errs, fmt.Errorf("creator must be at most %d characters", maxCreator))
	}
	if len(f.Description) > maxDescription {
		errs = append(errs, fmt.Errorf("description must be at most %d characters", maxDescription))
	}
	if f.CreatedAt.IsZero() {
		errs = append(errs, errors.New("createdAt is required"))
	}
	return errors.Join(errs...)
}

// Deployment is a stored deployment with its current state.
type Deployment struct {
	ID int64 `json:"id"`
	Facts
	State   State     `json:"state"`
	StateAt time.Time `json:"state_at"`
	// FinishedAt is when the first terminal status happened; nil while the
	// deployment is still running.
	FinishedAt *time.Time `json:"finished_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// StatusUpdate is what one deployment_status delivery says.
type StatusUpdate struct {
	DeliveryID string
	Facts
	State   State
	StateAt time.Time
}

// Validate checks the update the way Facts.Validate checks the facts.
func (u StatusUpdate) Validate() error {
	var errs []error
	if strings.TrimSpace(u.DeliveryID) == "" || len(u.DeliveryID) > maxDeliveryID {
		errs = append(errs, fmt.Errorf("deliveryId must be 1 to %d characters", maxDeliveryID))
	}
	if err := u.Facts.Validate(); err != nil {
		errs = append(errs, err)
	}
	if !u.State.Valid() {
		errs = append(errs, fmt.Errorf("state %q is not a deployment status", u.State))
	}
	if u.StateAt.IsZero() {
		errs = append(errs, errors.New("stateAt is required"))
	}
	return errors.Join(errs...)
}

// Change is one delivered status in a deployment's history.
type Change struct {
	ID           int64     `json:"id"`
	DeploymentID int64     `json:"deployment_id"`
	DeliveryID   string    `json:"delivery_id"`
	State        State     `json:"state"`
	StateAt      time.Time `json:"state_at"`
	// Applied is false when the precedence rule kept the existing state.
	Applied    bool      `json:"applied"`
	RecordedAt time.Time `json:"recorded_at"`
}

// ApplyResult says what a status update did.
type ApplyResult struct {
	Deployment Deployment
	// Applied is false when the update was recorded but did not change the
	// state, for example a late in_progress after success.
	Applied bool
	// ComparisonQueued is true when this update made the deployment
	// successful for the first time, which queues its comparison.
	ComparisonQueued bool
}

// Outcome is what processing a delivery led to.
type Outcome string

// Delivery outcomes.
const (
	OutcomePending Outcome = "pending" // stored, not processed yet
	OutcomeApplied Outcome = "applied" // changed or recorded a deployment
	OutcomeIgnored Outcome = "ignored" // an event DeployRadar does not use
	OutcomeFailed  Outcome = "failed"  // processing failed; may be redelivered
)

// Delivery is one verified webhook request.
type Delivery struct {
	ID         string
	Event      string
	Payload    json.RawMessage
	ReceivedAt time.Time
}

// Validate checks the delivery against the database limits.
func (d Delivery) Validate() error {
	var errs []error
	if strings.TrimSpace(d.ID) == "" || len(d.ID) > maxDeliveryID {
		errs = append(errs, fmt.Errorf("delivery ID must be 1 to %d characters", maxDeliveryID))
	}
	if strings.TrimSpace(d.Event) == "" || len(d.Event) > maxEvent {
		errs = append(errs, fmt.Errorf("event must be 1 to %d characters", maxEvent))
	}
	if !json.Valid(d.Payload) {
		errs = append(errs, errors.New("payload is not valid JSON"))
	}
	if d.ReceivedAt.IsZero() {
		errs = append(errs, errors.New("receivedAt is required"))
	}
	return errors.Join(errs...)
}

// Receipt is the result of storing a delivery.
type Receipt struct {
	// New is false when a delivery with this ID was stored before.
	New bool
	// PriorOutcome is the earlier delivery's outcome when New is false. A
	// failed one may be processed again, so GitHub's redeliver button works.
	PriorOutcome Outcome
}

// Windows places the comparison windows around a successful deployment.
type Windows struct {
	// Width is how long each window is.
	Width time.Duration
	// Settle is the gap left after the deployment before the after window
	// starts, so the restart (and its counter resets) is not measured.
	Settle time.Duration
}

// Validate checks that the windows are usable.
func (w Windows) Validate() error {
	if w.Width <= 0 {
		return errors.New("comparison window width must be positive")
	}
	if w.Settle < 0 {
		return errors.New("comparison settle time cannot be negative")
	}
	return nil
}

// Around returns the before and after windows for a deployment that
// succeeded at t: before = [t−Width, t), after = [t+Settle, t+Settle+Width).
func (w Windows) Around(t time.Time) (before, after Window) {
	before = Window{From: t.Add(-w.Width), To: t}
	after = Window{From: t.Add(w.Settle), To: t.Add(w.Settle + w.Width)}
	return before, after
}

// Window is a half-open time range [From, To).
type Window struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
}

// ComparisonStatus is where a comparison is in its life.
type ComparisonStatus string

// Comparison statuses.
const (
	ComparisonPending ComparisonStatus = "pending" // queued, or retrying
	ComparisonDone    ComparisonStatus = "done"
	ComparisonFailed  ComparisonStatus = "failed" // gave up after repeated errors
)

// Verdict is what a comparison concluded, per signal and overall.
type Verdict string

// Verdicts, from worst to best. insufficient_data is a conclusion in its own
// right: the engine never guesses on low traffic.
const (
	VerdictRegression       Verdict = "regression"
	VerdictInsufficientData Verdict = "insufficient_data"
	VerdictNoChange         Verdict = "no_change"
	VerdictImprovement      Verdict = "improvement"
)

// Query is the exact Prometheus query behind a stored value.
type Query struct {
	PromQL string    `json:"promql"`
	Time   time.Time `json:"time"`  // evaluation time of the instant query
	Range  string    `json:"range"` // the range selector, for example "10m"
}

// Result is a finished comparison's evidence and conclusion.
type Result struct {
	BeforeRequests float64 `json:"before_requests"`
	BeforeErrors   float64 `json:"before_errors"`
	AfterRequests  float64 `json:"after_requests"`
	AfterErrors    float64 `json:"after_errors"`
	// P95 latencies in seconds; nil when the window had no histogram data.
	BeforeP95 *float64 `json:"before_p95_seconds"`
	AfterP95  *float64 `json:"after_p95_seconds"`

	ErrorRateVerdict Verdict `json:"error_rate_verdict"`
	P95Verdict       Verdict `json:"p95_verdict"`
	Verdict          Verdict `json:"verdict"` // the two signals combined (see Judge)
	// Confidence is in [0, 1]; see ADR-0007 for what it means per signal.
	Confidence float64  `json:"confidence"`
	Reasons    []string `json:"reasons"`
	// Queries is keyed by what each measured: before_requests, after_p95, ...
	Queries       map[string]Query `json:"queries"`
	MethodVersion int              `json:"method_version"`
	ComputedAt    time.Time        `json:"computed_at"`
}

// Validate checks a result before it is stored.
func (r Result) Validate() error {
	var errs []error
	for _, v := range []Verdict{r.ErrorRateVerdict, r.P95Verdict, r.Verdict} {
		if !v.Valid() {
			errs = append(errs, fmt.Errorf("verdict %q is not valid", v))
		}
	}
	if r.Confidence < 0 || r.Confidence > 1 {
		errs = append(errs, fmt.Errorf("confidence %v is outside [0, 1]", r.Confidence))
	}
	for _, reason := range r.Reasons {
		if len(reason) > maxReasonLength {
			errs = append(errs, fmt.Errorf("reason longer than %d characters", maxReasonLength))
		}
	}
	if r.ComputedAt.IsZero() {
		errs = append(errs, errors.New("computedAt is required"))
	}
	return errors.Join(errs...)
}

// Valid reports whether v is a known verdict.
func (v Verdict) Valid() bool {
	switch v {
	case VerdictRegression, VerdictInsufficientData, VerdictNoChange, VerdictImprovement:
		return true
	}
	return false
}

// ErrorRate returns errors/requests, or 0 when there were no requests.
func ErrorRate(errors, requests float64) float64 {
	if requests <= 0 {
		return 0
	}
	return errors / requests
}

// Comparison is a deployment's before/after comparison, in any status.
type Comparison struct {
	ID           int64            `json:"id"`
	DeploymentID int64            `json:"deployment_id"`
	Status       ComparisonStatus `json:"status"`
	Attempts     int              `json:"attempts"`
	ClaimedAt    *time.Time       `json:"-"`
	Before       Window           `json:"before"`
	After        Window           `json:"after"`
	// Result is set once Status is done.
	Result *Result `json:"result"`
	// Error is the last failure, kept while retrying and after giving up.
	Error     string    `json:"error,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ComparisonOutcome is what one attempt at a comparison produced.
type ComparisonOutcome struct {
	// Status is done with a Result, failed to give up, or pending to retry.
	Status ComparisonStatus
	Result *Result
	Error  string
	At     time.Time
}

// Validate checks that the outcome is consistent.
func (o ComparisonOutcome) Validate() error {
	switch o.Status {
	case ComparisonDone:
		if o.Result == nil {
			return errors.New("a done comparison needs a result")
		}
		return o.Result.Validate()
	case ComparisonFailed, ComparisonPending:
		if o.Result != nil {
			return fmt.Errorf("a %s comparison cannot carry a result", o.Status)
		}
		if strings.TrimSpace(o.Error) == "" {
			return fmt.Errorf("a %s comparison needs an error", o.Status)
		}
		return nil
	}
	return fmt.Errorf("comparison status %q is not valid", o.Status)
}

// ClaimedComparison is a pending comparison a worker has taken, with the
// deployment it measures.
type ClaimedComparison struct {
	Comparison
	Deployment Deployment
}

// ClaimOptions configure Store.ClaimComparisons.
type ClaimOptions struct {
	Now time.Time
	// Grace holds back a comparison whose after window closed less than
	// this long ago, so the window's last metric export has reached
	// Prometheus before it is measured.
	Grace time.Duration
	// Lease is how long a claim keeps other workers away. A row claimed
	// longer ago than this is taken to belong to a crashed worker.
	Lease time.Duration
	// MaxAttempts stops a row being claimed again once it has been tried
	// this many times. The worker marks such a row failed.
	MaxAttempts int
	Limit       int
}

// DeploymentFilter selects deployments to list.
type DeploymentFilter struct {
	Service     string
	Environment string
	From, To    time.Time // on the deployment's createdAt; zero means open
	// BeforeID continues a previous page: only deployments with a smaller ID.
	BeforeID int64
	Limit    int
}

// DeploymentDetail is a deployment with its history and comparison.
type DeploymentDetail struct {
	Deployment
	Changes    []Change    `json:"changes"`
	Comparison *Comparison `json:"comparison"`
}

// Success is when a deployment first succeeded.
type Success struct {
	DeploymentID int64
	At           time.Time
}

// DeploymentSummary is a deployment as listed: its facts and state, and
// where its comparison stands, so a list can show verdicts without asking
// for each deployment's detail.
type DeploymentSummary struct {
	Deployment
	// ComparisonStatus is nil when no comparison was queued, because the
	// deployment never succeeded.
	ComparisonStatus *ComparisonStatus `json:"comparison_status"`
	// Verdict and Confidence are set once the comparison is done.
	Verdict    *Verdict `json:"verdict"`
	Confidence *float64 `json:"confidence"`
}

// Store is the deployment storage (*postgres.DB). Errors are the backend
// failure kinds, so handlers map them to the usual envelope.
type Store interface {
	// RecordDelivery stores a verified delivery before it is processed.
	RecordDelivery(ctx context.Context, d Delivery) (Receipt, error)
	// FinishDelivery records how processing a delivery ended.
	FinishDelivery(ctx context.Context, deliveryID string, outcome Outcome, message string, at time.Time) error
	// ApplyStatus upserts the deployment, appends the change, applies the
	// precedence rule, queues the comparison on the first success and marks
	// the delivery applied, all in one transaction. The delivery must have
	// been recorded first.
	ApplyStatus(ctx context.Context, u StatusUpdate, w Windows, now time.Time) (ApplyResult, error)

	ListDeployments(ctx context.Context, f DeploymentFilter) ([]DeploymentSummary, error)
	Deployment(ctx context.Context, id int64) (DeploymentDetail, error)

	// ClaimComparisons takes due pending comparisons for this worker, so
	// several API instances never compute the same one at once.
	ClaimComparisons(ctx context.Context, o ClaimOptions) ([]ClaimedComparison, error)
	FinishComparison(ctx context.Context, id int64, o ComparisonOutcome) error
	// SuccessesBetween lists the other deployments of a service and
	// environment that first succeeded strictly between from and to, so a
	// comparison can say when its windows measured more than one release.
	SuccessesBetween(ctx context.Context, service, environment string, exclude int64, from, to time.Time) ([]Success, error)
}
