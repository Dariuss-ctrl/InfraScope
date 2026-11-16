package httpapi

import (
	"net/http"
	"strconv"
	"time"

	"infrascope/api/internal/deployradar"
)

// maxLabelValue is the longest service or environment name, matching the
// database's limit on the Prometheus label values deployments store.
const maxLabelValue = 128

// listDeployments lists deployments newest first (FR-9). The cursor is the
// last deployment ID of the previous page; clients treat it as opaque.
func (a *api) listDeployments(w http.ResponseWriter, r *http.Request) {
	q := parseQuery(r, "service", "environment", "from", "to", "limit", "cursor")
	f := deployradar.DeploymentFilter{
		Service:     q.text("service", maxLabelValue),
		Environment: q.text("environment", maxLabelValue),
	}
	limit := q.integer("limit", defaultLimit, 1, maxLimit)
	f.From, _ = q.timestamp("from", time.Time{})
	f.To, _ = q.timestamp("to", time.Time{})
	if !f.From.IsZero() && !f.To.IsZero() && !f.From.Before(f.To) {
		q.fail("from", "must be before to")
	}
	if c := q.cursor(); c != "" {
		id, err := strconv.ParseInt(c, 10, 64)
		if err != nil || id < 1 {
			q.fail("cursor", "is not a cursor returned by this search")
		}
		f.BeforeID = id
	}
	if err := q.err(); err != nil {
		a.fail(w, r, err, database)
		return
	}

	// One extra row says whether another page exists.
	f.Limit = limit + 1
	list, err := a.deployments.ListDeployments(r.Context(), f)
	if err != nil {
		a.fail(w, r, err, database)
		return
	}
	out := deploymentListJSON{Deployments: make([]deploymentSummaryJSON, 0, min(len(list), limit))}
	if len(list) > limit {
		list = list[:limit]
		out.NextCursor = optional(strconv.FormatInt(list[limit-1].ID, 10))
	}
	for _, s := range list {
		item := deploymentSummaryJSON{deploymentJSON: toDeploymentJSON(s.Deployment), Confidence: s.Confidence}
		if s.ComparisonStatus != nil {
			item.ComparisonStatus = optional(string(*s.ComparisonStatus))
		}
		if s.Verdict != nil {
			item.Verdict = optional(string(*s.Verdict))
		}
		out.Deployments = append(out.Deployments, item)
	}
	writeJSON(w, http.StatusOK, out)
}

// getDeployment returns one deployment with its status history and the
// comparison's evidence: the measured values and the exact PromQL behind
// each, so any number can be reproduced in Prometheus.
func (a *api) getDeployment(w http.ResponseWriter, r *http.Request) {
	q := parseQuery(r)
	id, err := strconv.ParseInt(r.PathValue("deploymentId"), 10, 64)
	if err != nil || id < 1 {
		q.fail("deploymentId", "must be a positive integer")
	}
	if err := q.err(); err != nil {
		a.fail(w, r, err, database)
		return
	}
	det, err := a.deployments.Deployment(r.Context(), id)
	if err != nil {
		a.fail(w, r, err, database)
		return
	}

	out := deploymentDetailJSON{
		deploymentJSON: toDeploymentJSON(det.Deployment),
		Changes:        make([]changeJSON, 0, len(det.Changes)),
	}
	for _, c := range det.Changes {
		out.Changes = append(out.Changes, changeJSON{
			DeliveryID: c.DeliveryID,
			State:      string(c.State),
			StateAt:    timestamp(c.StateAt),
			Applied:    c.Applied,
			RecordedAt: timestamp(c.RecordedAt),
		})
	}
	if c := det.Comparison; c != nil {
		cj := &comparisonJSON{
			Status:   string(c.Status),
			Attempts: c.Attempts,
			Before:   windowJSON{From: timestamp(c.Before.From), To: timestamp(c.Before.To)},
			After:    windowJSON{From: timestamp(c.After.From), To: timestamp(c.After.To)},
			Error:    optional(c.Error),
		}
		if res := c.Result; res != nil {
			rj := &resultJSON{
				Verdict:          string(res.Verdict),
				ErrorRateVerdict: string(res.ErrorRateVerdict),
				P95Verdict:       string(res.P95Verdict),
				Confidence:       res.Confidence,
				Reasons:          res.Reasons,
				Before:           measuredJSON{Requests: res.BeforeRequests, Errors: res.BeforeErrors, P95Seconds: res.BeforeP95},
				After:            measuredJSON{Requests: res.AfterRequests, Errors: res.AfterErrors, P95Seconds: res.AfterP95},
				Queries:          make(map[string]queryJSON, len(res.Queries)),
				MethodVersion:    res.MethodVersion,
				ComputedAt:       timestamp(res.ComputedAt),
			}
			// Derived from the stored counts, so it always reconciles with them.
			rj.Before.ErrorRate = deployradar.ErrorRate(res.BeforeErrors, res.BeforeRequests)
			rj.After.ErrorRate = deployradar.ErrorRate(res.AfterErrors, res.AfterRequests)
			if rj.Reasons == nil {
				rj.Reasons = []string{}
			}
			for key, qv := range res.Queries {
				rj.Queries[key] = queryJSON{PromQL: qv.PromQL, Time: timestamp(qv.Time), Range: qv.Range}
			}
			rj.Routes = make([]routeJSON, 0, len(res.Routes))
			for _, rt := range res.Routes {
				rj.Routes = append(rj.Routes, routeJSON{
					Route: rt.Route,
					Before: countsJSON{Requests: rt.BeforeRequests, Errors: rt.BeforeErrors,
						ErrorRate: deployradar.ErrorRate(rt.BeforeErrors, rt.BeforeRequests)},
					After: countsJSON{Requests: rt.AfterRequests, Errors: rt.AfterErrors,
						ErrorRate: deployradar.ErrorRate(rt.AfterErrors, rt.AfterRequests)},
				})
			}
			cj.Result = rj
		}
		out.Comparison = cj
	}
	writeJSON(w, http.StatusOK, out)
}

