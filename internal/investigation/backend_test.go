package investigation

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
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
		body := `{"status":"success"}`
		if strings.HasSuffix(r.URL.Path, "/configmaps/orders-config") {
			body = `{"metadata":{"name":"orders-config"},"data":{"order_mode":"normal","ignored":"do-not-return"}}`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	b := Backend{Client: client, Kubernetes: "http://upstream", Prometheus: "http://upstream", Loki: "http://upstream", Tempo: "http://upstream", Token: "test"}
	ctx := context.Background()
	if _, err := b.Deployment(ctx, "payments-api"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Pods(ctx, "payments-api"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.EndpointSlices(ctx, "payments-api"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.OrdersConfig(ctx); err != nil {
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
	if len(seen) != 9 {
		t.Fatalf("got %d requests", len(seen))
	}
	if !strings.Contains(seen[8], "/api/traces/0"+strings.Repeat("b", 31)) {
		t.Errorf("trace ID not normalized: %s", seen[8])
	}
	if !strings.Contains(seen[4], "outcome%21%3D%22success%22") {
		t.Errorf("wrong error query: %s", seen[4])
	}
	if !strings.Contains(seen[2], "labelSelector=kubernetes.io%2Fservice-name%3Dpayments-api") {
		t.Errorf("wrong EndpointSlice selector: %s", seen[2])
	}
	for _, u := range seen {
		if strings.Contains(u, "secret") || strings.Contains(u, "exec") {
			t.Errorf("unsafe path: %s", u)
		}
	}
}

func TestOrdersConfigAndDeploymentExposeOnlyAcceptedDiagnosticFields(t *testing.T) {
	t.Parallel()
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := `{"metadata":{"name":"orders-api","annotations":{"token":"leak"}},"spec":{"template":{"spec":{"containers":[{"name":"orders-api","image":"app:v1","env":[{"name":"DEMO_ORDER_MODE","valueFrom":{"configMapKeyRef":{"name":"orders-config","key":"order_mode"}}},{"name":"PASSWORD","value":"leak"}]}]}}}}`
		if strings.HasSuffix(r.URL.Path, "/configmaps/orders-config") {
			body = `{"metadata":{"name":"orders-config","annotations":{"token":"leak"}},"data":{"order_mode":"unsupported","password":"leak"}}`
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	b := Backend{Client: client, Kubernetes: "http://upstream", Token: "test"}
	deployment, err := b.Deployment(context.Background(), "orders-api")
	if err != nil || !strings.Contains(string(deployment), "diagnosticConfigReferences") || !strings.Contains(string(deployment), "DEMO_ORDER_MODE") || strings.Contains(string(deployment), "PASSWORD") || strings.Contains(string(deployment), "leak") {
		t.Fatalf("unsafe or missing Deployment projection: %s err=%v", deployment, err)
	}
	config, err := b.OrdersConfig(context.Background())
	if err != nil || !strings.Contains(string(config), `"order_mode":"unsupported"`) || strings.Contains(string(config), "password") || strings.Contains(string(config), "leak") {
		t.Fatalf("unsafe or missing ConfigMap projection: %s err=%v", config, err)
	}
}

func TestDeploymentProjectsOnlyScenarioRuntimeConfiguration(t *testing.T) {
	t.Parallel()
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := `{"spec":{"template":{"spec":{"containers":[{"name":"orders-api","env":[{"name":"DEMO_ORDER_CPU_BURN_MS","value":"1000"},{"name":"PASSWORD","value":"leak"}]}]}}}}`
		if strings.HasSuffix(r.URL.Path, "/payments-api") {
			body = `{"spec":{"template":{"spec":{"containers":[{"name":"payments-api","env":[{"name":"DEMO_PAYMENT_MODE","value":"fail"},{"name":"TOKEN","value":"leak"}]}]}}}}`
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	b := Backend{Client: client, Kubernetes: "http://upstream", Token: "test"}
	orders, err := b.Deployment(context.Background(), "orders-api")
	if err != nil || !strings.Contains(string(orders), `"DEMO_ORDER_CPU_BURN_MS"`) || !strings.Contains(string(orders), `"1000"`) || strings.Contains(string(orders), "PASSWORD") || strings.Contains(string(orders), "leak") {
		t.Fatalf("unsafe or missing orders runtime projection: %s err=%v", orders, err)
	}
	payments, err := b.Deployment(context.Background(), "payments-api")
	if err != nil || !strings.Contains(string(payments), `"DEMO_PAYMENT_MODE"`) || !strings.Contains(string(payments), `"fail"`) || strings.Contains(string(payments), "TOKEN") || strings.Contains(string(payments), "leak") {
		t.Fatalf("unsafe or missing payments runtime projection: %s err=%v", payments, err)
	}
}

func TestSuccessLatencyMetricUsesFixedBoundedExpression(t *testing.T) {
	query, err := metricQuery("orders-api", "success_latency_avg")
	if err != nil || !strings.Contains(query, `demo_request_duration_seconds_sum{service_name="orders-api",outcome="success"}[30s]`) || !strings.Contains(query, `demo_request_duration_seconds_count{service_name="orders-api",outcome="success"}[30s]`) {
		t.Fatalf("unexpected latency query %q err=%v", query, err)
	}
}

func TestTraceSearchUsesOnlyFixedFilters(t *testing.T) {
	t.Parallel()
	var queries []string
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		queries = append(queries, r.URL.Query().Get("q"))
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"traces":[]}`)), Header: make(http.Header)}, nil
	})}
	b := Backend{Client: client, Tempo: "http://upstream"}
	now := time.Now()
	if _, err := b.SearchTracesWindowFiltered(context.Background(), "frontend", "error", now.Add(-time.Minute), now); err != nil {
		t.Fatal(err)
	}
	if _, err := b.SearchTracesWindowFiltered(context.Background(), "orders-api", "slow_success", now.Add(-time.Minute), now); err != nil {
		t.Fatal(err)
	}
	if len(queries) != 2 || !strings.Contains(queries[0], "status_code >= 500") || !strings.Contains(queries[1], "duration >= 900ms") {
		t.Fatalf("unexpected fixed trace queries: %v", queries)
	}
	if _, err := b.SearchTracesWindowFiltered(context.Background(), "frontend", `{ true }`, now.Add(-time.Minute), now); err == nil {
		t.Fatal("arbitrary TraceQL filter accepted")
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
		{"endpoint traversal", func() error { _, e := b.EndpointSlices(context.Background(), "../incidentpilot-system"); return e }},
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
