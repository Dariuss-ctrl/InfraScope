package deployradar

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestSupersedes(t *testing.T) {
	t0 := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	later := t0.Add(time.Minute)
	earlier := t0.Add(-time.Minute)

	tests := []struct {
		name               string
		current            State
		currentAt          time.Time
		next               State
		nextAt             time.Time
		want               bool
		whyThisRuleMatters string
	}{
		{"progress replaces pending", StatePending, t0, StateInProgress, later, true, ""},
		{"success replaces in_progress", StateInProgress, t0, StateSuccess, later, true, ""},
		{"late in_progress keeps success", StateSuccess, t0, StateInProgress, later, false,
			"a delayed webhook must not un-finish a deployment"},
		{"inactive keeps success", StateSuccess, t0, StateInactive, later, false,
			"GitHub marks superseded deployments inactive; the outcome stands"},
		{"inactive replaces pending", StatePending, t0, StateInactive, later, true, ""},
		{"terminal beats non-terminal even if earlier", StateInProgress, later, StateSuccess, earlier, true,
			"an out-of-order success still finishes the deployment"},
		{"later failure replaces success", StateSuccess, t0, StateFailure, later, true,
			"between terminal states the later status wins"},
		{"earlier failure does not replace success", StateSuccess, t0, StateFailure, earlier, false, ""},
		{"earlier in_progress does not replace queued", StateQueued, t0, StateInProgress, earlier, false,
			"between non-terminal states the later status wins"},
		{"equal time favours next", StateQueued, t0, StateInProgress, t0, true, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Supersedes(tc.current, tc.currentAt, tc.next, tc.nextAt); got != tc.want {
				t.Errorf("Supersedes(%s@%s, %s@%s) = %v, want %v. %s",
					tc.current, tc.currentAt.Format("15:04"), tc.next, tc.nextAt.Format("15:04"),
					got, tc.want, tc.whyThisRuleMatters)
			}
		})
	}
}

func TestStateValidAndTerminal(t *testing.T) {
	terminal := map[State]bool{StateSuccess: true, StateFailure: true, StateError: true}
	for _, s := range []State{StatePending, StateQueued, StateInProgress, StateSuccess, StateFailure, StateError, StateInactive} {
		if !s.Valid() {
			t.Errorf("%s should be valid", s)
		}
		if s.Terminal() != terminal[s] {
			t.Errorf("%s.Terminal() = %v, want %v", s, s.Terminal(), terminal[s])
		}
	}
	for _, s := range []State{"", "done", "SUCCESS", "in-progress"} {
		if s.Valid() {
			t.Errorf("%q should not be valid", s)
		}
	}
}

func TestWindowsAround(t *testing.T) {
	w := Windows{Width: 10 * time.Minute, Settle: 2 * time.Minute}
	at := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	before, after := w.Around(at)

	if before.From != at.Add(-10*time.Minute) || before.To != at {
		t.Errorf("before = %v, want [T-10m, T)", before)
	}
	if after.From != at.Add(2*time.Minute) || after.To != at.Add(12*time.Minute) {
		t.Errorf("after = %v, want [T+2m, T+12m)", after)
	}
	if !before.To.Before(after.From) && !before.To.Equal(after.From) {
		t.Error("windows overlap")
	}

	if err := (Windows{Width: 0, Settle: 0}).Validate(); err == nil {
		t.Error("zero width should be invalid")
	}
	if err := (Windows{Width: time.Minute, Settle: -1}).Validate(); err == nil {
		t.Error("negative settle should be invalid")
	}
	if err := (Windows{Width: time.Minute}).Validate(); err != nil {
		t.Errorf("zero settle should be valid: %v", err)
	}
}

func validUpdate() StatusUpdate {
	return StatusUpdate{
		DeliveryID: "d-1",
		Facts: Facts{
			Provider:             ProviderGitHub,
			Repository:           "acme/shop",
			ProviderDeploymentID: "1234",
			Service:              "demo-service",
			Environment:          "local",
			Ref:                  "main",
			SHA:                  "0123456789abcdef0123456789abcdef01234567",
			Creator:              "octocat",
			CreatedAt:            time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC),
		},
		State:   StateSuccess,
		StateAt: time.Date(2026, 11, 3, 10, 1, 0, 0, time.UTC),
	}
}

