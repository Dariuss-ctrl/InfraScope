package github

import (
	"errors"
	"strings"
	"testing"
	"time"

	"infrascope/api/internal/deployradar"
)

const secret = "a-webhook-secret-of-decent-length"

func TestSignatureRoundTrip(t *testing.T) {
	body := []byte(`{"zen":"Design for failure."}`)
	sig := Sign(secret, body)
	if !strings.HasPrefix(sig, "sha256=") || len(sig) != len("sha256=")+64 {
		t.Fatalf("Sign = %q, want sha256= and 64 hex characters", sig)
	}
	if err := VerifySignature(secret, sig, body); err != nil {
		t.Fatalf("VerifySignature(own signature) = %v", err)
	}

	// The value GitHub documents for its example payload and secret.
	// https://docs.github.com/webhooks/using-webhooks/validating-webhook-deliveries
	const docSecret = "It's a Secret to Everybody"
	docBody := []byte("Hello, World!")
	docSig := "sha256=757107ea0eb2509fc211221cce984b8a37570b6d7586c22c46f4379c8b043e17"
	if err := VerifySignature(docSecret, docSig, docBody); err != nil {
		t.Errorf("GitHub's documented example rejected: %v", err)
	}
}

func TestSignatureRejections(t *testing.T) {
	body := []byte(`{"a":1}`)
	good := Sign(secret, body)
	cases := []struct {
		name           string
		secret, header string
		body           []byte
	}{
		{"wrong secret", "another-secret-of-decent-length", good, body},
		{"tampered body", secret, good, []byte(`{"a":2}`)},
		{"missing header", secret, "", body},
		{"sha1 prefix", secret, "sha1=" + good[len("sha256="):], body},
		{"not hex", secret, "sha256=" + strings.Repeat("zz", 32), body},
		{"short digest", secret, "sha256=abcdef", body},
		{"empty secret", "", good, body},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := VerifySignature(tc.secret, tc.header, tc.body)
			if err == nil {
				t.Fatal("accepted")
			}
			if tc.secret != "" && !errors.Is(err, ErrBadSignature) {
				t.Errorf("err = %v, want ErrBadSignature", err)
			}
		})
	}
}

// A trimmed GitHub deployment_status payload with the fields DeployRadar
// reads, plus some it does not.
const statusPayload = `{
  "action": "created",
  "deployment_status": {
    "id": 2000,
    "state": "success",
    "description": "Deployed",
    "environment": "local",
    "target_url": "https://example.com/run/1",
    "created_at": "2026-11-03T10:05:00Z",
    "creator": {"login": "github-actions[bot]"}
  },
  "deployment": {
    "id": 1000,
    "sha": "0123456789ABCDEF0123456789abcdef01234567",
    "ref": "main",
    "task": "deploy",
    "payload": %s,
    "environment": "production",
    "description": "Release v2",
    "created_at": "2026-11-03T10:00:00Z",
    "creator": {"login": "octocat"}
  },
  "repository": {"id": 1, "name": "shop", "full_name": "acme/shop"},
  "sender": {"login": "octocat"}
}`

func payloadWith(deploymentPayload string) []byte {
	return []byte(strings.Replace(statusPayload, "%s", deploymentPayload, 1))
}

func TestParseDeploymentStatus(t *testing.T) {
	u, err := ParseDeploymentStatus(payloadWith(`{"service": "demo-service", "version": "v2"}`))
	if err != nil {
		t.Fatalf("ParseDeploymentStatus: %v", err)
	}
	u.DeliveryID = "d-1"
	if err := u.Validate(); err != nil {
		t.Fatalf("parsed update is invalid: %v", err)
	}
	want := deployradar.StatusUpdate{
		DeliveryID: "d-1",
		Facts: deployradar.Facts{
			Provider: deployradar.ProviderGitHub, Repository: "acme/shop", ProviderDeploymentID: "1000",
			Service: "demo-service", Environment: "local", Ref: "main",
			SHA: "0123456789abcdef0123456789abcdef01234567", Creator: "octocat", Description: "Release v2",
			CreatedAt: time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC),
		},
		State:   deployradar.StateSuccess,
		StateAt: time.Date(2026, 11, 3, 10, 5, 0, 0, time.UTC),
	}
	if u.Facts != want.Facts || u.State != want.State || !u.StateAt.Equal(want.StateAt) {
		t.Errorf("parsed:\n %+v\nwant:\n %+v", u, want)
	}
}

func TestServiceFallsBackToRepositoryName(t *testing.T) {
	cases := map[string]string{
		`{"service": "demo-service"}`:         "demo-service",
		`"{\"service\": \"from-string\"}"`:    "from-string", // payload stored as a JSON string
		`{"other": "x"}`:                      "shop",
		`""`:                                  "shop",
		`null`:                                "shop",
		`"plain text"`:                        "shop",
		`{"service": "   "}`:                  "shop",
		`[1, 2]`:                              "shop",
		`{"service": "demo-service", "a": 1}`: "demo-service",
	}
	for payload, want := range cases {
		u, err := ParseDeploymentStatus(payloadWith(payload))
		if err != nil {
			t.Errorf("payload %s: %v", payload, err)
			continue
		}
		if u.Service != want {
			t.Errorf("payload %s: service = %q, want %q", payload, u.Service, want)
		}
	}
}

func TestParseDeploymentStatusRejections(t *testing.T) {
	cases := map[string]string{
		"not json":             `{`,
		"no deployment":        `{"action":"created","deployment_status":{"state":"success"}}`,
		"no state":             strings.Replace(string(payloadWith(`{}`)), `"state": "success",`, "", 1),
		"array instead of obj": `[]`,
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseDeploymentStatus([]byte(payload)); err == nil {
				t.Error("accepted")
			}
		})
	}

	// Unknown states and other bad values are left to Validate, which has
	// the single list of rules.
	u, err := ParseDeploymentStatus([]byte(strings.Replace(string(payloadWith(`{}`)), `"state": "success"`, `"state": "deployed"`, 1)))
	if err != nil {
		t.Fatal(err)
	}
	u.DeliveryID = "d"
	if err := u.Validate(); err == nil || !strings.Contains(err.Error(), "state") {
		t.Errorf("Validate() = %v, want a state error", err)
	}
}

func TestEnvironmentFallsBackToDeployment(t *testing.T) {
	payload := strings.Replace(string(payloadWith(`{}`)), `"environment": "local",`, "", 1)
	u, err := ParseDeploymentStatus([]byte(payload))
	if err != nil {
		t.Fatal(err)
	}
	if u.Environment != "production" {
		t.Errorf("environment = %q, want the deployment's", u.Environment)
	}
}

func TestLongDescriptionIsTruncatedOnARuneBoundary(t *testing.T) {
	long := strings.Repeat("é", 600) // 1200 bytes
	payload := strings.Replace(string(payloadWith(`{}`)), `"Release v2"`, `"`+long+`"`, 1)
	u, err := ParseDeploymentStatus([]byte(payload))
	if err != nil {
		t.Fatal(err)
	}
	if len(u.Description) != 1024 || !strings.HasSuffix(u.Description, "é") {
		t.Errorf("description has %d bytes, ends %q", len(u.Description), u.Description[len(u.Description)-2:])
	}
	u.DeliveryID = "d"
	if err := u.Validate(); err != nil {
		t.Errorf("truncated update invalid: %v", err)
	}
}
