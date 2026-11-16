package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"infrascope/api/internal/auth"
	"infrascope/api/internal/backend"
	"infrascope/api/internal/deployradar"
	"infrascope/api/internal/github"
	"infrascope/api/internal/health"
)

const webhookSecret = "test-webhook-secret-0123456789"

// fakeDeployments is an in-memory deployradar.Store that records calls.
type fakeDeployments struct {
	mu         sync.Mutex
	err        error // returned by every method when set
	deliveries map[string]*fakeDelivery
	updates    []deployradar.StatusUpdate
	windows    deployradar.Windows
	byProvider map[string]*deployradar.Deployment
	nextID     int64

	// For the read endpoints.
	list       []deployradar.DeploymentSummary
	listFilter deployradar.DeploymentFilter
	details    map[int64]deployradar.DeploymentDetail
}

type fakeDelivery struct {
	deployradar.Delivery
	outcome deployradar.Outcome
	message string
}

func newFakeDeployments() *fakeDeployments {
	return &fakeDeployments{deliveries: map[string]*fakeDelivery{}, byProvider: map[string]*deployradar.Deployment{}}
}

func (f *fakeDeployments) RecordDelivery(_ context.Context, d deployradar.Delivery) (deployradar.Receipt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return deployradar.Receipt{}, f.err
	}
	if err := d.Validate(); err != nil {
		return deployradar.Receipt{}, err
	}
	if prior, ok := f.deliveries[d.ID]; ok {
		return deployradar.Receipt{New: false, PriorOutcome: prior.outcome}, nil
	}
	f.deliveries[d.ID] = &fakeDelivery{Delivery: d, outcome: deployradar.OutcomePending}
	return deployradar.Receipt{New: true, PriorOutcome: deployradar.OutcomePending}, nil
}

func (f *fakeDeployments) FinishDelivery(_ context.Context, id string, outcome deployradar.Outcome, message string, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	d, ok := f.deliveries[id]
	if !ok {
		return backend.ErrNotFound
	}
	d.outcome, d.message = outcome, message
	return nil
}

func (f *fakeDeployments) ApplyStatus(_ context.Context, u deployradar.StatusUpdate, w deployradar.Windows, now time.Time) (deployradar.ApplyResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return deployradar.ApplyResult{}, f.err
	}
	if err := u.Validate(); err != nil {
		return deployradar.ApplyResult{}, err
	}
	f.updates = append(f.updates, u)
	f.windows = w
	key := u.Provider + "/" + u.ProviderDeploymentID
	d, exists := f.byProvider[key]
	applied := true
	if !exists {
		f.nextID++
		d = &deployradar.Deployment{ID: f.nextID, Facts: u.Facts, State: u.State, StateAt: u.StateAt, UpdatedAt: now}
		f.byProvider[key] = d
	} else {
		applied = deployradar.Supersedes(d.State, d.StateAt, u.State, u.StateAt)
		if applied {
			d.State, d.StateAt, d.UpdatedAt = u.State, u.StateAt, now
		}
	}
	queued := false
	if applied && u.State.Terminal() && d.FinishedAt == nil {
		t := u.StateAt
		d.FinishedAt = &t
		queued = u.State == deployradar.StateSuccess
	}
	if del, ok := f.deliveries[u.DeliveryID]; ok {
		del.outcome = deployradar.OutcomeApplied
	}
	return deployradar.ApplyResult{Deployment: *d, Applied: applied, ComparisonQueued: queued}, nil
}

// ListDeployments returns f.list, newest first as given, after the filter's
// cursor and up to its limit, and records the filter.
func (f *fakeDeployments) ListDeployments(_ context.Context, filter deployradar.DeploymentFilter) ([]deployradar.DeploymentSummary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listFilter = filter
	if f.err != nil {
		return nil, f.err
	}
	out := []deployradar.DeploymentSummary{}
	for _, d := range f.list {
		if filter.BeforeID > 0 && d.ID >= filter.BeforeID {
			continue
		}
		if filter.Limit > 0 && len(out) == filter.Limit {
			break
		}
		out = append(out, d)
	}
	return out, nil
}

