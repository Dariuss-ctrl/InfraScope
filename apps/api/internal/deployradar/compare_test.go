package deployradar

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func f(v float64) *float64 { return &v }

var th = DefaultThresholds(200)

func TestJudgeFindsAnErrorRateRegression(t *testing.T) {
	// The demo regression: 0.4% → 9.8% on about 1200 requests a window.
	j := Judge(Sample{f(1200), f(5), f(0.120)}, Sample{f(1180), f(116), f(0.131)}, nil, th)
	if j.ErrorRate != VerdictRegression || j.Overall != VerdictRegression {
		t.Fatalf("verdicts = %+v, want an error-rate regression", j)
	}
	if j.P95 != VerdictNoChange {
		t.Errorf("p95 verdict = %s, want no_change (131 ms is under 1.25 × 120 ms)", j.P95)
	}
	if j.Confidence < 0.99 || j.Confidence > 1 {
		t.Errorf("confidence = %v, want 1 − p close to 1", j.Confidence)
	}
	if len(j.Reasons) != 2 || !strings.Contains(j.Reasons[0], "error rate 0.4% → 9.8% (+9.4 pp, p<0.001)") {
		t.Errorf("reasons = %q", j.Reasons)
	}
	if !strings.Contains(j.Reasons[1], "p95 (non-5xx) 120 ms → 131 ms (+9%)") {
		t.Errorf("p95 reason = %q", j.Reasons[1])
	}
}

func TestJudgeIgnoresSignificantButTinyChanges(t *testing.T) {
	// 0.1% → 0.9% over 100k requests: p is minuscule, the change is under
	// one percentage point, so it is not called a regression.
	j := Judge(Sample{f(100000), f(100), f(0.1)}, Sample{f(100000), f(900), f(0.1)}, nil, th)
	if j.ErrorRate != VerdictNoChange || j.Overall != VerdictNoChange {
		t.Errorf("verdicts = %+v, want no_change", j)
	}
}

func TestJudgeIgnoresLargeButInsignificantChanges(t *testing.T) {
	// 0.5% → 1.5% on 1000 requests: one percentage point, but p ≈ 0.025.
	j := Judge(Sample{f(1000), f(5), f(0.1)}, Sample{f(1000), f(15), f(0.1)}, nil, th)
	if j.ErrorRate != VerdictNoChange {
		t.Errorf("error-rate verdict = %s, want no_change", j.ErrorRate)
	}
	if !strings.Contains(j.Reasons[0], "p=0.02") {
		t.Errorf("reason = %q, want the p-value spelled out", j.Reasons[0])
	}
	// Confidence in no_change comes from traffic, not 1 − p: 1000 requests
	// against a minimum of 200 is 1000 / 1200.
	if j.Overall != VerdictNoChange || j.Confidence < 0.83 || j.Confidence > 0.84 {
		t.Errorf("overall %s with confidence %v, want no_change at about 0.833", j.Overall, j.Confidence)
	}
}

func TestBothSignalsAgreeingTakesTheStrongerConfidence(t *testing.T) {
	// Errors 0.5% → 10% (p tiny) and p95 50 → 175 ms on 1800 requests a
	// window: the error-rate evidence alone is near certain.
	j := Judge(Sample{f(1800), f(9), f(0.049)}, Sample{f(1800), f(180), f(0.175)}, nil, th)
	if j.ErrorRate != VerdictRegression || j.P95 != VerdictRegression || j.Overall != VerdictRegression {
		t.Fatalf("verdicts = %+v", j)
	}
	if j.Confidence < 0.999 {
		t.Errorf("confidence = %v, want the error-rate signal's, not the p95 signal's 0.9", j.Confidence)
	}
}

