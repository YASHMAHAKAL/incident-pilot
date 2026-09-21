package investigation

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestBoundedReadOnlyRequests(t *testing.T) {
	t.Parallel()
	var seen []string
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet {
			t.Errorf("unexpected method %s", r.Method)
		}
		seen = append(seen, r.URL.String())
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"status":"success"}`)), Header: make(http.Header)}, nil
	})}
	b := Backend{Client: client, Kubernetes: "http://upstream", Prometheus: "http://upstream", Loki: "http://upstream", Tempo: "http://upstream", Token: "test"}
	ctx := context.Background()
	if _, err := b.Deployment(ctx, "payments-api"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Pods(ctx, "payments-api"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Metric(ctx, "payments-api", "error_rate"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.LogQuery(ctx, "payments-api", 5, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Trace(ctx, strings.Repeat("a", 32)); err != nil {
		t.Fatal(err)
	}
	if _, err := b.SearchTraces(ctx, "payments-api", 10); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Trace(ctx, strings.Repeat("b", 31)); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 7 {
		t.Fatalf("got %d requests", len(seen))
	}
	if !strings.Contains(seen[6], "/api/traces/0"+strings.Repeat("b", 31)) {
		t.Errorf("trace ID not normalized: %s", seen[6])
	}
	if !strings.Contains(seen[2], "outcome%21%3D") {
		t.Errorf("wrong error query: %s", seen[2])
	}
	for _, u := range seen {
		if strings.Contains(u, "secret") || strings.Contains(u, "exec") {
			t.Errorf("unsafe path: %s", u)
		}
	}
}

func TestRejectsUnsafeInputsBeforeNetwork(t *testing.T) {
	t.Parallel()
	b := Backend{}
	cases := []struct {
		name string
		run  func() error
	}{
		{"secret deployment", func() error { _, e := b.Deployment(context.Background(), "secrets"); return e }},
		{"namespace traversal", func() error { _, e := b.Pods(context.Background(), "../incidentpilot-system"); return e }},
		{"pod traversal", func() error { _, e := b.Logs(context.Background(), "frontend", "../secret", 10); return e }},
		{"too many log lines", func() error { _, e := b.Logs(context.Background(), "frontend", "frontend-abc", 101); return e }},
		{"arbitrary PromQL", func() error { _, e := b.Metric(context.Background(), "frontend", "{__name__=~\".*\"}"); return e }},
		{"wide Loki window", func() error { _, e := b.LogQuery(context.Background(), "frontend", 61, 10); return e }},
		{"bad trace ID", func() error { _, e := b.Trace(context.Background(), strings.Repeat("z", 32)); return e }},
		{"wide trace window", func() error { _, e := b.SearchTraces(context.Background(), "frontend", 61); return e }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.run(); err == nil {
				t.Fatal("unsafe input accepted")
			}
		})
	}
}

func TestRejectsOversizedUpstream(t *testing.T) {
	t.Parallel()
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(strings.Repeat("x", 65537))), Header: make(http.Header)}, nil
	})}
	b := Backend{Client: client, Prometheus: "http://upstream"}
	if _, err := b.Metric(context.Background(), "frontend", "request_rate"); err == nil {
		t.Fatal("oversized response accepted")
	}
}

func TestKubernetesCredentialFieldsAreScrubbed(t *testing.T) {
	t.Parallel()
	raw := []byte(`{"metadata":{"name":"payments-api","annotations":{"password":"leak"}},"spec":{"template":{"spec":{"containers":[{"name":"app","env":[{"name":"TOKEN","value":"leak"}],"image":"app:v1"}],"volumes":[{"secret":{"secretName":"leak"}}]}}},"status":{"readyReplicas":1}}`)
	clean, err := sanitizeKubernetes(raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"password", "TOKEN", "secretName", "leak"} {
		if strings.Contains(string(clean), secret) {
			t.Errorf("credential field leaked: %s", secret)
		}
	}
	if !strings.Contains(string(clean), "readyReplicas") || !strings.Contains(string(clean), "app:v1") {
		t.Fatal("diagnostic fields lost")
	}
}
