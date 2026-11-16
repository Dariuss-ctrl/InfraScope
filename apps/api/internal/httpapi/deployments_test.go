package httpapi

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"infrascope/api/internal/backend"
	"infrascope/api/internal/deployradar"
)

// Postgres hands times back in the server's zone; the API must answer in UTC.
var eat = time.FixedZone("EAT", 3*60*60)

func summary(id int64, verdict *deployradar.Verdict) deployradar.DeploymentSummary {
	created := time.Date(2026, 10, 30, 14, 0, 0, 0, eat)
	finished := created.Add(2 * time.Minute)
	s := deployradar.DeploymentSummary{Deployment: deployradar.Deployment{
		ID: id,
		Facts: deployradar.Facts{
			Provider: deployradar.ProviderGitHub, Repository: "acme/shop", ProviderDeploymentID: "100",
			Service: "demo-service", Environment: "local", Ref: "main",
			SHA: "0123456789abcdef0123456789abcdef01234567", Creator: "octocat", CreatedAt: created,
		},
		State: deployradar.StateSuccess, StateAt: finished, FinishedAt: &finished, UpdatedAt: finished,
	}}
	if verdict != nil {
		done, conf := deployradar.ComparisonDone, 0.999
		s.ComparisonStatus, s.Verdict, s.Confidence = &done, verdict, &conf
	}
	return s
}

func TestListDeployments(t *testing.T) {
	f := newFixture()
	regression := deployradar.VerdictRegression
	f.deployments.list = []deployradar.DeploymentSummary{summary(3, &regression), summary(2, nil), summary(1, nil)}

	rec := f.do(t, request{target: "/api/v1/deployments?service=demo-service&environment=local&limit=2&from=2026-10-30T00:00:00Z", token: goodToken})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	page := decodeBody[deploymentListJSON](t, rec)
	if len(page.Deployments) != 2 || page.Deployments[0].ID != 3 || page.Deployments[1].ID != 2 {
		t.Fatalf("page = %+v, want deployments 3 and 2", page.Deployments)
	}
	if page.NextCursor == nil || *page.NextCursor != "2" {
		t.Errorf("next_cursor = %v, want 2", page.NextCursor)
	}
	filter := f.deployments.listFilter
	if filter.Service != "demo-service" || filter.Environment != "local" || filter.Limit != 3 ||
		!filter.From.Equal(time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)) || !filter.To.IsZero() || filter.BeforeID != 0 {
		t.Errorf("filter = %+v, want the parameters plus one extra row", filter)
	}

	first := page.Deployments[0]
	if first.Verdict == nil || *first.Verdict != "regression" || first.ComparisonStatus == nil || *first.ComparisonStatus != "done" || *first.Confidence != 0.999 {
		t.Errorf("first = %+v, want the regression verdict", first)
	}
	if first.CreatedAt != "2026-10-30T11:00:00Z" || first.FinishedAt == nil || *first.FinishedAt != "2026-10-30T11:02:00Z" {
		t.Errorf("times = %s / %v, want UTC", first.CreatedAt, first.FinishedAt)
	}
	second := page.Deployments[1]
	if second.Verdict != nil || second.ComparisonStatus != nil || second.Confidence != nil {
		t.Errorf("second = %+v, want no comparison", second)
	}
	// null, not missing, so clients can rely on the keys.
	body := f.do(t, request{target: "/api/v1/deployments?limit=2", token: goodToken}).Body.String()
	if !strings.Contains(body, `"verdict":null`) || !strings.Contains(body, `"comparison_status":null`) {
		t.Errorf("body = %s, want explicit nulls", body)
	}

	rec = f.do(t, request{target: "/api/v1/deployments?limit=2&cursor=2", token: goodToken})
	page = decodeBody[deploymentListJSON](t, rec)
	if f.deployments.listFilter.BeforeID != 2 || len(page.Deployments) != 1 || page.Deployments[0].ID != 1 || page.NextCursor != nil {
		t.Errorf("second page = %+v (filter %+v), want deployment 1 and no cursor", page, f.deployments.listFilter)
	}
}

