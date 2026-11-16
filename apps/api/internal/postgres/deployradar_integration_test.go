//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"infrascope/api/internal/backend"
	"infrascope/api/internal/deployradar"
)

// Postgres keeps microseconds, so fixture times are truncated to compare
// with Equal.
func at(base time.Time, offset time.Duration) time.Time {
	return base.Add(offset).Truncate(time.Microsecond).UTC()
}

var demoWindows = deployradar.Windows{Width: 3 * time.Minute, Settle: time.Minute}

type fixture struct {
	t    *testing.T
	db   *DB
	ctx  context.Context
	base time.Time
	n    int
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	db := open(t)
	ctx := context.Background()
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return &fixture{t: t, db: db, ctx: ctx, base: at(time.Now().Add(-time.Hour), 0)}
}

func (f *fixture) facts(deployment string) deployradar.Facts {
	return deployradar.Facts{
		Provider:             deployradar.ProviderGitHub,
		Repository:           "acme/shop",
		ProviderDeploymentID: f.t.Name() + "/" + deployment,
		Service:              "demo-service",
		Environment:          "local",
		Ref:                  "main",
		SHA:                  "0123456789abcdef0123456789abcdef01234567",
		Creator:              "octocat",
		CreatedAt:            f.base,
	}
}

// deliver records a delivery and applies the status it carries.
func (f *fixture) deliver(facts deployradar.Facts, state deployradar.State, stateAt time.Time) deployradar.ApplyResult {
	f.t.Helper()
	f.n++
	id := fmt.Sprintf("%s/%d", f.t.Name(), f.n)
	if _, err := f.db.RecordDelivery(f.ctx, deployradar.Delivery{
		ID: id, Event: "deployment_status", Payload: json.RawMessage(`{}`), ReceivedAt: time.Now(),
	}); err != nil {
		f.t.Fatalf("RecordDelivery(%s): %v", id, err)
	}
	res, err := f.db.ApplyStatus(f.ctx, deployradar.StatusUpdate{
		DeliveryID: id, Facts: facts, State: state, StateAt: stateAt,
	}, demoWindows, time.Now())
	if err != nil {
		f.t.Fatalf("ApplyStatus(%s, %s): %v", id, state, err)
	}
	return res
}

func (f *fixture) detail(id int64) deployradar.DeploymentDetail {
	f.t.Helper()
	det, err := f.db.Deployment(f.ctx, id)
	if err != nil {
		f.t.Fatalf("Deployment(%d): %v", id, err)
	}
	return det
}

func TestDeliveriesAreStoredOnce(t *testing.T) {
	f := newFixture(t)
	d := deployradar.Delivery{
		ID: t.Name(), Event: "ping", Payload: json.RawMessage(`{"zen":"Keep it logically awesome."}`),
		ReceivedAt: time.Now(),
	}

	r, err := f.db.RecordDelivery(f.ctx, d)
	if err != nil || !r.New || r.PriorOutcome != deployradar.OutcomePending {
		t.Fatalf("first RecordDelivery = %+v, %v; want New with pending outcome", r, err)
	}
	r, err = f.db.RecordDelivery(f.ctx, d)
	if err != nil || r.New || r.PriorOutcome != deployradar.OutcomePending {
		t.Fatalf("duplicate RecordDelivery = %+v, %v; want not New, prior pending", r, err)
	}

	if err := f.db.FinishDelivery(f.ctx, d.ID, deployradar.OutcomeFailed, "prometheus unavailable", time.Now()); err != nil {
		t.Fatalf("FinishDelivery: %v", err)
	}
	r, err = f.db.RecordDelivery(f.ctx, d)
	if err != nil || r.New || r.PriorOutcome != deployradar.OutcomeFailed {
		t.Fatalf("redelivery after failure = %+v, %v; want prior failed, so it can be retried", r, err)
	}

	if err := f.db.FinishDelivery(f.ctx, "no-such-delivery", deployradar.OutcomeIgnored, "", time.Now()); !errors.Is(err, backend.ErrNotFound) {
		t.Errorf("FinishDelivery(unknown) = %v, want ErrNotFound", err)
	}
	if _, err := f.db.RecordDelivery(f.ctx, deployradar.Delivery{ID: "", Event: "ping", Payload: json.RawMessage(`{}`), ReceivedAt: time.Now()}); err == nil {
		t.Error("invalid delivery accepted")
	}
}

