package investigation

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"incidentpilot/internal/remediation"
)

func TestRemediationClientUsesFixedAuthenticatedBoundary(t *testing.T) {
	token := strings.Repeat("r", 24)
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodPost || request.URL.Path != "/api/v1/remediations" || request.Header.Get("Authorization") != "Bearer "+token || request.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected request: %s %s", request.Method, request.URL.Path)
		}
		body, _ := json.Marshal(remediation.Result{
			Proposal: remediation.Proposal{ID: "00000000-0000-0000-0000-000000000001"},
			Decision: remediation.PolicyDecision{ID: "00000000-0000-0000-0000-000000000002"},
			Audit:    remediation.AuditEvent{ID: "00000000-0000-0000-0000-000000000003"},
		})
		return &http.Response{StatusCode: http.StatusCreated, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
	})
	client, err := NewRemediationClient(&http.Client{Transport: transport}, "https://api.example/api/v1/remediations", token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Request(context.Background(), remediation.Request{}); err != nil {
		t.Fatal(err)
	}
}

func TestRemediationClientRejectsUnsafeConfiguration(t *testing.T) {
	for _, endpoint := range []string{"https://example.com/other", "https://user@example.com/api/v1/remediations", "file:///api/v1/remediations", "https://example.com/api/v1/remediations?next=x"} {
		if _, err := NewRemediationClient(http.DefaultClient, endpoint, strings.Repeat("r", 24)); err == nil {
			t.Fatalf("accepted endpoint %q", endpoint)
		}
	}
}
