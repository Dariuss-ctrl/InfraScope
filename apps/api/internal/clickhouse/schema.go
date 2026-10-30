package clickhouse

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// expectedSchema lists the tables and skip indexes that the query patterns
// in SPEC §7.1 rely on. The indexes affect speed, not correctness.
var expectedSchema = []struct {
	table   string
	indexes []string
}{
	{"otel_traces", []string{"idx_trace_id", "idx_duration"}},
	{"otel_traces_trace_id_ts", []string{"idx_trace_id"}},
	{"otel_logs", []string{"idx_trace_id", "idx_lower_body"}},
}

// CheckSchema verifies that the telemetry tables and the skip indexes the
// queries rely on exist, and names whatever is missing. Searches still
// return correct results without the indexes, only more slowly, so the
// status endpoint reports a failed check as a degradation, not an outage.
func (c *Client) CheckSchema(ctx context.Context) error {
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
		return err
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
		return err
	}

	var missing []string
	for _, want := range expectedSchema {
		if !tables[want.table] {
			missing = append(missing, "table otel."+want.table)
			continue
		}
		for _, idx := range want.indexes {
			if !indexes[want.table+"."+idx] {
				missing = append(missing, "index otel."+want.table+"."+idx)
			}
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("telemetry schema incomplete: missing %s", strings.Join(missing, ", "))
	}
	return nil
}