func TestStatusPrecedenceAndHistory(t *testing.T) {
	f := newFixture(t)
	facts := f.facts("1")
	t0, t1, t2 := at(f.base, time.Minute), at(f.base, 2*time.Minute), at(f.base, 5*time.Minute)

	res := f.deliver(facts, deployradar.StateInProgress, t0)
	if !res.Applied || res.ComparisonQueued || res.Deployment.State != deployradar.StateInProgress || res.Deployment.FinishedAt != nil {
		t.Fatalf("in_progress: %+v", res)
	}
	id := res.Deployment.ID

	res = f.deliver(facts, deployradar.StateSuccess, t1)
	if !res.Applied || !res.ComparisonQueued || res.Deployment.State != deployradar.StateSuccess {
		t.Fatalf("success: %+v", res)
	}
	if res.Deployment.FinishedAt == nil || !res.Deployment.FinishedAt.Equal(t1) {
		t.Fatalf("success: finishedAt = %v, want %v", res.Deployment.FinishedAt, t1)
	}

	// A late in_progress (status time before the success, delivered after it)
	// is recorded but changes nothing.
	res = f.deliver(facts, deployradar.StateInProgress, at(f.base, 90*time.Second))
	if res.Applied || res.Deployment.State != deployradar.StateSuccess || !res.Deployment.StateAt.Equal(t1) {
		t.Fatalf("late in_progress: %+v", res)
	}
	// So is an inactive from a later deployment superseding this one.
	res = f.deliver(facts, deployradar.StateInactive, t2)
	if res.Applied || res.Deployment.State != deployradar.StateSuccess {
		t.Fatalf("inactive: %+v", res)
	}
	// A later terminal status replaces the state but not the first finish
	// time, and does not queue a second comparison.
	res = f.deliver(facts, deployradar.StateFailure, t2)
	if !res.Applied || res.ComparisonQueued || res.Deployment.State != deployradar.StateFailure || !res.Deployment.FinishedAt.Equal(t1) {
		t.Fatalf("later failure: %+v", res)
	}

	det := f.detail(id)
	if det.ID != id || det.State != deployradar.StateFailure {
		t.Fatalf("detail = %+v", det.Deployment)
	}
	wantHistory := []struct {
		state   deployradar.State
		applied bool
	}{
		{deployradar.StateInProgress, true},
		{deployradar.StateInProgress, false},
		{deployradar.StateSuccess, true},
		{deployradar.StateInactive, false},
		{deployradar.StateFailure, true},
	}
	if len(det.Changes) != len(wantHistory) {
		t.Fatalf("history has %d changes, want %d: %+v", len(det.Changes), len(wantHistory), det.Changes)
	}
	for i, want := range wantHistory {
		got := det.Changes[i]
		if got.State != want.state || got.Applied != want.applied || got.DeploymentID != id {
			t.Errorf("history[%d] = %s applied=%v, want %s applied=%v", i, got.State, got.Applied, want.state, want.applied)
		}
	}

	c := det.Comparison
	if c == nil || c.Status != deployradar.ComparisonPending || c.Result != nil || c.Attempts != 0 {
		t.Fatalf("comparison = %+v, want pending without result", c)
	}
	before, after := demoWindows.Around(t1)
	if !c.Before.From.Equal(before.From) || !c.Before.To.Equal(before.To) || !c.After.From.Equal(after.From) || !c.After.To.Equal(after.To) {
		t.Errorf("windows = %+v / %+v, want %+v / %+v around the success at %v", c.Before, c.After, before, after, t1)
	}

	// The deliveries were marked applied by the same transaction.
	var applied int
	if err := f.db.pool.QueryRow(f.ctx,
		`SELECT count(*) FROM webhook_deliveries WHERE delivery_id LIKE $1 AND outcome = 'applied' AND processed_at IS NOT NULL`,
		t.Name()+"/%").Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied != 5 {
		t.Errorf("%d deliveries marked applied, want 5", applied)
	}
}

func TestFirstStatusMayBeTerminal(t *testing.T) {
	f := newFixture(t)
	res := f.deliver(f.facts("1"), deployradar.StateSuccess, at(f.base, time.Minute))
	if !res.Applied || !res.ComparisonQueued || res.Deployment.FinishedAt == nil {
		t.Fatalf("success as first status: %+v", res)
	}
	if det := f.detail(res.Deployment.ID); det.Comparison == nil {
		t.Error("comparison not queued")
	}
}