func TestListDeploymentsEmpty(t *testing.T) {
	rec := newFixture().do(t, request{target: "/api/v1/deployments", token: goodToken})
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"deployments":[],"next_cursor":null}` {
		t.Errorf("status %d body %s, want an empty list", rec.Code, rec.Body)
	}
}

func TestListDeploymentsValidation(t *testing.T) {
	cases := map[string]string{
		"limit=0":        "limit",
		"limit=501":      "limit",
		"from=yesterday": "from",
		"from=2026-10-30T10:00:00Z&to=2026-10-30T09:00:00Z": "from",
		"cursor=abc":                          "cursor",
		"cursor=-4":                           "cursor",
		"servcie=demo-service":                "servcie",
		"service=" + strings.Repeat("s", 129): "service",
		"environment=local&environment=prod":  "environment",
	}
	f := newFixture()
	for params, field := range cases {
		t.Run(params[:min(len(params), 40)], func(t *testing.T) {
			rec := f.do(t, request{target: "/api/v1/deployments?" + params, token: goodToken})
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", rec.Code)
			}
			e := errorOf(t, rec)
			if e.Code != "invalid_argument" || len(e.Fields) == 0 || e.Fields[0].Field != field {
				t.Errorf("error = %+v, want a problem with %s", e, field)
			}
		})
	}
}

func TestGetDeployment(t *testing.T) {
	f := newFixture()
	regression := deployradar.VerdictRegression
	d := summary(3, &regression).Deployment
	before, after := (deployradar.Windows{Width: 3 * time.Minute, Settle: time.Minute}).Around(*d.FinishedAt)
	p95b, p95a := 0.049, 0.175
	f.deployments.details = map[int64]deployradar.DeploymentDetail{3: {
		Deployment: d,
		Changes: []deployradar.Change{
			{ID: 1, DeploymentID: 3, DeliveryID: "d-1", State: deployradar.StateInProgress, StateAt: d.CreatedAt, Applied: true, RecordedAt: d.CreatedAt},
			{ID: 2, DeploymentID: 3, DeliveryID: "d-2", State: deployradar.StateSuccess, StateAt: *d.FinishedAt, Applied: true, RecordedAt: *d.FinishedAt},
		},
		Comparison: &deployradar.Comparison{
			ID: 9, DeploymentID: 3, Status: deployradar.ComparisonDone, Attempts: 1, Before: before, After: after,
			Result: &deployradar.Result{
				BeforeRequests: 1800, BeforeErrors: 9, AfterRequests: 1800, AfterErrors: 180,
				BeforeP95: &p95b, AfterP95: &p95a,
				ErrorRateVerdict: deployradar.VerdictRegression, P95Verdict: deployradar.VerdictRegression,
				Verdict: deployradar.VerdictRegression, Confidence: 0.9999,
				Reasons: []string{"error rate 0.5% → 10.0% (+9.5 pp, p<0.001)", "p95 49 ms → 175 ms (+259%)"},
				Queries: map[string]deployradar.Query{
					"after_errors": {PromQL: `sum(increase(x[180s]))`, Time: after.To, Range: "180s"},
				},
				MethodVersion: 1, ComputedAt: after.To.Add(time.Minute),
			},
		},
	}}

	rec := f.do(t, request{target: "/api/v1/deployments/3", token: goodToken})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	got := decodeBody[deploymentDetailJSON](t, rec)
	if got.ID != 3 || got.Service != "demo-service" || len(got.Changes) != 2 || got.Changes[1].State != "success" || got.Changes[1].StateAt != "2026-10-30T11:02:00Z" {
		t.Errorf("detail = %+v", got)
	}
	c := got.Comparison
	if c == nil || c.Status != "done" || c.Error != nil || c.Result == nil {
		t.Fatalf("comparison = %+v", c)
	}
	if c.After.From != "2026-10-30T11:03:00Z" || c.After.To != "2026-10-30T11:06:00Z" || c.Before.From != "2026-10-30T10:59:00Z" {
		t.Errorf("windows = %+v / %+v", c.Before, c.After)
	}
	r := c.Result
	if r.Verdict != "regression" || r.Before.ErrorRate != 0.005 || r.After.ErrorRate != 0.1 || *r.After.P95Seconds != 0.175 || len(r.Reasons) != 2 {
		t.Errorf("result = %+v", r)
	}
	if q := r.Queries["after_errors"]; q.PromQL != `sum(increase(x[180s]))` || q.Time != "2026-10-30T11:06:00Z" || q.Range != "180s" {
		t.Errorf("query = %+v", q)
	}

	// A deployment that never succeeded has no comparison: null, not missing.
	f.deployments.details[4] = deployradar.DeploymentDetail{Deployment: summary(4, nil).Deployment}
	rec = f.do(t, request{target: "/api/v1/deployments/4", token: goodToken})
	if body := rec.Body.String(); rec.Code != http.StatusOK || !strings.Contains(body, `"comparison":null`) || !strings.Contains(body, `"changes":[]`) {
		t.Errorf("status %d body %s", rec.Code, body)
	}
}

func TestGetDeploymentErrors(t *testing.T) {
	f := newFixture()
	for path, want := range map[string]int{
		"/api/v1/deployments/99":  http.StatusNotFound,
		"/api/v1/deployments/abc": http.StatusBadRequest,
		"/api/v1/deployments/0":   http.StatusBadRequest,
		"/api/v1/deployments/3?x": http.StatusBadRequest,
	} {
		if rec := f.do(t, request{target: path, token: goodToken}); rec.Code != want {
			t.Errorf("GET %s = %d, want %d", path, rec.Code, want)
		}
	}

	f.deployments.err = backend.ErrUnavailable
	for _, path := range []string{"/api/v1/deployments", "/api/v1/deployments/3"} {
		rec := f.do(t, request{target: path, token: goodToken})
		if rec.Code != http.StatusServiceUnavailable || errorOf(t, rec).Code != "database_unavailable" {
			t.Errorf("GET %s with the database down = %d", path, rec.Code)
		}
	}
}
