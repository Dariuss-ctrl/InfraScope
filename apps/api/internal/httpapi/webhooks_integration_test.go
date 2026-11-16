//go:build integration

package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"infrascope/api/internal/auth"
	"infrascope/api/internal/deployradar"
	"infrascope/api/internal/github"
	"infrascope/api/internal/health"
)

// webhookStack is the real router over the real PostgreSQL store, with the
// webhook route turned on.
func webhookStack() http.Handler {
	return NewRouter(Deps{
		Health: health.NewChecker(2*time.Second, logger), Auth: authSvc, Limiter: auth.NewLimiter(100, time.Second),
		Telemetry: chStore, Metrics: prom, Deployments: db, Logger: logger,
		Settings: Settings{
			MaxSearchRange: 24 * time.Hour, MaxMetricsRange: 7 * 24 * time.Hour, MaxSpans: 10_000,
			WebhookSecret: webhookSecret,
			DeployWindows: deployradar.Windows{Width: 3 * time.Minute, Settle: time.Minute},
		},
	})
}

func signedHeaders(secret, event, deliveryID, body string) map[string]string {
	return map[string]string{
		github.HeaderEvent:     event,
		github.HeaderDelivery:  deliveryID,
		github.HeaderSignature: github.Sign(secret, []byte(body)),
	}
}

