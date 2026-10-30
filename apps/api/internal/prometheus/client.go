// Package prometheus reads service metrics (rate, errors, duration) from
// Prometheus's HTTP API.
package prometheus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
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
	URL string // for example http://prometheus:9090
	// DialTimeout bounds connection attempts.
	DialTimeout time.Duration
	// QueryTimeout is passed to Prometheus as the evaluation timeout; the
	// client deadline adds a short grace period on top.
	QueryTimeout time.Duration
	// Wrap, when set, wraps the HTTP transport (for example with tracing).
	Wrap func(http.RoundTripper) http.RoundTripper
}

// Client queries Prometheus.
type Client struct {
	base         string
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
		http:         &http.Client{Transport: rt},
		queryTimeout: cfg.QueryTimeout,
	}
}

// series is one labelled time series; instant queries carry one point.
type series struct {
	Labels map[string]string
	Points []point
}

type point struct {
	Time  time.Time
	Value float64 // NaN when Prometheus has no defined value
}

type apiResponse struct {
	Status    string          `json:"status"`
	ErrorType string          `json:"errorType"`
	Error     string          `json:"error"`
	Data      json.RawMessage `json:"data"`
}

// instant evaluates query at t.
func (c *Client) instant(ctx context.Context, query string, t time.Time) ([]series, error) {
	v := url.Values{"query": {query}, "time": {unix(t)}}
	var data struct {
		Result []struct {
			Metric map[string]string `json:"metric"`
			Value  [2]any            `json:"value"`
		} `json:"result"`
	}
	if err := c.call(ctx, http.MethodPost, "/api/v1/query", v, &data); err != nil {
		return nil, err
	}
	out := make([]series, 0, len(data.Result))
	for _, r := range data.Result {
		p, err := parsePoint(r.Value)
		if err != nil {
			return nil, err
		}
		out = append(out, series{Labels: r.Metric, Points: []point{p}})
	}
	return out, nil
}

// rangeQuery evaluates query from start to end, every step.
func (c *Client) rangeQuery(ctx context.Context, query string, start, end time.Time, step time.Duration) ([]series, error) {
	v := url.Values{
		"query": {query},
		"start": {unix(start)},
		"end":   {unix(end)},
		"step":  {strconv.FormatFloat(step.Seconds(), 'f', -1, 64)},
	}
	var data struct {
		Result []struct {
			Metric map[string]string `json:"metric"`
			Values [][2]any          `json:"values"`
		} `json:"result"`
	}
	if err := c.call(ctx, http.MethodPost, "/api/v1/query_range", v, &data); err != nil {
		return nil, err
	}
	out := make([]series, 0, len(data.Result))
	for _, r := range data.Result {
		s := series{Labels: r.Metric}
		for _, raw := range r.Values {
			p, err := parsePoint(raw)
			if err != nil {
				return nil, err
			}
			s.Points = append(s.Points, p)
		}
		out = append(out, s)
	}
	return out, nil
}

// labelValues returns the values of label among series matching match
// between start and end.
func (c *Client) labelValues(ctx context.Context, label, match string, start, end time.Time) ([]string, error) {
	v := url.Values{"match[]": {match}, "start": {unix(start)}, "end": {unix(end)}}
	var values []string
	// Unlike the query endpoints, label values accepts only GET.
	err := c.call(ctx, http.MethodGet, "/api/v1/label/"+url.PathEscape(label)+"/values", v, &values)
	return values, err
}

// call sends v to path. Query endpoints use POST, so long PromQL never
// meets URL length limits; endpoints that only accept GET pass it in the URL.
func (c *Client) call(ctx context.Context, method, path string, v url.Values, data any) error {
	ctx, cancel := context.WithTimeout(ctx, c.queryTimeout+2*time.Second)
	defer cancel()
	v.Set("timeout", strconv.FormatFloat(c.queryTimeout.Seconds(), 'f', -1, 64)+"s")

	target, reqBody := c.base+path, io.Reader(nil)
	if method == http.MethodGet {
		target += "?" + v.Encode()
	} else {
		reqBody = strings.NewReader(v.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reqBody)
	if err != nil {
		return err
	}
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("%w: prometheus: %w", backend.ErrTimeout, err)
		}
		return fmt.Errorf("%w: prometheus: %w", backend.ErrUnavailable, err)
	}
	defer resp.Body.Close()

	var body apiResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(&body); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("%w: prometheus: %w", backend.ErrTimeout, err)
		}
		if resp.StatusCode >= 500 {
			return fmt.Errorf("%w: prometheus answered %s", backend.ErrUnavailable, resp.Status)
		}
		return fmt.Errorf("prometheus: decode response: %w", err)
	}
	if body.Status != "success" {
		switch body.ErrorType {
		case "timeout", "canceled":
			return fmt.Errorf("%w: prometheus: %s", backend.ErrTimeout, body.Error)
		case "unavailable":
			return fmt.Errorf("%w: prometheus: %s", backend.ErrUnavailable, body.Error)
		}
		return fmt.Errorf("prometheus: %s: %s", body.ErrorType, body.Error)
	}
	if err := json.Unmarshal(body.Data, data); err != nil {
		return fmt.Errorf("prometheus: decode data: %w", err)
	}
	return nil
}

// parsePoint decodes Prometheus's [<unix seconds>, "<value>"] pair.
func parsePoint(raw [2]any) (point, error) {
	ts, ok := raw[0].(float64)
	if !ok {
		return point{}, errors.New("prometheus: malformed sample timestamp")
	}
	s, ok := raw[1].(string)
	if !ok {
		return point{}, errors.New("prometheus: malformed sample value")
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return point{}, fmt.Errorf("prometheus: sample value %q: %w", s, err)
	}
	sec, frac := math.Modf(ts)
	return point{Time: time.Unix(int64(sec), int64(frac*1e9)).UTC(), Value: v}, nil
}

func unix(t time.Time) string {
	return strconv.FormatFloat(float64(t.UnixNano())/1e9, 'f', 3, 64)
}
