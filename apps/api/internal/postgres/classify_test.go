package postgres

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"infrascope/api/internal/backend"
)

func TestClassify(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want error // nil: passed through as an internal error
	}{
		{"no rows", pgx.ErrNoRows, backend.ErrNotFound},
		{"deadline", fmt.Errorf("query: %w", context.DeadlineExceeded), backend.ErrTimeout},
		{"admin shutdown", &pgconn.PgError{Code: "57P01"}, backend.ErrUnavailable},
		{"too many connections", &pgconn.PgError{Code: "53300"}, backend.ErrUnavailable},
		{"connection exception", &pgconn.PgError{Code: "08006"}, backend.ErrUnavailable},
		{"connection dropped", fmt.Errorf("read: %w", io.ErrUnexpectedEOF), backend.ErrUnavailable},
		{"unique violation", &pgconn.PgError{Code: "23505"}, nil},
		{"syntax error", &pgconn.PgError{Code: "42601"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classify(tt.err)
			if tt.want == nil {
				for _, kind := range []error{backend.ErrNotFound, backend.ErrTimeout, backend.ErrUnavailable} {
					if errors.Is(got, kind) {
						t.Errorf("classify = %v, want an internal error", got)
					}
				}
				return
			}
			if !errors.Is(got, tt.want) {
				t.Errorf("classify = %v, want %v", got, tt.want)
			}
		})
	}
	if classify(nil) != nil {
		t.Error("classify(nil) != nil")
	}
}
