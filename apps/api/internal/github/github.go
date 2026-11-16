// Package github verifies GitHub webhook deliveries and reads the
// deployment_status event into DeployRadar's terms. Everything here is pure:
// bytes in, values or errors out.
package github

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"infrascope/api/internal/deployradar"
)

// Webhook request headers.
const (
	HeaderEvent     = "X-GitHub-Event"
	HeaderDelivery  = "X-GitHub-Delivery"
	HeaderSignature = "X-Hub-Signature-256"

	// EventDeploymentStatus is the event DeployRadar consumes; every other
	// event is stored and ignored.
	EventDeploymentStatus = "deployment_status"
	EventPing             = "ping"
)

const signaturePrefix = "sha256="

// ErrBadSignature means the signature header is missing, malformed, or was
// not produced with the configured secret.
var ErrBadSignature = errors.New("github: signature does not match")

// Sign returns the X-Hub-Signature-256 value GitHub would send for body.
func Sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return signaturePrefix + hex.EncodeToString(mac.Sum(nil))
}

// VerifySignature checks the X-Hub-Signature-256 header against body. The
// comparison is constant-time, so a wrong signature takes as long as a
// right one.
func VerifySignature(secret, header string, body []byte) error {
	if secret == "" {
		return errors.New("github: no webhook secret configured")
	}
	digest, ok := strings.CutPrefix(header, signaturePrefix)
	if !ok {
		return fmt.Errorf("%w: header must start with %s", ErrBadSignature, signaturePrefix)
	}
	want, err := hex.DecodeString(digest)
	if err != nil || len(want) != sha256.Size {
		return fmt.Errorf("%w: header is not a hex SHA-256", ErrBadSignature)
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	if !hmac.Equal(mac.Sum(nil), want) {
		return ErrBadSignature
	}
	return nil
}

// deploymentStatusEvent is the part of GitHub's payload DeployRadar reads.
// https://docs.github.com/webhooks/webhook-events-and-payloads#deployment_status
type deploymentStatusEvent struct {
	Action           string `json:"action"`
	DeploymentStatus struct {
		ID          int64     `json:"id"`
		State       string    `json:"state"`
		Description string    `json:"description"`
		Environment string    `json:"environment"`
		CreatedAt   time.Time `json:"created_at"`
	} `json:"deployment_status"`
	Deployment struct {
		ID          int64           `json:"id"`
		SHA         string          `json:"sha"`
		Ref         string          `json:"ref"`
		Environment string          `json:"environment"`
		Description string          `json:"description"`
		Payload     json.RawMessage `json:"payload"`
		CreatedAt   time.Time       `json:"created_at"`
		Creator     struct {
			Login string `json:"login"`
		} `json:"creator"`
	} `json:"deployment"`
	Repository struct {
		Name     string `json:"name"`
		FullName string `json:"full_name"`
	} `json:"repository"`
}

// ParseDeploymentStatus reads a deployment_status payload. The result has no
// DeliveryID; the caller sets it from the request header, then validates.
//
// The service name comes from deployment.payload.service when the workflow
// set one, otherwise from the repository name. The environment is the
// status's, falling back to the deployment's. Both must equal the values of
// the Prometheus labels service_name and deployment_environment_name for the
// comparison to find the service's metrics (docs/runbook.md).
func ParseDeploymentStatus(payload []byte) (deployradar.StatusUpdate, error) {
	var ev deploymentStatusEvent
	dec := json.NewDecoder(bytes.NewReader(payload))
	if err := dec.Decode(&ev); err != nil {
		return deployradar.StatusUpdate{}, fmt.Errorf("payload is not a deployment_status event: %w", err)
	}
	if ev.Deployment.ID <= 0 {
		return deployradar.StatusUpdate{}, errors.New("payload has no deployment.id")
	}
	if ev.DeploymentStatus.State == "" {
		return deployradar.StatusUpdate{}, errors.New("payload has no deployment_status.state")
	}

	service := serviceFromPayload(ev.Deployment.Payload)
	if service == "" {
		service = ev.Repository.Name
	}
	environment := ev.DeploymentStatus.Environment
	if environment == "" {
		environment = ev.Deployment.Environment
	}
	description := ev.Deployment.Description
	if description == "" {
		description = ev.DeploymentStatus.Description
	}

	return deployradar.StatusUpdate{
		Facts: deployradar.Facts{
			Provider:             deployradar.ProviderGitHub,
			Repository:           ev.Repository.FullName,
			ProviderDeploymentID: strconv.FormatInt(ev.Deployment.ID, 10),
			Service:              service,
			Environment:          environment,
			Ref:                  ev.Deployment.Ref,
			SHA:                  strings.ToLower(ev.Deployment.SHA),
			Creator:              ev.Deployment.Creator.Login,
			Description:          truncate(description, 1024),
			CreatedAt:            ev.Deployment.CreatedAt,
		},
		State:   deployradar.State(ev.DeploymentStatus.State),
		StateAt: ev.DeploymentStatus.CreatedAt,
	}, nil
}

// serviceFromPayload reads deployment.payload.service. GitHub stores the
// payload as whatever the creating API call sent: a JSON object, or a string
// that may itself hold JSON.
func serviceFromPayload(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return ""
	}
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return ""
		}
		raw = []byte(strings.TrimSpace(s))
		if len(raw) == 0 || raw[0] != '{' {
			return ""
		}
	}
	var obj struct {
		Service string `json:"service"`
	}
	if json.Unmarshal(raw, &obj) != nil {
		return ""
	}
	return strings.TrimSpace(obj.Service)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	// Cut on a rune boundary.
	for n > 0 && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n]
}