func (f *fakeDeployments) Deployment(_ context.Context, id int64) (deployradar.DeploymentDetail, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return deployradar.DeploymentDetail{}, f.err
	}
	d, ok := f.details[id]
	if !ok {
		return deployradar.DeploymentDetail{}, backend.ErrNotFound
	}
	return d, nil
}

func (f *fakeDeployments) ClaimComparisons(context.Context, deployradar.ClaimOptions) ([]deployradar.ClaimedComparison, error) {
	return nil, f.err
}

func (f *fakeDeployments) FinishComparison(context.Context, int64, deployradar.ComparisonOutcome) error {
	return f.err
}

func (f *fakeDeployments) SuccessesBetween(context.Context, string, string, int64, time.Time, time.Time) ([]deployradar.Success, error) {
	return nil, f.err
}

func (f *fakeDeployments) delivery(id string) *fakeDelivery {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.deliveries[id]
}

func newWebhookFixture(secret string) (*fakeDeployments, *fixture) {
	store := newFakeDeployments()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	f := &fixture{auth: &fakeAuth{}, telemetry: &fakeTelemetry{}, metrics: &fakeMetrics{}}
	f.handler = NewRouter(Deps{
		Health:      health.NewChecker(time.Second, logger),
		Auth:        f.auth,
		Limiter:     auth.NewLimiter(3, time.Minute),
		Telemetry:   f.telemetry,
		Metrics:     f.metrics,
		Deployments: store,
		Logger:      logger,
		Settings: Settings{
			MaxSearchRange: 24 * time.Hour, MaxMetricsRange: 7 * 24 * time.Hour, MaxSpans: 100,
			WebhookSecret: secret,
			DeployWindows: deployradar.Windows{Width: 3 * time.Minute, Settle: time.Minute},
		},
		Now: func() time.Time { return now },
	})
	return store, f
}

const statusPayload = `{
  "action": "created",
  "deployment_status": {"id": 2000, "state": "%s", "environment": "local", "created_at": "%s"},
  "deployment": {"id": 1000, "sha": "0123456789abcdef0123456789abcdef01234567", "ref": "main",
                 "payload": {"service": "demo-service"}, "environment": "local",
                 "created_at": "2026-10-30T11:50:00Z", "creator": {"login": "octocat"}},
  "repository": {"name": "shop", "full_name": "acme/shop"}
}`

func statusBody(state string, at time.Time) string {
	return strings.Replace(strings.Replace(statusPayload, "%s", state, 1), "%s", at.Format(time.RFC3339), 1)
}

// deliver posts a signed webhook.
func deliver(t *testing.T, f *fixture, secret, event, deliveryID, body string) *webhookResponse {
	t.Helper()
	headers := map[string]string{
		"Content-Type":         "application/json",
		github.HeaderEvent:     event,
		github.HeaderDelivery:  deliveryID,
		github.HeaderSignature: github.Sign(secret, []byte(body)),
	}
	rec := f.do(t, request{method: http.MethodPost, target: "/api/v1/webhooks/github", body: body, header: headers})
	return &webhookResponse{t: t, code: rec.Code, body: rec.Body.String()}
}

type webhookResponse struct {
	t    *testing.T
	code int
	body string
}

func (r *webhookResponse) receipt() webhookReceipt {
	r.t.Helper()
	if r.code != http.StatusOK {
		r.t.Fatalf("status = %d, body %s", r.code, r.body)
	}
	var w webhookReceipt
	if err := json.Unmarshal([]byte(r.body), &w); err != nil {
		r.t.Fatalf("decode receipt: %v", err)
	}
	return w
}

func TestWebhookIsOffWithoutASecret(t *testing.T) {
	_, f := newWebhookFixture("")
	res := deliver(t, f, "anything", github.EventPing, "d-1", `{}`)
	if res.code != http.StatusServiceUnavailable || !strings.Contains(res.body, "webhooks_disabled") {
		t.Errorf("status = %d, body %s; want 503 webhooks_disabled", res.code, res.body)
	}
}

