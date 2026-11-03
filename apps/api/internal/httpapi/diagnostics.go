package httpapi

import "net/http"

// schemaDiagnostics names the telemetry tables and skip indexes that are
// missing, and what goes wrong without each. The public status report only
// says that the clickhouse_schema check failed; this is the detail behind
// it, for a logged-in user rather than the server log.
func (a *api) schemaDiagnostics(w http.ResponseWriter, r *http.Request) {
	if err := parseQuery(r).err(); err != nil {
		a.fail(w, r, err, storage)
		return
	}
	report, err := a.telemetry.InspectSchema(r.Context())
	if err != nil {
		a.fail(w, r, err, storage)
		return
	}
	out := schemaReportJSON{
		CheckedAt: timestamp(a.now()),
		Complete:  len(report.Missing) == 0,
		Missing:   make([]schemaItemJSON, 0, len(report.Missing)),
	}
	for _, item := range report.Missing {
		out.Missing = append(out.Missing, schemaItemJSON{
			Kind:   item.Kind,
			Table:  item.Table,
			Name:   item.Name,
			Effect: item.Effect,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

type schemaReportJSON struct {
	CheckedAt string           `json:"checked_at"`
	Complete  bool             `json:"complete"`
	Missing   []schemaItemJSON `json:"missing"`
}

type schemaItemJSON struct {
	Kind   string `json:"kind"`
	Table  string `json:"table"`
	Name   string `json:"name"`
	Effect string `json:"effect"`
}
