package pipeline

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

// budget is how long each signal may take to become queryable. It is a test
// timeout (AC-1.4), not a latency claim: measured lag lives in benchmark reports.
const budget = 60 * time.Second

// TestRequestIsObservable is the end-to-end check of FR-1, made through the
// InfraScope API exactly as the dashboard would (SPEC AC-1.4): one request to
// the demo service becomes a trace with an HTTP server span and a database
// span, log records that carry the same trace ID, and request metrics for
// the service.
func TestRequestIsObservable(t *testing.T) {
	s := stackFromEnv(t)
	s.login(t)
	sent := time.Now().UTC()
	traceID := s.createOrder(t)
	t.Logf("created an order in trace %s", traceID)

	eventually(t, "trace with an HTTP server span and a database span", func() error {
		var trace struct {
			Spans []struct {
				Name         string            `json:"name"`
				ParentSpanID *string           `json:"parent_span_id"`
				Attributes   map[string]string `json:"attributes"`
			} `json:"spans"`
		}
		if err := s.get("/api/v1/traces/"+traceID, &trace); err != nil {
			return err
		}
		var root, db bool
		for _, span := range trace.Spans {
			root = root || (span.Name == "POST /orders" && span.ParentSpanID == nil)
			db = db || span.Attributes["db.system.name"] != ""
		}
		if !root || !db {
			return fmt.Errorf("want a root span %q and a database span; have %+v", "POST /orders", trace.Spans)
		}
		return nil
	})

	eventually(t, "log records carrying the trace ID", func() error {
		var page struct {
			Logs []struct {
				Body    string  `json:"body"`
				TraceID *string `json:"trace_id"`
			} `json:"logs"`
		}
		q := url.Values{
			"trace_id": {traceID},
			"from":     {sent.Add(-time.Minute).Format(time.RFC3339)},
			"to":       {time.Now().UTC().Format(time.RFC3339)},
		}
		if err := s.get("/api/v1/logs?"+q.Encode(), &page); err != nil {
			return err
		}
		if len(page.Logs) == 0 {
			return errors.New("no log records with this trace ID yet")
		}
		for _, l := range page.Logs {
			if l.TraceID == nil || *l.TraceID != traceID {
				return fmt.Errorf("record %q is not linked to the trace", l.Body)
			}
		}
		return nil
	})

	// A rate needs a counter that rises between two samples. On a fresh stack
	// the order above is the service's first request, so its series starts
	// at 1 and stays there, and the rate is zero. Keep a little read-only
	// traffic flowing while waiting.
	eventually(t, "request metrics for the service", func() error {
		if err := s.browse(); err != nil {
			return err
		}
		var list struct {
			Services []struct {
				Name        string   `json:"name"`
				RequestRate *float64 `json:"request_rate"`
			} `json:"services"`
		}
		if err := s.get("/api/v1/services", &list); err != nil {
			return err
		}
		for _, svc := range list.Services {
			if svc.Name == "demo-service" && svc.RequestRate != nil && *svc.RequestRate > 0 {
				return nil
			}
		}
		return fmt.Errorf("demo-service has no request rate yet: %+v", list.Services)
	})
}

// stack is a running deployment, reached the way a browser reaches it:
// through the dashboard's nginx.
type stack struct {
	apiURL, demoURL string
	user, password  string
	client          *http.Client
}

func stackFromEnv(t *testing.T) stack {
	t.Helper()
	if os.Getenv("INFRASCOPE_PIPELINE") == "" {
		t.Skip("needs a running stack: make up, then make test-pipeline")
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return stack{
		apiURL:   envOr("DASHBOARD_URL", "http://localhost:"+envOr("DASHBOARD_PORT", "8080")),
		demoURL:  envOr("DEMO_URL", "http://localhost:"+envOr("DEMO_SERVICE_PORT", "8082")),
		user:     envOr("INFRASCOPE_ADMIN_USER", "admin"),
		password: os.Getenv("INFRASCOPE_ADMIN_PASSWORD"),
		client:   &http.Client{Timeout: 15 * time.Second, Jar: jar},
	}
}

func (s stack) login(t *testing.T) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"username": s.user, "password": s.password})
	resp, err := s.client.Post(s.apiURL+"/api/v1/auth/login", "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("login = %d: %s (are INFRASCOPE_ADMIN_USER/PASSWORD from .env exported?)", resp.StatusCode, b)
	}
}

var traceIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// createOrder places an order for a unique customer and returns the trace ID
// the demo service reports in X-Trace-Id.
func (s stack) createOrder(t *testing.T) string {
	t.Helper()
	body := fmt.Sprintf(`{"customer_email":"pipeline-%d@example.com","product_id":1,"quantity":1}`, time.Now().UnixNano())
	resp, err := s.client.Post(s.demoURL+"/orders", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /orders: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST /orders = %d: %s", resp.StatusCode, b)
	}
	traceID := resp.Header.Get("X-Trace-Id")
	if !traceIDPattern.MatchString(traceID) {
		t.Fatalf("X-Trace-Id = %q, want 32 lowercase hex characters", traceID)
	}
	return traceID
}

// browse sends one read-only request to the demo service.
func (s stack) browse() error {
	resp, err := s.client.Get(s.demoURL + "/products/1")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET /products/1 = %d", resp.StatusCode)
	}
	return nil
}

// get calls the InfraScope API and decodes a 200 response into out. A 404 is
// "not there yet" for the polling loop; other statuses are reported as is.
func (s stack) get(path string, out any) error {
	resp, err := s.client.Get(s.apiURL + path)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	switch resp.StatusCode {
	case http.StatusOK:
		return json.Unmarshal(body, out)
	case http.StatusNotFound:
		return errors.New("not ingested yet")
	}
	return fmt.Errorf("GET %s = %d: %s", path, resp.StatusCode, strings.TrimSpace(string(body)))
}

// eventually retries check until it succeeds or the budget runs out.
func eventually(t *testing.T, what string, check func() error) {
	t.Helper()
	deadline := time.Now().Add(budget)
	for {
		err := check()
		if err == nil {
			t.Logf("observed: %s", what)
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: not observed within %v: %v", what, budget, err)
		}
		time.Sleep(2 * time.Second)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
