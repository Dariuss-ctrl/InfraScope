// Package backend defines the failure kinds shared by the API's storage
// clients (PostgreSQL, ClickHouse, Prometheus). Clients wrap their errors
// with one of these, so the HTTP layer can answer with a predictable status
// without knowing which driver produced the error.
package backend

import "errors"

var (
	// ErrUnavailable means the dependency could not be reached or refused to
	// serve (connection refused, DNS failure, authentication, missing schema).
	ErrUnavailable = errors.New("backend unavailable")

	// ErrTimeout means the dependency was reachable but the operation ran out
	// of time, either on the client deadline or the server's own limit.
	ErrTimeout = errors.New("backend timeout")

	// ErrNotFound means the requested object does not exist.
	ErrNotFound = errors.New("not found")
)
