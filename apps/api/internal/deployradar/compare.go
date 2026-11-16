package deployradar

import (
	"context"
	"fmt"
	"math"
	"slices"
	"time"
)

// MethodVersion is stored with every result, so results judged by an older
// version of the rules below can be told apart from newer ones.
//
//	1  error rate (z-test) and p95 over all responses
//	2  p95 over non-5xx responses only, since failing requests are often fast
//	   and pulled p95 down exactly when a release broke; a traffic-change
//	   check; overlapping deployments named (ADR-0007)
const MethodVersion = 2

// Thresholds are the rules a comparison is judged by (ADR-0007).
type Thresholds struct {
	// MinRequests is the traffic each window needs for any verdict other
	// than insufficient_data.
	MinRequests float64
	// ErrorRateMinDelta is the smallest change in error rate that counts,
	// in rate units: 0.01 is one percentage point.
	ErrorRateMinDelta float64
	// ErrorRatePValue is the two-sided significance level for the z-test.
	ErrorRatePValue float64
	// P95Ratio and P95MinDelta (seconds) must both be exceeded for a p95
	// change to count; the ratio alone would flag 2 ms → 3 ms.
	P95Ratio    float64
	P95MinDelta float64
	// TrafficRatio is how far request counts may differ between the windows
	// (either way) before the comparison says the load changed.
	TrafficRatio float64
	// CaveatConfidence caps the confidence of a comparison whose windows
	// did not see the same conditions: a change in load, or another
	// deployment inside a window.
	CaveatConfidence float64
}

// DefaultThresholds are the rules documented in ADR-0007.
func DefaultThresholds(minRequests int) Thresholds {
	return Thresholds{
		MinRequests:       float64(minRequests),
		ErrorRateMinDelta: 0.01,
		ErrorRatePValue:   0.01,
		P95Ratio:          1.25,
		P95MinDelta:       0.025,
		TrafficRatio:      2,
		CaveatConfidence:  0.5,
	}
}

// Overlap is another deployment of the same service and environment that
// succeeded inside this comparison's span, so a window measured more than
// the one release being judged.
type Overlap struct {
	DeploymentID int64
	At           time.Time // when it succeeded
	// Window is where it fell: "before", "settle" (the gap between the
	// windows) or "after".
	Window string
}

// Overlaps places other deployments' success times in c's windows. Times
// outside c's span are dropped.
func Overlaps(c Comparison, others []Success) []Overlap {
	var out []Overlap
	for _, o := range others {
		var window string
		switch {
		case !o.At.After(c.Before.From) || !o.At.Before(c.After.To):
			continue
		case o.At.Before(c.Before.To):
			window = "before"
		case o.At.Before(c.After.From):
			window = "settle"
		default:
			window = "after"
		}
		out = append(out, Overlap{DeploymentID: o.DeploymentID, At: o.At, Window: window})
	}
	return out
}

// Measurement is one value with the query that produced it. A nil Value
// means Prometheus returned no series for the query.
type Measurement struct {
	Value *float64
	Query Query
}

// WindowStats are one service's measurements over one window.
type WindowStats struct {
	Requests Measurement // requests in the window
	Errors   Measurement // responses with a 5xx status
	P95      Measurement // p95 latency in seconds
}

// Sample returns the values alone, for Judge.
func (s WindowStats) Sample() Sample {
	return Sample{Requests: s.Requests.Value, Errors: s.Errors.Value, P95: s.P95.Value}
}

// Metrics measures a service's windows (*prometheus.Client).
type Metrics interface {
	WindowStats(ctx context.Context, service, environment string, w Window) (WindowStats, error)
}

// Sample is one window's values. Nil means no data, which is not zero.
type Sample struct {
	Requests *float64
	Errors   *float64
	P95      *float64
}

// Judgement is what Judge concluded, per signal and overall.
type Judgement struct {
	ErrorRate  Verdict
	P95        Verdict
	Overall    Verdict
	Confidence float64
	Reasons    []string
}