func TestWebhookThroughTheContract(t *testing.T) {
	h := webhookStack()
	ctx := context.Background()
	t0 := time.Now().UTC().Truncate(time.Second).Add(-20 * time.Minute)

	post := func(secret, event, deliveryID, body string) (*http.Response, webhookReceipt, errorBody) {
		t.Helper()
		resp, raw := call(t, h, apiCall{method: "POST", path: "/api/v1/webhooks/github", body: body,
			header: signedHeaders(secret, event, deliveryID, body)})
		var receipt webhookReceipt
		var e errorEnvelope
		if resp.StatusCode == http.StatusOK {
			if err := json.Unmarshal(raw, &receipt); err != nil {
				t.Fatalf("decode receipt: %v", err)
			}
		} else {
			_ = json.Unmarshal(raw, &e)
		}
		return resp, receipt, e.Error
	}

	// ping: stored and ignored.
	resp, receipt, _ := post(webhookSecret, github.EventPing, "it-ping", `{"zen":"Practicality beats purity."}`)
	if resp.StatusCode != http.StatusOK || receipt.Outcome != "ignored" {
		t.Fatalf("ping = %d %+v", resp.StatusCode, receipt)
	}

	// A deployment's life: in_progress, success, a duplicate, a late in_progress.
	resp, inProgress, _ := post(webhookSecret, github.EventDeploymentStatus, "it-1", statusBody("in_progress", t0))
	if resp.StatusCode != http.StatusOK || inProgress.Outcome != "recorded" || inProgress.DeploymentID == nil || !*inProgress.Applied || *inProgress.ComparisonQueued {
		t.Fatalf("in_progress = %d %+v", resp.StatusCode, inProgress)
	}
	id := *inProgress.DeploymentID

	successBody := statusBody("success", t0.Add(time.Minute))
	_, success, _ := post(webhookSecret, github.EventDeploymentStatus, "it-2", successBody)
	if success.Outcome != "recorded" || *success.DeploymentID != id || !*success.Applied || !*success.ComparisonQueued {
		t.Fatalf("success = %+v", success)
	}
	_, dup, _ := post(webhookSecret, github.EventDeploymentStatus, "it-2", successBody)
	if dup.Outcome != "duplicate" || dup.DeploymentID != nil {
		t.Fatalf("duplicate = %+v", dup)
	}
	_, late, _ := post(webhookSecret, github.EventDeploymentStatus, "it-3", statusBody("in_progress", t0.Add(30*time.Second)))
	if late.Outcome != "recorded" || *late.Applied || *late.ComparisonQueued {
		t.Fatalf("late in_progress = %+v", late)
	}

	det, err := db.Deployment(ctx, id)
	if err != nil {
		t.Fatalf("Deployment(%d): %v", id, err)
	}
	if det.State != deployradar.StateSuccess || det.Service != "demo-service" || det.Environment != "local" || det.FinishedAt == nil {
		t.Errorf("deployment = %+v", det.Deployment)
	}
	if len(det.Changes) != 3 || det.Comparison == nil || det.Comparison.Status != deployradar.ComparisonPending {
		t.Errorf("history: %d changes, comparison %+v", len(det.Changes), det.Comparison)
	}

	// Wrong secret: 401 through the contract, and nothing stored.
	resp, _, e := post("not-the-secret-but-long-enough", github.EventDeploymentStatus, "it-bad", successBody)
	if resp.StatusCode != http.StatusUnauthorized || e.Code != "invalid_signature" {
		t.Errorf("bad signature = %d %+v", resp.StatusCode, e)
	}
	var stored int
	if err := db.Pool().QueryRow(ctx, `SELECT count(*) FROM webhook_deliveries WHERE delivery_id = 'it-bad'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != 0 {
		t.Error("a delivery with a bad signature was stored")
	}

	// No GitHub headers at all: 400 naming them.
	resp, raw := call(t, h, apiCall{method: "POST", path: "/api/v1/webhooks/github", body: `{}`})
	var env errorEnvelope
	_ = json.Unmarshal(raw, &env)
	if resp.StatusCode != http.StatusBadRequest || env.Error.Code != "invalid_argument" || len(env.Error.Fields) != 3 {
		t.Errorf("missing headers = %d %+v", resp.StatusCode, env.Error)
	}

	// Unparsable deployment_status: 400, and the delivery is kept as failed.
	resp, _, e = post(webhookSecret, github.EventDeploymentStatus, "it-broken", `{"action":"created"}`)
	if resp.StatusCode != http.StatusBadRequest || e.Code != "invalid_argument" {
		t.Errorf("broken payload = %d %+v", resp.StatusCode, e)
	}
	var outcome string
	if err := db.Pool().QueryRow(ctx, `SELECT outcome FROM webhook_deliveries WHERE delivery_id = 'it-broken'`).Scan(&outcome); err != nil {
		t.Fatal(err)
	}
	if outcome != "failed" {
		t.Errorf("broken delivery outcome = %q, want failed", outcome)
	}

	// The read endpoints, through the contract: first with the comparison
	// pending, then done with a full result.
	token := login(t, h)
	resp, raw = call(t, h, apiCall{path: "/api/v1/deployments?service=demo-service&environment=local", token: token})
	var list deploymentListJSON
	if err := json.Unmarshal(raw, &list); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("list = %d %s", resp.StatusCode, raw)
	}
	if len(list.Deployments) != 1 || list.Deployments[0].ID != id || list.Deployments[0].ComparisonStatus == nil ||
		*list.Deployments[0].ComparisonStatus != "pending" || list.Deployments[0].Verdict != nil {
		t.Errorf("list = %+v", list)
	}
	resp, raw = call(t, h, apiCall{path: fmt.Sprintf("/api/v1/deployments/%d", id), token: token})
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), `"result":null`) {
		t.Errorf("pending detail = %d %s", resp.StatusCode, raw)
	}

	claimed, err := db.ClaimComparisons(ctx, deployradar.ClaimOptions{Now: time.Now(), Lease: time.Minute, MaxAttempts: 5, Limit: 10})
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim = %v, %v", claimed, err)
	}
	p95b, p95a := 0.049, 0.175
	if err := db.FinishComparison(ctx, claimed[0].ID, deployradar.ComparisonOutcome{Status: deployradar.ComparisonDone, At: time.Now(), Result: &deployradar.Result{
		BeforeRequests: 1800, BeforeErrors: 9, AfterRequests: 1795.5, AfterErrors: 180,
		BeforeP95: &p95b, AfterP95: &p95a,
		ErrorRateVerdict: deployradar.VerdictRegression, P95Verdict: deployradar.VerdictRegression,
		Verdict: deployradar.VerdictRegression, Confidence: 0.9999,
		Reasons: []string{"error rate 0.5% → 10.0% (+9.5 pp, p<0.001)"},
		Queries: map[string]deployradar.Query{
			"after_errors": {PromQL: `sum(increase(http_server_request_duration_seconds_count{service_name="demo-service"}[180s]))`, Time: claimed[0].After.To, Range: "180s"},
		},
		MethodVersion: deployradar.MethodVersion, ComputedAt: time.Now(),
	}}); err != nil {
		t.Fatal(err)
	}
	resp, raw = call(t, h, apiCall{path: fmt.Sprintf("/api/v1/deployments/%d", id), token: token})
	var detail deploymentDetailJSON
	if err := json.Unmarshal(raw, &detail); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("done detail = %d %s", resp.StatusCode, raw)
	}
	if c := detail.Comparison; c == nil || c.Result == nil || c.Result.Verdict != "regression" || len(detail.Changes) != 3 {
		t.Errorf("done detail = %s", raw)
	}
	resp, _ = call(t, h, apiCall{path: "/api/v1/deployments/999999", token: token})
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown deployment = %d, want 404", resp.StatusCode)
	}
}

func TestWebhookIsOffByDefault(t *testing.T) {
	// The ordinary stack sets no secret and no store.
	resp, raw := call(t, stack{}.handler(), apiCall{method: "POST", path: "/api/v1/webhooks/github", body: `{}`,
		header: signedHeaders(webhookSecret, github.EventPing, "it-off", `{}`)})
	var env errorEnvelope
	_ = json.Unmarshal(raw, &env)
	if resp.StatusCode != http.StatusServiceUnavailable || env.Error.Code != "webhooks_disabled" {
		t.Errorf("without a secret = %d %+v, want 503 webhooks_disabled", resp.StatusCode, env.Error)
	}
}