func TestWebhookNeedsNoSessionButAValidSignature(t *testing.T) {
	store, f := newWebhookFixture(webhookSecret)
	body := `{"zen":"Keep it logically awesome."}`

	// Wrong secret: refused and not stored.
	res := deliver(t, f, "not-the-secret", github.EventPing, "d-bad", body)
	if res.code != http.StatusUnauthorized || !strings.Contains(res.body, "invalid_signature") {
		t.Errorf("wrong secret: status = %d, body %s", res.code, res.body)
	}
	if store.delivery("d-bad") != nil {
		t.Error("a delivery with a bad signature was stored")
	}

	// Right secret, no cookie: accepted.
	receipt := deliver(t, f, webhookSecret, github.EventPing, "d-ping", body).receipt()
	if receipt.Outcome != "ignored" || receipt.Event != "ping" || receipt.DeliveryID != "d-ping" {
		t.Errorf("ping receipt = %+v", receipt)
	}
	if d := store.delivery("d-ping"); d == nil || d.outcome != deployradar.OutcomeIgnored {
		t.Errorf("ping delivery = %+v, want stored as ignored", d)
	}
}

func TestWebhookRequiresTheGitHubHeaders(t *testing.T) {
	_, f := newWebhookFixture(webhookSecret)
	rec := f.do(t, request{method: http.MethodPost, target: "/api/v1/webhooks/github", body: `{}`,
		header: map[string]string{"Content-Type": "application/json"}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	e := errorOf(t, rec)
	if e.Code != "invalid_argument" || len(e.Fields) != 3 {
		t.Errorf("error = %+v, want invalid_argument naming the three headers", e)
	}
}

func TestWebhookRecordsDeploymentStatuses(t *testing.T) {
	store, f := newWebhookFixture(webhookSecret)
	t0 := now.Add(-10 * time.Minute)

	r1 := deliver(t, f, webhookSecret, github.EventDeploymentStatus, "d-1", statusBody("in_progress", t0)).receipt()
	if r1.Outcome != "recorded" || r1.DeploymentID == nil || *r1.DeploymentID != 1 || r1.Applied == nil || !*r1.Applied || *r1.ComparisonQueued {
		t.Fatalf("in_progress receipt = %+v", r1)
	}
	r2 := deliver(t, f, webhookSecret, github.EventDeploymentStatus, "d-2", statusBody("success", t0.Add(time.Minute))).receipt()
	if r2.Outcome != "recorded" || *r2.DeploymentID != 1 || !*r2.Applied || !*r2.ComparisonQueued {
		t.Fatalf("success receipt = %+v", r2)
	}
	// Late in_progress: recorded, not applied.
	r3 := deliver(t, f, webhookSecret, github.EventDeploymentStatus, "d-3", statusBody("in_progress", t0.Add(30*time.Second))).receipt()
	if r3.Outcome != "recorded" || *r3.Applied || *r3.ComparisonQueued {
		t.Fatalf("late in_progress receipt = %+v", r3)
	}

	if len(store.updates) != 3 {
		t.Fatalf("%d updates applied, want 3", len(store.updates))
	}
	u := store.updates[1]
	if u.DeliveryID != "d-2" || u.Service != "demo-service" || u.Environment != "local" || u.State != deployradar.StateSuccess || u.Repository != "acme/shop" {
		t.Errorf("update = %+v", u)
	}
	if store.windows != (deployradar.Windows{Width: 3 * time.Minute, Settle: time.Minute}) {
		t.Errorf("windows passed = %+v, want the configured ones", store.windows)
	}
	if d := store.delivery("d-2"); d.outcome != deployradar.OutcomeApplied {
		t.Errorf("delivery d-2 outcome = %s, want applied", d.outcome)
	}
}

func TestWebhookDuplicatesAreAnsweredNotReapplied(t *testing.T) {
	store, f := newWebhookFixture(webhookSecret)
	body := statusBody("success", now.Add(-5*time.Minute))

	first := deliver(t, f, webhookSecret, github.EventDeploymentStatus, "d-dup", body).receipt()
	second := deliver(t, f, webhookSecret, github.EventDeploymentStatus, "d-dup", body).receipt()
	if first.Outcome != "recorded" || second.Outcome != "duplicate" || second.DeploymentID != nil {
		t.Errorf("receipts = %+v / %+v", first, second)
	}
	if len(store.updates) != 1 {
		t.Errorf("%d updates applied, want 1", len(store.updates))
	}

	// A failed earlier attempt is retried on redelivery.
	store.deliveries["d-dup"].outcome = deployradar.OutcomeFailed
	third := deliver(t, f, webhookSecret, github.EventDeploymentStatus, "d-dup", body).receipt()
	if third.Outcome != "recorded" || len(store.updates) != 2 {
		t.Errorf("redelivery after failure: receipt %+v, %d updates", third, len(store.updates))
	}
}

func TestWebhookBadPayloadIsRecordedAsFailed(t *testing.T) {
	store, f := newWebhookFixture(webhookSecret)

	res := deliver(t, f, webhookSecret, github.EventDeploymentStatus, "d-nojson", `{"action":"created"}`)
	if res.code != http.StatusBadRequest || !strings.Contains(res.body, "deployment.id") {
		t.Errorf("payload without deployment: status = %d, body %s", res.code, res.body)
	}
	if d := store.delivery("d-nojson"); d == nil || d.outcome != deployradar.OutcomeFailed || d.message == "" {
		t.Errorf("delivery = %+v, want stored as failed with a reason", d)
	}

	// Parses, but fails validation (unknown state).
	res = deliver(t, f, webhookSecret, github.EventDeploymentStatus, "d-state", statusBody("deployed", now))
	if res.code != http.StatusBadRequest || !strings.Contains(res.body, "state") {
		t.Errorf("unknown state: status = %d, body %s", res.code, res.body)
	}
	if len(store.updates) != 0 {
		t.Error("an invalid update reached the store")
	}

	// Not JSON at all, but correctly signed.
	res = deliver(t, f, webhookSecret, github.EventPing, "d-text", `not json`)
	if res.code != http.StatusBadRequest {
		t.Errorf("non-JSON body: status = %d, body %s", res.code, res.body)
	}
	if store.delivery("d-text") != nil {
		t.Error("a non-JSON body was stored")
	}
}

func TestWebhookWhenTheDatabaseIsDown(t *testing.T) {
	store, f := newWebhookFixture(webhookSecret)
	store.err = backend.ErrUnavailable
	res := deliver(t, f, webhookSecret, github.EventDeploymentStatus, "d-down", statusBody("success", now))
	if res.code != http.StatusServiceUnavailable || !strings.Contains(res.body, "database_unavailable") {
		t.Errorf("status = %d, body %s; want 503 database_unavailable", res.code, res.body)
	}
}

func TestWebhookBodyLimit(t *testing.T) {
	_, f := newWebhookFixture(webhookSecret)
	big := `{"pad":"` + strings.Repeat("x", maxWebhookBody) + `"}`
	res := deliver(t, f, webhookSecret, github.EventPing, "d-big", big)
	if res.code != http.StatusRequestEntityTooLarge || !strings.Contains(res.body, "payload_too_large") {
		t.Errorf("status = %d, body %.100s; want 413", res.code, res.body)
	}
}

func TestWebhookOtherEventsAreIgnored(t *testing.T) {
	store, f := newWebhookFixture(webhookSecret)
	receipt := deliver(t, f, webhookSecret, "push", "d-push", `{"ref":"refs/heads/main"}`).receipt()
	if receipt.Outcome != "ignored" || receipt.Event != "push" {
		t.Errorf("receipt = %+v", receipt)
	}
	if d := store.delivery("d-push"); d == nil || d.outcome != deployradar.OutcomeIgnored {
		t.Errorf("delivery = %+v", d)
	}
}