func TestCombineConfidence(t *testing.T) {
	cases := []struct {
		name    string
		verdict Verdict
		signals []signal
		want    float64
	}{
		{"two regressions take the stronger", VerdictRegression, []signal{{VerdictRegression, 0.999}, {VerdictRegression, 0.9}}, 0.999},
		{"one regression takes its own", VerdictRegression, []signal{{VerdictNoChange, 0.95}, {VerdictRegression, 0.6}}, 0.6},
		{"two no_change take the weaker", VerdictNoChange, []signal{{VerdictNoChange, 0.95}, {VerdictNoChange, 0.6}}, 0.6},
		{"no_change ignores an improvement", VerdictNoChange, []signal{{VerdictNoChange, 0.8}, {VerdictImprovement, 0.99}}, 0.8},
		{"two improvements take the stronger", VerdictImprovement, []signal{{VerdictImprovement, 0.7}, {VerdictImprovement, 0.8}}, 0.8},
		{"insufficient data is 0", VerdictInsufficientData, []signal{{VerdictNoChange, 0.8}, {VerdictInsufficientData, 0}}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := combineConfidence(tc.verdict, tc.signals...); got != tc.want {
				t.Errorf("combineConfidence = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestJudgeFindsImprovements(t *testing.T) {
	j := Judge(Sample{f(1180), f(116), f(0.300)}, Sample{f(1200), f(5), f(0.120)}, nil, th)
	if j.ErrorRate != VerdictImprovement || j.P95 != VerdictImprovement || j.Overall != VerdictImprovement {
		t.Errorf("verdicts = %+v, want improvements", j)
	}
	if !strings.Contains(j.Reasons[0], "-9.4 pp") || !strings.Contains(j.Reasons[1], "300 ms → 120 ms (-60%)") {
		t.Errorf("reasons = %q", j.Reasons)
	}
}

func TestJudgeP95Rules(t *testing.T) {
	cases := []struct {
		name          string
		before, after float64
		want          Verdict
	}{
		{"ratio and delta exceeded", 0.120, 0.160, VerdictRegression},
		{"ratio exceeded, delta too small", 0.010, 0.014, VerdictNoChange},
		{"delta exceeded, ratio too small", 1.000, 1.100, VerdictNoChange},
		{"exactly the ratio and the delta", 0.100, 0.125, VerdictRegression},
		{"faster by both", 0.200, 0.100, VerdictImprovement},
		{"from zero", 0, 0.030, VerdictRegression},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			j := Judge(Sample{f(5000), f(10), f(tc.before)}, Sample{f(5000), f(10), f(tc.after)}, nil, th)
			if j.P95 != tc.want {
				t.Errorf("p95 verdict = %s, want %s (%v)", j.P95, tc.want, j.Reasons)
			}
			if j.ErrorRate != VerdictNoChange {
				t.Errorf("error-rate verdict = %s", j.ErrorRate)
			}
			// The error rate does not change here, so p95 decides.
			if j.Overall != tc.want {
				t.Errorf("overall = %s, want %s", j.Overall, tc.want)
			}
		})
	}
}

func TestJudgeP95ConfidenceGrowsWithTraffic(t *testing.T) {
	at := func(n float64) float64 {
		return Judge(Sample{f(n), f(0), f(0.1)}, Sample{f(n), f(0), f(0.2)}, nil, th).Confidence
	}
	if c := at(200); c < 0.49 || c > 0.51 {
		t.Errorf("confidence at the minimum traffic = %v, want 0.5", c)
	}
	if c := at(1800); c < 0.89 || c > 0.91 {
		t.Errorf("confidence at 9× the minimum = %v, want 0.9", c)
	}
	if at(200) >= at(1800) {
		t.Error("confidence should grow with traffic")
	}
}

func TestJudgeWithoutAHistogram(t *testing.T) {
	j := Judge(Sample{f(1200), f(5), nil}, Sample{f(1180), f(116), f(0.1)}, nil, th)
	if j.ErrorRate != VerdictRegression || j.P95 != VerdictInsufficientData || j.Overall != VerdictRegression {
		t.Errorf("verdicts = %+v; the error-rate regression must still show", j)
	}
	if !strings.Contains(strings.Join(j.Reasons, "\n"), "before window: no latency histogram") {
		t.Errorf("reasons = %q", j.Reasons)
	}

	j = Judge(Sample{f(1200), f(5), f(0.1)}, Sample{f(1180), f(5), nil}, nil, th)
	if j.Overall != VerdictInsufficientData || j.Confidence != 0 {
		t.Errorf("no change plus unknown p95 = %+v, want insufficient_data overall with confidence 0", j)
	}
}

func TestJudgeRefusesLowTraffic(t *testing.T) {
	j := Judge(Sample{f(57), f(0), f(0.1)}, Sample{f(1180), f(116), f(0.3)}, nil, th)
	if j.ErrorRate != VerdictInsufficientData || j.P95 != VerdictInsufficientData || j.Overall != VerdictInsufficientData || j.Confidence != 0 {
		t.Fatalf("judgement = %+v, want insufficient_data everywhere", j)
	}
	if len(j.Reasons) != 1 || j.Reasons[0] != "before window: 57 requests, fewer than the 200 needed" {
		t.Errorf("reasons = %q", j.Reasons)
	}

	j = Judge(Sample{}, Sample{f(10), nil, nil}, nil, th)
	if len(j.Reasons) != 2 || !strings.Contains(j.Reasons[0], "no request series") || !strings.Contains(j.Reasons[1], "10 requests") {
		t.Errorf("reasons = %q", j.Reasons)
	}
}

func TestJudgeTreatsMissingErrorSeriesAsZero(t *testing.T) {
	j := Judge(Sample{f(1000), nil, f(0.1)}, Sample{f(1000), nil, f(0.1)}, nil, th)
	if j.ErrorRate != VerdictNoChange || !strings.Contains(j.Reasons[0], "0.0% → 0.0%") {
		t.Errorf("judgement = %+v", j)
	}
}

func TestTwoProportionP(t *testing.T) {
	// 5/1000 vs 15/1000: z ≈ 2.25, two-sided p ≈ 0.025.
	if p := twoProportionP(5, 1000, 15, 1000); p < 0.02 || p > 0.03 {
		t.Errorf("p = %v, want about 0.025", p)
	}
	if p := twoProportionP(0, 1000, 0, 1000); p != 1 {
		t.Errorf("no errors anywhere: p = %v, want 1", p)
	}
	if p := twoProportionP(1000, 1000, 500, 500); p != 1 {
		t.Errorf("all errors everywhere: p = %v, want 1", p)
	}
	if p := twoProportionP(10, 1000, 10, 1000); p < 0.999 {
		t.Errorf("equal rates: p = %v, want 1", p)
	}
	if p := twoProportionP(5, 1200, 116, 1180); p > 1e-10 {
		t.Errorf("the demo regression: p = %v, want tiny", p)
	}
}

func TestOverall(t *testing.T) {
	r, i, n, x := VerdictRegression, VerdictImprovement, VerdictNoChange, VerdictInsufficientData
	cases := []struct {
		errorRate, p95, want Verdict
	}{
		{r, r, r}, {r, i, r}, {i, r, r}, {r, n, r}, {n, r, r}, {r, x, r}, {x, r, r},
		{i, i, i}, {i, n, i}, {n, i, i}, {i, x, i}, {x, i, i},
		{n, x, x}, {x, n, x}, {x, x, x},
		{n, n, n},
	}
	for _, tc := range cases {
		if got := overall(tc.errorRate, tc.p95); got != tc.want {
			t.Errorf("overall(%s, %s) = %s, want %s", tc.errorRate, tc.p95, got, tc.want)
		}
	}
}

// The fix from the milestone run (2026-11-15): v3 removed v2's injected
// faults. Errors fell from 19.9% to 0% while p95 stayed within its
// thresholds, which is an improvement, not "no change".
func TestJudgeCallsAFixAnImprovement(t *testing.T) {
	j := Judge(Sample{f(3602.0), f(716.4), f(0.0377)}, Sample{f(3601.8), f(0), f(0.0241)}, nil, th)
	if j.ErrorRate != VerdictImprovement || j.P95 != VerdictNoChange || j.Overall != VerdictImprovement {
		t.Fatalf("judgement = %+v, want an improvement", j)
	}
	if j.Confidence < 0.999 {
		t.Errorf("confidence = %v, want the error-rate signal's", j.Confidence)
	}
}

// v1 from the second milestone run: the machine stalled during the after
// window, so it saw 57% fewer requests. Nothing changed in the service, and
// the verdict says so, but it must not sound sure about it.
func TestJudgeFlagsATrafficDrop(t *testing.T) {
	j := Judge(Sample{f(3606.6), f(0), f(0.0466)}, Sample{f(1553.9), f(0), f(0.0434)}, nil, th)
	if j.Overall != VerdictNoChange {
		t.Fatalf("overall = %s, want no_change", j.Overall)
	}
	if j.Confidence > th.CaveatConfidence {
		t.Errorf("confidence = %v, want at most %v when the load changed", j.Confidence, th.CaveatConfidence)
	}
	last := j.Reasons[len(j.Reasons)-1]
	if !strings.HasPrefix(last, "traffic fell 57% between the windows (3607 → 1554 requests)") ||
		!strings.Contains(last, "drops connections") {
		t.Errorf("reason = %q", last)
	}
}

func TestJudgeFlagsATrafficRise(t *testing.T) {
	j := Judge(Sample{f(1000), f(0), f(0.05)}, Sample{f(2500), f(0), f(0.05)}, nil, th)
	if last := j.Reasons[len(j.Reasons)-1]; !strings.HasPrefix(last, "traffic rose 150% between the windows (1000 → 2500 requests)") {
		t.Errorf("reason = %q", last)
	}
	if j.Confidence > th.CaveatConfidence {
		t.Errorf("confidence = %v, want capped", j.Confidence)
	}
}

func TestJudgeAcceptsOrdinaryTrafficVariation(t *testing.T) {
	// Up to ×2 either way is ordinary; exactly ×2 is still accepted.
	for _, after := range []float64{1000, 600, 1800, 2000, 500} {
		j := Judge(Sample{f(1000), f(0), f(0.05)}, Sample{f(after), f(0), f(0.05)}, nil, th)
		for _, r := range j.Reasons {
			if strings.HasPrefix(r, "traffic ") {
				t.Errorf("1000 → %v requests flagged: %q", after, r)
			}
		}
	}
}

func TestJudgeNamesOverlappingDeployments(t *testing.T) {
	overlaps := []Overlap{
		{DeploymentID: 3, Window: "before"},
		{DeploymentID: 9, Window: "settle"},
		{DeploymentID: 12, Window: "after"},
	}
	// A clear regression stays a regression, but no longer near-certain.
	j := Judge(Sample{f(1200), f(5), f(0.1)}, Sample{f(1180), f(116), f(0.1)}, overlaps, th)
	if j.Overall != VerdictRegression || j.Confidence != th.CaveatConfidence {
		t.Fatalf("judgement = %+v, want a regression capped at %v", j, th.CaveatConfidence)
	}
	got := strings.Join(j.Reasons, "\n")
	for _, want := range []string{
		"deployment 3 succeeded during the before window, so the baseline mixes two releases.",
		"deployment 9 succeeded before the after window began, so the after window measures its release, not this one.",
		"deployment 12 succeeded during the after window, so the after window measures two releases.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("reasons lack %q:\n%s", want, got)
		}
	}

	// Overlaps are reported even when there is too little traffic to judge.
	j = Judge(Sample{f(10), f(0), f(0.1)}, Sample{f(10), f(0), f(0.1)}, overlaps[:1], th)
	if j.Overall != VerdictInsufficientData || !strings.Contains(strings.Join(j.Reasons, "\n"), "deployment 3") {
		t.Errorf("judgement = %+v", j)
	}
}

func TestOverlapsPlacesSuccessesInWindows(t *testing.T) {
	at := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	before, after := (Windows{Width: 3 * time.Minute, Settle: time.Minute}).Around(at)
	c := Comparison{Before: before, After: after}
	others := []Success{
		{DeploymentID: 1, At: at.Add(-3 * time.Minute)},               // the before window's start: outside
		{DeploymentID: 2, At: at.Add(-time.Minute)},                   // before
		{DeploymentID: 3, At: at.Add(30 * time.Second)},               // settle
		{DeploymentID: 4, At: at.Add(time.Minute)},                    // the after window's start: after
		{DeploymentID: 5, At: at.Add(3*time.Minute + 59*time.Second)}, // after
		{DeploymentID: 6, At: at.Add(4 * time.Minute)},                // the after window's end: outside
	}
	got := Overlaps(c, others)
	want := []Overlap{
		{DeploymentID: 2, At: others[1].At, Window: "before"},
		{DeploymentID: 3, At: others[2].At, Window: "settle"},
		{DeploymentID: 4, At: others[3].At, Window: "after"},
		{DeploymentID: 5, At: others[4].At, Window: "after"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("Overlaps = %+v\nwant       %+v", got, want)
	}
}

func TestFormatting(t *testing.T) {
	cases := map[string]string{
		seconds(0.0004): "0 ms",
		seconds(0.1234): "123 ms",
		seconds(1.5):    "1.50 s",
		pct(0.004167):   "0.4%",
		formatP(0.0001): "p<0.001",
		formatP(0.0247): "p=0.025",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}
