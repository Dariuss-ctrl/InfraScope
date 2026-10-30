//go:build integration

// Package testenv starts the disposable databases the integration tests run
// against: PostgreSQL, ClickHouse with its schema created by the pinned
// OpenTelemetry Collector (using the repository's own collector config), and
// Prometheus with backfilled history. Nothing is mocked.
package testenv

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"
)

// Pinned images; TestImagesMatchCompose keeps them equal to the stack's.
const (
	PostgresImage   = "postgres:18.6-alpine"
	ClickHouseImage = "clickhouse/clickhouse-server:26.8.36.3"
	CollectorImage  = "otel/opentelemetry-collector-contrib:0.164.0"
	PrometheusImage = "prom/prometheus:v3.15.1"
)

// RepoFile returns the absolute path of a file relative to the repository root.
func RepoFile(rel string) string {
	_, here, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(here), "..", "..", "..", "..", filepath.FromSlash(rel))
}

// Postgres is a running PostgreSQL container.
type Postgres struct {
	URL       string
	Container *testcontainers.DockerContainer
}

// StartPostgres starts an empty PostgreSQL database.
func StartPostgres(ctx context.Context) (*Postgres, error) {
	ctr, err := testcontainers.Run(ctx, PostgresImage,
		testcontainers.WithEnv(map[string]string{
			"POSTGRES_USER":     "infrascope",
			"POSTGRES_PASSWORD": "test-password",
			"POSTGRES_DB":       "infrascope",
		}),
		testcontainers.WithExposedPorts("5432/tcp"),
		// The init-time server logs this once before restarting for real.
		testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").
			WithOccurrence(2).WithStartupTimeout(2*time.Minute)),
	)
	if err != nil {
		return nil, fmt.Errorf("postgres container: %w", err)
	}
	endpoint, err := ctr.PortEndpoint(ctx, "5432/tcp", "")
	if err != nil {
		return nil, err
	}
	return &Postgres{
		URL:       "postgres://infrascope:test-password@" + endpoint + "/infrascope?sslmode=disable",
		Container: ctr,
	}, nil
}

// ClickHouse is a running ClickHouse server whose telemetry schema was
// created by the collector, exactly as in the deployed stack.
type ClickHouse struct {
	URL       string // HTTP interface
	User      string
	Password  string
	Container *testcontainers.DockerContainer
	Collector *testcontainers.DockerContainer
	Network   *testcontainers.DockerNetwork
}

// StartClickHouse starts ClickHouse and the pinned collector with
// telemetry/otel-collector.yaml, and waits until the schema exists.
func StartClickHouse(ctx context.Context) (*ClickHouse, error) {
	nw, err := network.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("docker network: %w", err)
	}
	const user, password = "infrascope", "test-password"
	ch, err := testcontainers.Run(ctx, ClickHouseImage,
		network.WithNetwork([]string{"clickhouse"}, nw),
		testcontainers.WithEnv(map[string]string{
			"CLICKHOUSE_DB":       "otel",
			"CLICKHOUSE_USER":     user,
			"CLICKHOUSE_PASSWORD": password,
		}),
		testcontainers.WithExposedPorts("8123/tcp"),
		testcontainers.WithWaitStrategy(wait.ForHTTP("/ping").WithPort("8123/tcp").WithStartupTimeout(2*time.Minute)),
	)
	if err != nil {
		return nil, fmt.Errorf("clickhouse container: %w", err)
	}
	endpoint, err := ch.PortEndpoint(ctx, "8123/tcp", "http")
	if err != nil {
		return nil, err
	}

	collector, err := testcontainers.Run(ctx, CollectorImage,
		network.WithNetwork([]string{"otel-collector"}, nw),
		testcontainers.WithEnv(map[string]string{
			"CLICKHOUSE_USER":        user,
			"CLICKHOUSE_PASSWORD":    password,
			"TELEMETRY_TTL":          "72h",
			"INFRASCOPE_ENVIRONMENT": "test",
		}),
		testcontainers.WithFiles(testcontainers.ContainerFile{
			HostFilePath:      RepoFile("telemetry/otel-collector.yaml"),
			ContainerFilePath: "/etc/otelcol-contrib/config.yaml",
			FileMode:          0o644,
		}),
		testcontainers.WithCmd("--config=/etc/otelcol-contrib/config.yaml"),
		testcontainers.WithWaitStrategy(wait.ForLog("Everything is ready").WithStartupTimeout(2*time.Minute)),
	)
	if err != nil {
		return nil, fmt.Errorf("collector container: %w", err)
	}

	env := &ClickHouse{URL: endpoint, User: user, Password: password, Container: ch, Collector: collector, Network: nw}
	deadline := time.Now().Add(time.Minute)
	for {
		out, err := env.Query(ctx, `SELECT count() FROM system.tables WHERE database = 'otel'
			AND name IN ('otel_traces', 'otel_traces_trace_id_ts', 'otel_traces_trace_id_ts_mv', 'otel_logs')`)
		if err == nil && strings.TrimSpace(out) == "4" {
			return env, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("telemetry schema not created within a minute (last result %q, %v)", out, err)
		}
		time.Sleep(time.Second)
	}
}

