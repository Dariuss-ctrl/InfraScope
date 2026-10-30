// Package clickhouse searches traces and logs in ClickHouse through its HTTP
// interface.
//
// Every user-supplied value is bound server-side as a typed query parameter
// ({name:Type}); SQL text is assembled only from constants. Each query also
// runs with readonly=2 and a server-side time limit, so even a bug in this
// package cannot write data or run forever.
package clickhouse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"infrascope/api/internal/backend"
)

// Config configures a Client.
type Config struct {
	URL      string // HTTP interface, for example http://clickhouse:8123
	User     string
	Password string
	// DialTimeout bounds connection attempts, so an unreachable server
	// fails fast.
	DialTimeout time.Duration
	// QueryTimeout is ClickHouse's max_execution_time for every query; the
	// client deadline adds a short grace period on top.
	QueryTimeout time.Duration
	// Wrap, when set, wraps the HTTP transport (for example with tracing).
	Wrap func(http.RoundTripper) http.RoundTripper
}

// Client queries the telemetry tables.
type Client struct {
	base         string
	user         string
	password     string
	http         *http.Client
	queryTimeout time.Duration
}

// New returns a Client.
func New(cfg Config) *Client {
	dialer := &net.Dialer{Timeout: cfg.DialTimeout}
	var rt http.RoundTripper = &http.Transport{
		DialContext:         dialer.DialContext,
		MaxIdleConnsPerHost: 8,
		IdleConnTimeout:     90 * time.Second,
	}
	if cfg.Wrap != nil {
		rt = cfg.Wrap(rt)
	}
	return &Client{
		base:         strings.TrimRight(cfg.URL, "/"),
		user:         cfg.User,
		password:     cfg.Password,
		http:         &http.Client{Transport: rt},
		queryTimeout: cfg.QueryTimeout,
	}
}

// query runs a SELECT and calls each for every result row, decoded from
// JSONEachRow. params are bound to {name:Type} placeholders.
func (c *Client) query(ctx context.Context, sql string, params map[string]string, each func(json.RawMessage) error) error {
	ctx, cancel := context.WithTimeout(ctx, c.queryTimeout+2*time.Second)
	defer cancel()

	v := url.Values{}
	v.Set("readonly", "2")
	v.Set("max_execution_time", strconv.FormatFloat(c.queryTimeout.Seconds(), 'f', -1, 64))
	v.Set("output_format_json_quote_64bit_integers", "0")
	// Buffer the result, so an error during execution arrives as an HTTP
	// error status instead of being appended to a 200 response.
	v.Set("wait_end_of_query", "1")
	for name, value := range params {
		v.Set("param_"+name, value)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/?"+v.Encode(),
		strings.NewReader(sql+"\nFORMAT JSONEachRow"))
	if err != nil {
		return err
	}
	req.SetBasicAuth(c.user, c.password)

	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("%w: clickhouse: %w", backend.ErrTimeout, err)
		}
		return fmt.Errorf("%w: clickhouse: %w", backend.ErrUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return serverError(resp)
	}

	dec := json.NewDecoder(resp.Body)
	for {
		var row json.RawMessage
		if err := dec.Decode(&row); errors.Is(err, io.EOF) {
			return nil
		} else if err != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("%w: clickhouse: %w", backend.ErrTimeout, err)
			}
			return fmt.Errorf("clickhouse: decode result: %w", err)
		}
		if err := each(row); err != nil {
			return err
		}
	}
}

// ClickHouse exception codes the API treats specially.
const (
	codeUnknownTable        = "60"
	codeUnknownDatabase     = "81"
	codeTimeoutExceeded     = "159"
	codeTooSlow             = "160"
	codeReadonly            = "164"
	codeUnknownUser         = "192"
	codeAuthenticationError = "516"
)

func serverError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	msg := strings.TrimSpace(strings.SplitN(string(body), "\n", 2)[0])
	switch resp.Header.Get("X-ClickHouse-Exception-Code") {
	case codeTimeoutExceeded, codeTooSlow:
		return fmt.Errorf("%w: clickhouse: %s", backend.ErrTimeout, msg)
	case codeUnknownTable, codeUnknownDatabase:
		// The collector creates the schema when it starts; until then the
		// telemetry store is not usable.
		return fmt.Errorf("%w: clickhouse: telemetry schema missing: %s", backend.ErrUnavailable, msg)
	case codeUnknownUser, codeAuthenticationError:
		return fmt.Errorf("%w: clickhouse: %s", backend.ErrUnavailable, msg)
	case codeReadonly:
		// Only a bug in this package could get here: every query is a SELECT.
		return fmt.Errorf("clickhouse: refused a write in read-only mode: %s", msg)
	}
	if resp.StatusCode == http.StatusServiceUnavailable || resp.StatusCode == http.StatusBadGateway {
		return fmt.Errorf("%w: clickhouse: %s", backend.ErrUnavailable, msg)
	}
	return fmt.Errorf("clickhouse: %s", msg)
}

// nanos formats t as the Int64 nanosecond timestamp the queries bind with
// fromUnixTimestamp64Nano, which avoids any time zone interpretation.
func nanos(t time.Time) string {
	return strconv.FormatInt(t.UnixNano(), 10)
}

var quoteEscaper = strings.NewReplacer(`\`, `\\`, `'`, `\'`)

// stringArray formats values as a ClickHouse Array(String) parameter.
func stringArray(values []string) string {
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = "'" + quoteEscaper.Replace(v) + "'"
	}
	return "[" + strings.Join(quoted, ",") + "]"
}