// Judge compares two windows. It is pure, so the rules are testable as a
// table; see ADR-0007 for why they are what they are.
//
//   - Either window below MinRequests, or without series at all, is
//     insufficient_data for everything. The engine never guesses.
//   - Error rate: a two-proportion z-test. A regression is a rise of at
//     least ErrorRateMinDelta with p < ErrorRatePValue; an improvement is
//     the mirror image. Confidence in a change is 1 − p.
//   - p95: a regression when after ≥ P95Ratio × before and the rise is at
//     least P95MinDelta; the mirror image is an improvement. Histogram
//     quantiles have no clean significance test, so confidence comes from
//     the sample size: n / (n + MinRequests) for the smaller window.
//   - A high p-value is not evidence that nothing changed, so confidence in
//     no_change comes from the sample size too, for both signals.
//   - Overall: a regression in either signal is a regression. Otherwise an
//     improvement in either is an improvement (a fix that cuts errors and
//     leaves latency alone improved the service). Otherwise a signal without
//     data makes the whole insufficient_data, and only two unchanged signals
//     are no_change.
//   - A regression or improvement takes the strongest confidence among the
//     signals that found it, since either one alone is enough; no_change
//     takes the weaker, since both must hold; insufficient_data has 0.
//   - Caveats keep the verdict but cap its confidence at CaveatConfidence:
//     request counts that differ by more than TrafficRatio either way (the
//     windows did not see the same load), and other deployments of the same
//     service inside the windows (a window measured two releases).
func Judge(before, after Sample, overlaps []Overlap, th Thresholds) Judgement {
	var reasons []string
	caveats := overlapReasons(overlaps)
	gate := func(name string, s Sample) {
		switch {
		case s.Requests == nil:
			reasons = append(reasons, name+" window: no request series for this service and environment")
		case *s.Requests < th.MinRequests:
			reasons = append(reasons, fmt.Sprintf("%s window: %.0f requests, fewer than the %.0f needed", name, *s.Requests, th.MinRequests))
		}
	}
	gate("before", before)
	gate("after", after)
	if len(reasons) > 0 {
		return Judgement{
			ErrorRate: VerdictInsufficientData, P95: VerdictInsufficientData, Overall: VerdictInsufficientData,
			Reasons: append(reasons, caveats...),
		}
	}

	// Error rate.
	n1, n2 := *before.Requests, *after.Requests
	e1, e2 := zeroIfNil(before.Errors), zeroIfNil(after.Errors)
	p1, p2 := e1/n1, e2/n2
	delta := p2 - p1
	pValue := twoProportionP(e1, n1, e2, n2)
	n := math.Min(n1, n2)
	sizeConfidence := clamp01(n / (n + th.MinRequests))
	errVerdict, errConfidence := VerdictNoChange, sizeConfidence
	switch {
	case delta >= th.ErrorRateMinDelta-epsilon && pValue < th.ErrorRatePValue:
		errVerdict, errConfidence = VerdictRegression, clamp01(1-pValue)
	case -delta >= th.ErrorRateMinDelta-epsilon && pValue < th.ErrorRatePValue:
		errVerdict, errConfidence = VerdictImprovement, clamp01(1-pValue)
	}
	reasons = append(reasons, fmt.Sprintf("error rate %s → %s (%+.1f pp, %s)", pct(p1), pct(p2), delta*100, formatP(pValue)))

	// p95.
	p95Verdict, p95Confidence := VerdictInsufficientData, 0.0
	switch {
	case before.P95 == nil:
		reasons = append(reasons, "before window: no latency histogram, so p95 is unknown")
	case after.P95 == nil:
		reasons = append(reasons, "after window: no latency histogram, so p95 is unknown")
	default:
		b, a := *before.P95, *after.P95
		p95Verdict = VerdictNoChange
		switch {
		case a >= th.P95Ratio*b-epsilon && a-b >= th.P95MinDelta-epsilon:
			p95Verdict = VerdictRegression
		case b >= th.P95Ratio*a-epsilon && b-a >= th.P95MinDelta-epsilon:
			p95Verdict = VerdictImprovement
		}
		p95Confidence = sizeConfidence
		if b > 0 {
			reasons = append(reasons, fmt.Sprintf("p95 (non-5xx) %s → %s (%+.0f%%)", seconds(b), seconds(a), (a/b-1)*100))
		} else {
			reasons = append(reasons, fmt.Sprintf("p95 (non-5xx) %s → %s", seconds(b), seconds(a)))
		}
	}

	// Traffic: the comparison assumes both windows saw the same load.
	switch ratio := n2 / n1; {
	case ratio < 1/th.TrafficRatio:
		caveats = append(caveats, fmt.Sprintf(
			"traffic fell %.0f%% between the windows (%.0f → %.0f requests), so they did not see the same load. "+
				"A release that drops connections before they reach the service also looks like this.",
			(1-ratio)*100, n1, n2))
	case ratio > th.TrafficRatio:
		caveats = append(caveats, fmt.Sprintf(
			"traffic rose %.0f%% between the windows (%.0f → %.0f requests), so they did not see the same load.",
			(ratio-1)*100, n1, n2))
	}

	verdict := overall(errVerdict, p95Verdict)
	confidence := combineConfidence(verdict,
		signal{errVerdict, errConfidence}, signal{p95Verdict, p95Confidence})
	if len(caveats) > 0 {
		confidence = math.Min(confidence, th.CaveatConfidence)
	}
	return Judgement{
		ErrorRate: errVerdict, P95: p95Verdict, Overall: verdict, Confidence: confidence,
		Reasons: append(reasons, caveats...),
	}
}