func TestApplyStatusRejectsBadInput(t *testing.T) {
	f := newFixture(t)
	facts := f.facts("1")
	facts.Service = ""
	_, err := f.db.ApplyStatus(f.ctx, deployradar.StatusUpdate{DeliveryID: "x", Facts: facts, State: deployradar.StateSuccess, StateAt: f.base}, demoWindows, time.Now())
	if err == nil {
		t.Error("empty service accepted")
	}
	_, err = f.db.ApplyStatus(f.ctx, deployradar.StatusUpdate{DeliveryID: "x", Facts: f.facts("2"), State: deployradar.StateSuccess, StateAt: f.base}, deployradar.Windows{}, time.Now())
	if err == nil {
		t.Error("zero windows accepted")
	}
	if _, err := f.db.Deployment(f.ctx, -1); !errors.Is(err, backend.ErrNotFound) {
		t.Errorf("Deployment(-1) = %v, want ErrNotFound", err)
	}
}

func TestConcurrentStatusesConverge(t *testing.T) {
	f := newFixture(t)
	facts := f.facts("1")

	type status struct {
		state deployradar.State
		at    time.Time
	}
	statuses := []status{
		{deployradar.StatePending, at(f.base, 0)},
		{deployradar.StateQueued, at(f.base, 10*time.Second)},
		{deployradar.StateInProgress, at(f.base, 20*time.Second)},
		{deployradar.StateSuccess, at(f.base, 60*time.Second)},
		{deployradar.StateInProgress, at(f.base, 30*time.Second)},
		{deployradar.StateInactive, at(f.base, 5*time.Minute)},
		{deployradar.StateQueued, at(f.base, 15*time.Second)},
		{deployradar.StateInProgress, at(f.base, 25*time.Second)},
	}
	ids := make([]string, len(statuses))
	for i := range statuses {
		ids[i] = fmt.Sprintf("%s/%d", t.Name(), i)
		if _, err := f.db.RecordDelivery(f.ctx, deployradar.Delivery{ID: ids[i], Event: "deployment_status", Payload: json.RawMessage(`{}`), ReceivedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		queued  int
		applied int
		errs    []error
	)
	for i, s := range statuses {
		wg.Go(func() {
			res, err := f.db.ApplyStatus(f.ctx, deployradar.StatusUpdate{DeliveryID: ids[i], Facts: facts, State: s.state, StateAt: s.at}, demoWindows, time.Now())
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				return
			}
			if res.ComparisonQueued {
				queued++
			}
			if res.Applied {
				applied++
			}
		})
	}
	wg.Wait()
	if len(errs) > 0 {
		t.Fatalf("concurrent ApplyStatus errors: %v", errs)
	}
	if queued != 1 {
		t.Errorf("comparison queued %d times, want 1", queued)
	}

	list, err := f.db.ListDeployments(f.ctx, deployradar.DeploymentFilter{Service: facts.Service})
	if err != nil {
		t.Fatal(err)
	}
	var d *deployradar.Deployment
	for i := range list {
		if list[i].ProviderDeploymentID == facts.ProviderDeploymentID {
			d = &list[i].Deployment
		}
	}
	if d == nil {
		t.Fatal("deployment not listed")
	}
	if d.State != deployradar.StateSuccess || !d.StateAt.Equal(at(f.base, 60*time.Second)) {
		t.Errorf("final state = %s at %v, want success at T+60s whatever the arrival order", d.State, d.StateAt)
	}
	det := f.detail(d.ID)
	if len(det.Changes) != len(statuses) {
		t.Errorf("%d changes recorded, want %d", len(det.Changes), len(statuses))
	}
	// The success was applied, and so was whichever subset of the
	// non-terminal statuses arrived before it in increasing time order; the
	// count is run-dependent but never more than the statuses themselves.
	if applied < 1 || applied > len(statuses) {
		t.Errorf("applied = %d", applied)
	}
}

func TestListDeploymentsFilters(t *testing.T) {
	f := newFixture(t)
	shop := f.facts("shop")
	shop.Service = t.Name() + "-shop"
	search := f.facts("search")
	search.Service = t.Name() + "-search"
	staging := f.facts("shop-staging")
	staging.Service = shop.Service
	staging.Environment = "staging"
	staging.CreatedAt = at(f.base, -time.Hour)

	first := f.deliver(shop, deployradar.StateSuccess, at(f.base, time.Minute)).Deployment.ID
	f.deliver(search, deployradar.StateInProgress, at(f.base, time.Minute))
	third := f.deliver(staging, deployradar.StateSuccess, at(f.base, time.Minute)).Deployment.ID

	ids := func(filter deployradar.DeploymentFilter) []int64 {
		t.Helper()
		list, err := f.db.ListDeployments(f.ctx, filter)
		if err != nil {
			t.Fatalf("ListDeployments(%+v): %v", filter, err)
		}
		out := make([]int64, len(list))
		for i, d := range list {
			out[i] = d.ID
		}
		return out
	}

	if got := ids(deployradar.DeploymentFilter{Service: shop.Service}); len(got) != 2 || got[0] != third || got[1] != first {
		t.Errorf("by service = %v, want [%d %d] newest first", got, third, first)
	}
	if got := ids(deployradar.DeploymentFilter{Service: shop.Service, Environment: "staging"}); len(got) != 1 || got[0] != third {
		t.Errorf("by environment = %v, want [%d]", got, third)
	}
	if got := ids(deployradar.DeploymentFilter{Service: shop.Service, From: f.base}); len(got) != 1 || got[0] != first {
		t.Errorf("from = %v, want [%d] (the staging deployment was created an hour earlier)", got, first)
	}
	if got := ids(deployradar.DeploymentFilter{Service: shop.Service, To: f.base}); len(got) != 1 || got[0] != third {
		t.Errorf("to = %v, want [%d]", got, third)
	}
	if got := ids(deployradar.DeploymentFilter{Service: shop.Service, Limit: 1}); len(got) != 1 || got[0] != third {
		t.Errorf("limit 1 = %v, want [%d]", got, third)
	}
	if got := ids(deployradar.DeploymentFilter{Service: shop.Service, BeforeID: third}); len(got) != 1 || got[0] != first {
		t.Errorf("before %d = %v, want [%d]", third, got, first)
	}
	if got := ids(deployradar.DeploymentFilter{Service: "nobody"}); got == nil || len(got) != 0 {
		t.Errorf("unknown service = %v, want an empty, non-nil list", got)
	}

	// Each row says where its comparison stands: queued for the successful
	// deployments, none for the one still in progress.
	list, err := f.db.ListDeployments(f.ctx, deployradar.DeploymentFilter{Service: search.Service})
	if err != nil || len(list) != 1 {
		t.Fatalf("search deployments = %v, %v", list, err)
	}
	if s := list[0]; s.ComparisonStatus != nil || s.Verdict != nil || s.Confidence != nil {
		t.Errorf("in-progress summary = %+v, want no comparison", s)
	}
	list, err = f.db.ListDeployments(f.ctx, deployradar.DeploymentFilter{Service: shop.Service, Limit: 1})
	if err != nil || len(list) != 1 {
		t.Fatalf("shop deployments = %v, %v", list, err)
	}
	if s := list[0]; s.ComparisonStatus == nil || *s.ComparisonStatus != deployradar.ComparisonPending || s.Verdict != nil {
		t.Errorf("successful summary = %+v, want a pending comparison without a verdict", s)
	}
}

func TestSuccessesBetween(t *testing.T) {
	f := newFixture(t)
	service := t.Name() + "-svc"
	facts := func(name, svc, env string) deployradar.Facts {
		x := f.facts(name)
		x.Service, x.Environment = svc, env
		return x
	}
	t0 := at(f.base, 0)
	this := f.deliver(facts("this", service, "local"), deployradar.StateSuccess, t0).Deployment
	inside := f.deliver(facts("inside", service, "local"), deployradar.StateSuccess, at(f.base, 2*time.Minute)).Deployment
	f.deliver(facts("edge", service, "local"), deployradar.StateSuccess, at(f.base, 5*time.Minute))      // at `to`: excluded
	f.deliver(facts("elsewhere", service, "staging"), deployradar.StateSuccess, at(f.base, time.Minute)) // other environment
	f.deliver(facts("other", service+"-x", "local"), deployradar.StateSuccess, at(f.base, time.Minute))  // other service
	f.deliver(facts("running", service, "local"), deployradar.StateInProgress, at(f.base, time.Minute))  // never succeeded
	// A deployment that succeeded and later failed still changed the release.
	later := facts("later-failed", service, "local")
	f.deliver(later, deployradar.StateSuccess, at(f.base, 3*time.Minute))
	f.deliver(later, deployradar.StateFailure, at(f.base, 4*time.Minute))

	got, err := f.db.SuccessesBetween(f.ctx, service, "local", this.ID, at(f.base, -time.Minute), at(f.base, 5*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].DeploymentID != inside.ID || !got[0].At.Equal(at(f.base, 2*time.Minute)) ||
		!got[1].At.Equal(at(f.base, 3*time.Minute)) {
		t.Errorf("SuccessesBetween = %+v, want the deployment at +2m and the one that succeeded at +3m", got)
	}
}

func TestComparisonClaimLifecycle(t *testing.T) {
	f := newFixture(t)
	now := at(time.Now(), 0)
	lease := 2 * time.Minute

	// Due: succeeded 10 minutes ago, so the after window closed 6 minutes ago.
	due := f.deliver(f.facts("due"), deployradar.StateSuccess, at(now, -10*time.Minute)).Deployment
	// Not due within this test: its windows lie an hour ahead of every claim.
	fresh := f.deliver(f.facts("fresh"), deployradar.StateSuccess, at(now, 2*time.Hour)).Deployment

	claimWith := func(o deployradar.ClaimOptions) []deployradar.ClaimedComparison {
		t.Helper()
		got, err := f.db.ClaimComparisons(f.ctx, o)
		if err != nil {
			t.Fatalf("ClaimComparisons: %v", err)
		}
		// Other tests' comparisons may be due too, and older ones are claimed
		// first, so the limit is high enough to reach this test's; keep its own.
		var own []deployradar.ClaimedComparison
		for _, c := range got {
			if c.DeploymentID == due.ID || c.DeploymentID == fresh.ID {
				own = append(own, c)
			}
		}
		return own
	}
	claim := func(at time.Time, maxAttempts int) []deployradar.ClaimedComparison {
		return claimWith(deployradar.ClaimOptions{Now: at, Lease: lease, MaxAttempts: maxAttempts, Limit: 1000})
	}

	got := claim(now, 3)
	if len(got) != 1 || got[0].DeploymentID != due.ID {
		t.Fatalf("first claim = %+v, want only the due comparison", got)
	}
	if got[0].Attempts != 1 || got[0].ClaimedAt == nil || got[0].Deployment.Service != due.Service || got[0].Deployment.ID != due.ID {
		t.Errorf("claimed = %+v", got[0])
	}
	if again := claim(now.Add(time.Second), 3); len(again) != 0 {
		t.Errorf("claim inside the lease = %+v, want none", again)
	}
	// The after window closed 6 minutes ago; a 10-minute grace holds it back.
	if held := claimWith(deployradar.ClaimOptions{Now: now.Add(lease), Grace: 10 * time.Minute, Lease: lease, MaxAttempts: 3, Limit: 1000}); len(held) != 0 {
		t.Errorf("claim inside the grace period = %+v, want none", held)
	}

	// The attempt fails with Prometheus down: the row stays pending.
	if err := f.db.FinishComparison(f.ctx, got[0].ID, deployradar.ComparisonOutcome{Status: deployradar.ComparisonPending, Error: "prometheus unavailable", At: now}); err != nil {
		t.Fatalf("FinishComparison(pending): %v", err)
	}
	if c := f.detail(due.ID).Comparison; c.Status != deployradar.ComparisonPending || c.Error != "prometheus unavailable" || c.Attempts != 1 {
		t.Errorf("after failed attempt = %+v", c)
	}

	second := claim(now.Add(lease), 3)
	if len(second) != 1 || second[0].Attempts != 2 {
		t.Fatalf("claim after the lease = %+v, want attempts 2", second)
	}
	third := claim(now.Add(2*lease), 3)
	if len(third) != 1 || third[0].Attempts != 3 {
		t.Fatalf("third claim = %+v, want attempts 3", third)
	}
	if fourth := claim(now.Add(3*lease), 3); len(fourth) != 0 {
		t.Errorf("claim past max attempts = %+v, want none", fourth)
	}

	p95Before, p95After := 0.120, 0.131
	result := &deployradar.Result{
		BeforeRequests: 1200, BeforeErrors: 5, AfterRequests: 1180, AfterErrors: 116,
		BeforeP95: &p95Before, AfterP95: &p95After,
		ErrorRateVerdict: deployradar.VerdictRegression, P95Verdict: deployradar.VerdictNoChange,
		Verdict: deployradar.VerdictRegression, Confidence: 0.999,
		Reasons: []string{"error rate 0.4% → 9.8% (+9.4 pp, p<0.001)"},
		Queries: map[string]deployradar.Query{
			"before_requests": {PromQL: `sum(increase(http_server_request_duration_seconds_count{service_name="demo-service"}[3m]))`, Time: at(now, -13*time.Minute), Range: "3m"},
		},
		Routes: []deployradar.RouteStats{
			{Route: "/orders", BeforeRequests: 200, AfterRequests: 190, AfterErrors: 80},
			{Route: "", BeforeRequests: 5, AfterRequests: 4},
		},
		MethodVersion: 1, ComputedAt: now,
	}
	if err := f.db.FinishComparison(f.ctx, third[0].ID, deployradar.ComparisonOutcome{Status: deployradar.ComparisonDone, Result: result, At: now}); err != nil {
		t.Fatalf("FinishComparison(done): %v", err)
	}
	c := f.detail(due.ID).Comparison
	if c.Status != deployradar.ComparisonDone || c.Error != "" || c.Result == nil {
		t.Fatalf("done comparison = %+v", c)
	}
	r := c.Result
	if r.Verdict != deployradar.VerdictRegression || r.AfterErrors != 116 || *r.AfterP95 != p95After || r.Confidence != 0.999 || !r.ComputedAt.Equal(now) {
		t.Errorf("result = %+v", r)
	}
	if !slices.Equal(r.Routes, result.Routes) {
		t.Errorf("routes = %+v, want them back in order: %+v", r.Routes, result.Routes)
	}
	if len(r.Reasons) != 1 || r.Reasons[0] != result.Reasons[0] {
		t.Errorf("reasons = %v", r.Reasons)
	}
	if q, ok := r.Queries["before_requests"]; !ok || q.PromQL != result.Queries["before_requests"].PromQL || q.Range != "3m" || !q.Time.Equal(at(now, -13*time.Minute)) {
		t.Errorf("queries = %+v", r.Queries)
	}
	if rate := deployradar.ErrorRate(r.AfterErrors, r.AfterRequests); rate < 0.098 || rate > 0.0984 {
		t.Errorf("after error rate = %v", rate)
	}
	list, err := f.db.ListDeployments(f.ctx, deployradar.DeploymentFilter{Service: due.Service, BeforeID: due.ID + 1, Limit: 1})
	if err != nil || len(list) != 1 || list[0].ID != due.ID {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if s := list[0]; s.ComparisonStatus == nil || *s.ComparisonStatus != deployradar.ComparisonDone ||
		s.Verdict == nil || *s.Verdict != deployradar.VerdictRegression || s.Confidence == nil || *s.Confidence != 0.999 {
		t.Errorf("done summary = %+v, want the verdict and confidence", s)
	}

	// A done row is never claimed again, and the fresh one is still not due.
	if later := claim(now.Add(time.Hour), 10); len(later) != 0 {
		t.Errorf("claim after done = %+v, want none", later)
	}
	if _, err := f.db.ClaimComparisons(f.ctx, deployradar.ClaimOptions{Now: now, Lease: lease, MaxAttempts: 0, Limit: 1}); err == nil {
		t.Error("zero max attempts accepted")
	}

	// Giving up is recorded as failed.
	if err := f.db.FinishComparison(f.ctx, f.detail(fresh.ID).Comparison.ID, deployradar.ComparisonOutcome{Status: deployradar.ComparisonFailed, Error: "gave up after 5 attempts", At: now}); err != nil {
		t.Fatalf("FinishComparison(failed): %v", err)
	}
	if c := f.detail(fresh.ID).Comparison; c.Status != deployradar.ComparisonFailed || c.Result != nil {
		t.Errorf("failed comparison = %+v", c)
	}
	if err := f.db.FinishComparison(f.ctx, c.ID, deployradar.ComparisonOutcome{Status: deployradar.ComparisonDone, At: now}); err == nil {
		t.Error("done without a result accepted")
	}
}