type deploymentJSON struct {
	ID                   int64   `json:"id"`
	Provider             string  `json:"provider"`
	Repository           string  `json:"repository"`
	ProviderDeploymentID string  `json:"provider_deployment_id"`
	Service              string  `json:"service"`
	Environment          string  `json:"environment"`
	Ref                  string  `json:"ref"`
	SHA                  string  `json:"sha"`
	Creator              string  `json:"creator"`
	Description          string  `json:"description"`
	CreatedAt            string  `json:"created_at"`
	State                string  `json:"state"`
	StateAt              string  `json:"state_at"`
	FinishedAt           *string `json:"finished_at"`
	UpdatedAt            string  `json:"updated_at"`
}

func toDeploymentJSON(d deployradar.Deployment) deploymentJSON {
	out := deploymentJSON{
		ID:                   d.ID,
		Provider:             d.Provider,
		Repository:           d.Repository,
		ProviderDeploymentID: d.ProviderDeploymentID,
		Service:              d.Service,
		Environment:          d.Environment,
		Ref:                  d.Ref,
		SHA:                  d.SHA,
		Creator:              d.Creator,
		Description:          d.Description,
		CreatedAt:            timestamp(d.CreatedAt),
		State:                string(d.State),
		StateAt:              timestamp(d.StateAt),
		UpdatedAt:            timestamp(d.UpdatedAt),
	}
	if d.FinishedAt != nil {
		out.FinishedAt = optional(timestamp(*d.FinishedAt))
	}
	return out
}

type deploymentSummaryJSON struct {
	deploymentJSON
	ComparisonStatus *string  `json:"comparison_status"`
	Verdict          *string  `json:"verdict"`
	Confidence       *float64 `json:"confidence"`
}

type deploymentListJSON struct {
	Deployments []deploymentSummaryJSON `json:"deployments"`
	NextCursor  *string                 `json:"next_cursor"`
}

type deploymentDetailJSON struct {
	deploymentJSON
	Changes    []changeJSON    `json:"changes"`
	Comparison *comparisonJSON `json:"comparison"`
}

type changeJSON struct {
	DeliveryID string `json:"delivery_id"`
	State      string `json:"state"`
	StateAt    string `json:"state_at"`
	Applied    bool   `json:"applied"`
	RecordedAt string `json:"recorded_at"`
}

type windowJSON struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type comparisonJSON struct {
	Status   string      `json:"status"`
	Attempts int         `json:"attempts"`
	Before   windowJSON  `json:"before"`
	After    windowJSON  `json:"after"`
	Error    *string     `json:"error"`
	Result   *resultJSON `json:"result"`
}

type measuredJSON struct {
	Requests   float64  `json:"requests"`
	Errors     float64  `json:"errors"`
	ErrorRate  float64  `json:"error_rate"`
	P95Seconds *float64 `json:"p95_seconds"`
}

type queryJSON struct {
	PromQL string `json:"promql"`
	Time   string `json:"time"`
	Range  string `json:"range"`
}

type resultJSON struct {
	Verdict          string               `json:"verdict"`
	ErrorRateVerdict string               `json:"error_rate_verdict"`
	P95Verdict       string               `json:"p95_verdict"`
	Confidence       float64              `json:"confidence"`
	Reasons          []string             `json:"reasons"`
	Before           measuredJSON         `json:"before"`
	After            measuredJSON         `json:"after"`
	Queries          map[string]queryJSON `json:"queries"`
	Routes           []routeJSON          `json:"routes"`
	MethodVersion    int                  `json:"method_version"`
	ComputedAt       string               `json:"computed_at"`
}

// routeJSON is one http_route's evidence. The error rates are derived from
// the counts, like the totals'.
type routeJSON struct {
	Route  string     `json:"route"`
	Before countsJSON `json:"before"`
	After  countsJSON `json:"after"`
}

type countsJSON struct {
	Requests  float64 `json:"requests"`
	Errors    float64 `json:"errors"`
	ErrorRate float64 `json:"error_rate"`
}
