package shop

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestSlowQueryTracer(t *testing.T) {
	const stmt = "SELECT * FROM orders WHERE customer_email = $1"
	tests := []struct {
		name      string
		threshold time.Duration
		wantLog   bool
	}{
		{"at or over the threshold is logged", 0, true},
		{"under the threshold is not logged", time.Hour, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logs bytes.Buffer
			tracer := &SlowQueryTracer{Threshold: tt.threshold, Logger: slog.New(slog.NewJSONHandler(&logs, nil))}

			ctx := tracer.TraceQueryStart(context.Background(), nil, pgx.TraceQueryStartData{SQL: stmt})
			tracer.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{})

			logged := strings.Contains(logs.String(), `"msg":"slow query"`)
			if logged != tt.wantLog {
				t.Fatalf("logged = %v, want %v; logs: %s", logged, tt.wantLog, logs.String())
			}
			if logged && !strings.Contains(logs.String(), `"db.query.text":"`+stmt+`"`) {
				t.Errorf("log record does not include the statement: %s", logs.String())
			}
		})
	}
}

func TestSlowQueryTracerIgnoresQueriesItDidNotStart(t *testing.T) {
	var logs bytes.Buffer
	tracer := &SlowQueryTracer{Logger: slog.New(slog.NewJSONHandler(&logs, nil))}
	tracer.TraceQueryEnd(context.Background(), nil, pgx.TraceQueryEndData{})
	if logs.Len() != 0 {
		t.Errorf("logged without a matching start: %s", logs.String())
	}
}
