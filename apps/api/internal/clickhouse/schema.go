package clickhouse

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// SchemaItem is a table or skip index that the queries rely on.
type SchemaItem struct {
	Kind   string // "table" or "index"
	Table  string // with its database, for example otel.otel_logs
	Name   string // the index; for a table, the table itself
	Effect string // what goes wrong while it is missing
}

// SchemaReport is what InspectSchema found missing; empty when nothing is.
type SchemaReport struct {
	Missing []SchemaItem
}

type expectedIndex struct{ name, effect string }

// expectedSchema lists the tables and skip indexes that the query patterns
// in SPEC §7.1 rely on, and what goes wrong without each. A missing table
// breaks the features that read it; a missing index only slows them down.
var expectedSchema = []struct {
	table   string
	effect  string
	indexes []expectedIndex
}{
	{"otel_traces", "Trace search and trace pages fail.", []expectedIndex{
		{"idx_trace_id", "Opening a trace is slower: its spans are found without the trace ID index."},
		{"idx_duration", "Trace searches by duration are slower."},
	}},
	{"otel_traces_trace_id_ts", "Opening a trace fails: its time bounds are looked up in this table.", []expectedIndex{
		{"idx_trace_id", "Opening a trace is slower: its time bounds are found without an index."},
	}},
	{"otel_logs", "Log search fails.", []expectedIndex{
		{"idx_trace_id", "Finding the logs written during a trace is slower."},
		{"idx_lower_body", "Text search in logs is slower: every message in the time range is read."},
	}},
}

// InspectSchema reports which of the tables and skip indexes the queries
// rely on are missing, with what goes wrong without each, so a degraded
// schema can be diagnosed without reading server logs.
func (c *Client) InspectSchema(ctx context.Context) (SchemaReport, error) {
	tables := map[string]bool{}
	err := c.query(ctx, `SELECT name FROM system.tables WHERE database = 'otel'`, nil,
		func(row json.RawMessage) error {
			var r struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(row, &r); err != nil {
				return err
			}
			tables[r.Name] = true
			return nil
		})
	if err != nil {
		return SchemaReport{}, err
	}

	indexes := map[string]bool{}
	err = c.query(ctx, `SELECT table, name FROM system.data_skipping_indices WHERE database = 'otel'`, nil,
		func(row json.RawMessage) error {
			var r struct {
				Table string `json:"table"`
				Name  string `json:"name"`
			}
			if err := json.Unmarshal(row, &r); err != nil {
				return err
			}
			indexes[r.Table+"."+r.Name] = true
			return nil
		})
	if err != nil {
		return SchemaReport{}, err
	}

	var report SchemaReport
	for _, want := range expectedSchema {
		table := "otel." + want.table
		if !tables[want.table] {
			report.Missing = append(report.Missing, SchemaItem{Kind: "table", Table: table, Name: want.table, Effect: want.effect})
			continue
		}
		for _, idx := range want.indexes {
			if !indexes[want.table+"."+idx.name] {
				report.Missing = append(report.Missing, SchemaItem{Kind: "index", Table: table, Name: idx.name, Effect: idx.effect})
			}
		}
	}
	return report, nil
}

// CheckSchema fails when anything InspectSchema looks for is missing, and
// names it. Searches still return correct results without the indexes, only
// more slowly, so the status endpoint reports a failed check as a
// degradation, not an outage.
func (c *Client) CheckSchema(ctx context.Context) error {
	report, err := c.InspectSchema(ctx)
	if err != nil {
		return err
	}
	if len(report.Missing) == 0 {
		return nil
	}
	missing := make([]string, 0, len(report.Missing))
	for _, item := range report.Missing {
		if item.Kind == "table" {
			missing = append(missing, "table "+item.Table)
		} else {
			missing = append(missing, "index "+item.Table+"."+item.Name)
		}
	}
	return fmt.Errorf("telemetry schema incomplete: missing %s", strings.Join(missing, ", "))
}
