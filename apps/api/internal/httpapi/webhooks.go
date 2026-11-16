package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"infrascope/api/internal/deployradar"
	"infrascope/api/internal/github"
)

// maxWebhookBody caps a delivery. GitHub's own limit is 25 MB, but a
// deployment_status event is a few kilobytes.
const maxWebhookBody = 1 << 20

// webhookReceipt is the response to a webhook delivery. GitHub shows it in
// the delivery log, so it says what happened in plain words.
type webhookReceipt struct {
	DeliveryID string `json:"delivery_id"`
	Event      string `json:"event"`
	// Outcome is recorded (a deployment status), ignored (another event) or
	// duplicate (this delivery ID was processed before).
	Outcome          string `json:"outcome"`
	DeploymentID     *int64 `json:"deployment_id,omitempty"`
	Applied          *bool  `json:"applied,omitempty"`
	ComparisonQueued *bool  `json:"comparison_queued,omitempty"`
}

// githubWebhook receives GitHub webhooks. The route is public: the
// X-Hub-Signature-256 header, not a session, is what authenticates it.
// Nothing is stored before the signature checks out.
func (a *api) githubWebhook(w http.ResponseWriter, r *http.Request) {
	if a.deployments == nil || a.settings.WebhookSecret == "" {
		writeError(w, http.StatusServiceUnavailable, "webhooks_disabled",
			"Deployment webhooks are off. Set INFRASCOPE_GITHUB_WEBHOOK_SECRET to accept them.")
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxWebhookBody))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "payload_too_large", "Webhook payloads are limited to 1 MiB.")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid_argument", "The request body could not be read.")
		return
	}

	event := r.Header.Get(github.HeaderEvent)
	deliveryID := r.Header.Get(github.HeaderDelivery)
	signature := r.Header.Get(github.HeaderSignature)
	var problems []FieldError
	if event == "" || len(event) > 64 {
		problems = append(problems, FieldError{Field: github.HeaderEvent, Message: "header is required (at most 64 characters)"})
	}
	if deliveryID == "" || len(deliveryID) > 128 {
		problems = append(problems, FieldError{Field: github.HeaderDelivery, Message: "header is required (at most 128 characters)"})
	}
	if signature == "" {
		problems = append(problems, FieldError{Field: github.HeaderSignature, Message: "header is required"})
	}
	if len(problems) > 0 {
		a.fail(w, r, invalid(problems...), database)
		return
	}
	if err := github.VerifySignature(a.settings.WebhookSecret, signature, body); err != nil {
		a.logger.WarnContext(r.Context(), "webhook signature rejected",
			"client", clientIP(r), "delivery", deliveryID, "event", event)
		writeError(w, http.StatusUnauthorized, "invalid_signature",
			"The X-Hub-Signature-256 header does not match the body for the configured secret.")
		return
	}
	if !json.Valid(body) {
		a.fail(w, r, invalid(FieldError{Field: "body", Message: "must be JSON"}), database)
		return
	}

	now := a.now()
	receipt, err := a.deployments.RecordDelivery(r.Context(), deployradar.Delivery{
		ID: deliveryID, Event: event, Payload: body, ReceivedAt: now,
	})
	if err != nil {
		a.fail(w, r, err, database)
		return
	}
	// A delivery seen before is answered, not processed again, unless its
	// earlier attempt failed: GitHub's "Redeliver" button retries those.
	if !receipt.New && receipt.PriorOutcome != deployradar.OutcomeFailed {
		a.logger.InfoContext(r.Context(), "duplicate webhook delivery", "delivery", deliveryID, "event", event)
		writeJSON(w, http.StatusOK, webhookReceipt{DeliveryID: deliveryID, Event: event, Outcome: "duplicate"})
		return
	}

	if event != github.EventDeploymentStatus {
		if err := a.deployments.FinishDelivery(r.Context(), deliveryID, deployradar.OutcomeIgnored, "", now); err != nil {
			a.fail(w, r, err, database)
			return
		}
		writeJSON(w, http.StatusOK, webhookReceipt{DeliveryID: deliveryID, Event: event, Outcome: "ignored"})
		return
	}

	u, err := github.ParseDeploymentStatus(body)
	if err == nil {
		u.DeliveryID = deliveryID
		err = u.Validate()
	}
	if err != nil {
		a.finishDelivery(r, deliveryID, deployradar.OutcomeFailed, err.Error(), now)
		a.fail(w, r, invalid(FieldError{Field: "payload", Message: err.Error()}), database)
		return
	}

	res, err := a.deployments.ApplyStatus(r.Context(), u, a.settings.DeployWindows, now)
	if err != nil {
		a.finishDelivery(r, deliveryID, deployradar.OutcomeFailed, "apply: "+err.Error(), now)
		a.fail(w, r, err, database)
		return
	}
	a.logger.InfoContext(r.Context(), "deployment status recorded",
		"deployment", res.Deployment.ID, "service", u.Service, "environment", u.Environment,
		"state", u.State, "applied", res.Applied, "comparison_queued", res.ComparisonQueued)
	writeJSON(w, http.StatusOK, webhookReceipt{
		DeliveryID: deliveryID, Event: event, Outcome: "recorded",
		DeploymentID: &res.Deployment.ID, Applied: &res.Applied, ComparisonQueued: &res.ComparisonQueued,
	})
}

// finishDelivery records a delivery's failure. It is best effort: when the
// database is the problem, there is nowhere to record that.
func (a *api) finishDelivery(r *http.Request, deliveryID string, outcome deployradar.Outcome, message string, at time.Time) {
	if err := a.deployments.FinishDelivery(r.Context(), deliveryID, outcome, message, at); err != nil {
		a.logger.WarnContext(r.Context(), "could not record delivery outcome",
			"delivery", deliveryID, "outcome", outcome, "error", err)
	}
}