// Query runs sql with full privileges and returns the raw TabSeparated output.
func (c *ClickHouse) Query(ctx context.Context, sql string) (string, error) {
	return c.post(ctx, sql+"\nFORMAT TabSeparated", nil)
}

// QueryRaw runs sql exactly as given (it may carry its own FORMAT clause).
func (c *ClickHouse) QueryRaw(ctx context.Context, sql string) (string, error) {
	return c.post(ctx, sql, nil)
}

// Exec runs a statement with full privileges.
func (c *ClickHouse) Exec(ctx context.Context, sql string) error {
	_, err := c.post(ctx, sql, nil)
	return err
}

// Insert writes rows (one JSON object each) into an otel table.
func (c *ClickHouse) Insert(ctx context.Context, table string, rows []map[string]any) error {
	var body bytes.Buffer
	body.WriteString("INSERT INTO otel." + table + " FORMAT JSONEachRow\n")
	enc := json.NewEncoder(&body)
	for _, r := range rows {
		if err := enc.Encode(r); err != nil {
			return err
		}
	}
	_, err := c.post(ctx, body.String(), url.Values{"date_time_input_format": {"best_effort"}})
	return err
}

func (c *ClickHouse) post(ctx context.Context, body string, params url.Values) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL+"/?"+params.Encode(), strings.NewReader(body))
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(c.User, c.Password)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("clickhouse %d: %s", resp.StatusCode, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// Prometheus is a running Prometheus server.
type Prometheus struct {
	URL       string
	Container *testcontainers.DockerContainer
}

// StartPrometheus starts Prometheus after backfilling openMetrics (an
// OpenMetrics exposition with timestamps) into its TSDB with promtool, so
// tests query deterministic history instead of waiting for live samples.
func StartPrometheus(ctx context.Context, openMetrics []byte) (*Prometheus, error) {
	ctr, err := testcontainers.Run(ctx, PrometheusImage,
		testcontainers.WithFiles(testcontainers.ContainerFile{
			Reader:            bytes.NewReader(openMetrics),
			ContainerFilePath: "/tmp/series.om",
			FileMode:          0o644,
		}),
		testcontainers.WithEntrypoint("/bin/sh", "-c"),
		testcontainers.WithCmd("promtool tsdb create-blocks-from openmetrics /tmp/series.om /prometheus && "+
			"exec /bin/prometheus --config.file=/etc/prometheus/prometheus.yml --storage.tsdb.path=/prometheus"),
		testcontainers.WithExposedPorts("9090/tcp"),
		testcontainers.WithWaitStrategy(wait.ForHTTP("/-/ready").WithPort("9090/tcp").WithStartupTimeout(2*time.Minute)),
	)
	if err != nil {
		return nil, fmt.Errorf("prometheus container: %w", err)
	}
	endpoint, err := ctr.PortEndpoint(ctx, "9090/tcp", "http")
	if err != nil {
		return nil, err
	}
	return &Prometheus{URL: endpoint, Container: ctr}, nil
}