// overlapReasons says what each overlapping deployment did to the windows.
func overlapReasons(overlaps []Overlap) []string {
	var out []string
	for _, o := range overlaps {
		switch o.Window {
		case "before":
			out = append(out, fmt.Sprintf(
				"deployment %d succeeded during the before window, so the baseline mixes two releases.", o.DeploymentID))
		case "settle":
			out = append(out, fmt.Sprintf(
				"deployment %d succeeded before the after window began, so the after window measures its release, not this one.",
				o.DeploymentID))
		default:
			out = append(out, fmt.Sprintf(
				"deployment %d succeeded during the after window, so the after window measures two releases.", o.DeploymentID))
		}
	}
	return out
}

// overall combines the signals' verdicts, in this order of precedence:
// regression, improvement, insufficient_data, no_change (see Judge).
func overall(verdicts ...Verdict) Verdict {
	for _, v := range []Verdict{VerdictRegression, VerdictImprovement, VerdictInsufficientData} {
		if slices.Contains(verdicts, v) {
			return v
		}
	}
	return VerdictNoChange
}

// epsilon absorbs floating-point error at the thresholds, so a change of
// exactly the threshold counts ("at least" means at least).
const epsilon = 1e-9

type signal struct {
	verdict    Verdict
	confidence float64
}

// combineConfidence is the overall confidence for verdict, from the signals
// that reached it (see Judge).
func combineConfidence(verdict Verdict, signals ...signal) float64 {
	out, found := 0.0, false
	for _, s := range signals {
		if s.verdict != verdict {
			continue
		}
		switch {
		case !found:
			out = s.confidence
		case verdict == VerdictNoChange:
			out = math.Min(out, s.confidence)
		default:
			out = math.Max(out, s.confidence)
		}
		found = true
	}
	if verdict == VerdictInsufficientData {
		return 0
	}
	return out
}

// twoProportionP is the two-sided p-value of the pooled two-proportion
// z-test for e1/n1 against e2/n2. It is 1 when the rates are equal, which
// covers the pooled rate being 0 or 1, where the standard error vanishes.
func twoProportionP(e1, n1, e2, n2 float64) float64 {
	p := (e1 + e2) / (n1 + n2)
	se := math.Sqrt(p * (1 - p) * (1/n1 + 1/n2))
	if se == 0 {
		return 1
	}
	z := math.Abs((e2/n2 - e1/n1) / se)
	// Two-sided: 2 × (1 − Φ(z)) = erfc(z / √2).
	return math.Erfc(z / math.Sqrt2)
}

func clamp01(v float64) float64 {
	return math.Max(0, math.Min(1, v))
}

func zeroIfNil(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

func pct(p float64) string {
	return fmt.Sprintf("%.1f%%", p*100)
}

func formatP(p float64) string {
	if p < 0.001 {
		return "p<0.001"
	}
	return fmt.Sprintf("p=%.3f", p)
}

func seconds(s float64) string {
	if s >= 1 {
		return fmt.Sprintf("%.2f s", s)
	}
	return fmt.Sprintf("%.0f ms", s*1000)
}