func TestStatusUpdateValidate(t *testing.T) {
	if err := validUpdate().Validate(); err != nil {
		t.Fatalf("valid update rejected: %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*StatusUpdate)
		want   string
	}{
		{"empty delivery id", func(u *StatusUpdate) { u.DeliveryID = " " }, "deliveryId"},
		{"long delivery id", func(u *StatusUpdate) { u.DeliveryID = strings.Repeat("x", 129) }, "deliveryId"},
		{"unknown provider", func(u *StatusUpdate) { u.Provider = "gitlab" }, "provider"},
		{"empty service", func(u *StatusUpdate) { u.Service = "" }, "service"},
		{"long environment", func(u *StatusUpdate) { u.Environment = strings.Repeat("e", 129) }, "environment"},
		{"short sha", func(u *StatusUpdate) { u.SHA = "abc" }, "sha"},
		{"uppercase sha", func(u *StatusUpdate) { u.SHA = "ABCDEF0123" }, "sha"},
		{"long description", func(u *StatusUpdate) { u.Description = strings.Repeat("d", 1025) }, "description"},
		{"zero created", func(u *StatusUpdate) { u.CreatedAt = time.Time{} }, "createdAt"},
		{"bad state", func(u *StatusUpdate) { u.State = "deployed" }, "state"},
		{"zero state time", func(u *StatusUpdate) { u.StateAt = time.Time{} }, "stateAt"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			u := validUpdate()
			tc.mutate(&u)
			err := u.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Validate() = %v, want an error mentioning %q", err, tc.want)
			}
		})
	}
}

func TestDeliveryValidate(t *testing.T) {
	now := time.Now()
	ok := Delivery{ID: "d", Event: "ping", Payload: json.RawMessage(`{"zen":"ok"}`), ReceivedAt: now}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid delivery rejected: %v", err)
	}
	bad := []Delivery{
		{ID: "", Event: "ping", Payload: json.RawMessage(`{}`), ReceivedAt: now},
		{ID: "d", Event: "", Payload: json.RawMessage(`{}`), ReceivedAt: now},
		{ID: "d", Event: "ping", Payload: json.RawMessage(`{`), ReceivedAt: now},
		{ID: "d", Event: "ping", Payload: nil, ReceivedAt: now},
		{ID: "d", Event: "ping", Payload: json.RawMessage(`{}`)},
	}
	for i, d := range bad {
		if err := d.Validate(); err == nil {
			t.Errorf("bad delivery %d accepted", i)
		}
	}
}

func TestComparisonOutcomeValidate(t *testing.T) {
	now := time.Now()
	result := &Result{
		ErrorRateVerdict: VerdictRegression, P95Verdict: VerdictNoChange, Verdict: VerdictRegression,
		Confidence: 0.99, Reasons: []string{"error rate 0.4% → 9.8%"}, ComputedAt: now,
	}
	cases := []struct {
		name string
		o    ComparisonOutcome
		ok   bool
	}{
		{"done with result", ComparisonOutcome{Status: ComparisonDone, Result: result, At: now}, true},
		{"done without result", ComparisonOutcome{Status: ComparisonDone, At: now}, false},
		{"pending with error", ComparisonOutcome{Status: ComparisonPending, Error: "prometheus unavailable", At: now}, true},
		{"pending without error", ComparisonOutcome{Status: ComparisonPending, At: now}, false},
		{"failed with result", ComparisonOutcome{Status: ComparisonFailed, Result: result, Error: "x", At: now}, false},
		{"unknown status", ComparisonOutcome{Status: "skipped", Error: "x", At: now}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.o.Validate(); (err == nil) != tc.ok {
				t.Errorf("Validate() = %v, want ok=%v", err, tc.ok)
			}
		})
	}

	badConfidence := *result
	badConfidence.Confidence = 1.5
	if err := badConfidence.Validate(); err == nil {
		t.Error("confidence above 1 accepted")
	}
	badVerdict := *result
	badVerdict.Verdict = "worse"
	if err := badVerdict.Validate(); err == nil {
		t.Error("unknown verdict accepted")
	}
}

func TestErrorRate(t *testing.T) {
	if got := ErrorRate(5, 100); got != 0.05 {
		t.Errorf("ErrorRate(5, 100) = %v", got)
	}
	if got := ErrorRate(5, 0); got != 0 {
		t.Errorf("ErrorRate(5, 0) = %v, want 0 (no division by zero)", got)
	}
}
