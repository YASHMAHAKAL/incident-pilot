package investigation

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestChangeSourcesAreBoundedReadOnlyAndSanitized(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC().Truncate(time.Second)
	revision := strings.Repeat("a", 40)
	deployedAt := now.Add(-5 * time.Minute)
	oldDeployment := now.Add(-3 * time.Hour)
	var requests atomic.Int64
	var mu sync.Mutex
	var seen []string
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests.Add(1)
		if r.Method != http.MethodGet {
			t.Errorf("unexpected method %s", r.Method)
		}
		mu.Lock()
		seen = append(seen, r.URL.String())
		mu.Unlock()
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v1/applications/"):
			if r.Header.Get("Authorization") != "Bearer argo-secret" || r.URL.Query().Get("project") != "default" {
				t.Errorf("bad Argo request headers or project")
			}
			body := fmt.Sprintf(`{"metadata":{"name":"incidentpilot-demo","annotations":{"token":"must-not-leak"}},"spec":{"project":"default","source":{"repoURL":"https://user:password@example.invalid/repo","targetRevision":"main"}},"status":{"sync":{"status":"Synced","revision":%q},"health":{"status":"Healthy"},"history":[{"id":2,"revision":%q,"deployedAt":%q},{"id":1,"revision":%q,"deployedAt":%q}]}}`, revision, revision, deployedAt.Format(time.RFC3339), strings.Repeat("b", 40), oldDeployment.Format(time.RFC3339))
			return jsonResponse(body), nil
		case strings.HasPrefix(r.URL.Path, "/repos/example/incident-pilot/commits/"):
			if r.Header.Get("Authorization") != "Bearer github-secret" || r.Header.Get("Accept") != "application/vnd.github+json" || r.Header.Get("X-GitHub-Api-Version") != "2026-03-10" {
				t.Errorf("bad GitHub request headers")
			}
			if r.URL.Query().Get("per_page") != "30" {
				t.Errorf("GitHub result was not bounded")
			}
			patch := "- password=must-not-leak\n+ memory: 48Mi"
			body := fmt.Sprintf(`{"sha":%q,"commit":{"message":"limit memory token=must-not-leak","author":{"date":%q,"email":"private@example.invalid"},"committer":{"date":%q}},"files":[{"filename":"deploy/kind/30-demo.yaml","status":"modified","additions":1,"deletions":1,"changes":2,"patch":%q}]}`, revision, now.Add(-10*time.Minute).Format(time.RFC3339), now.Add(-9*time.Minute).Format(time.RFC3339), patch)
			return jsonResponse(body), nil
		default:
			return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader(`{}`)), Header: make(http.Header)}, nil
		}
	})}

	b := Backend{Client: client, ArgoCD: "https://argo.example", ArgoCDToken: "argo-secret", ArgoCDApplication: "incidentpilot-demo", ArgoCDProject: "default", GitHub: "https://api.github.example", GitHubToken: "github-secret", GitHubRepository: "example/incident-pilot"}
	if err := b.ValidateChangeSources(); err != nil {
		t.Fatal(err)
	}
	start, end := now.Add(-10*time.Minute), now
	application, err := b.argoApplication(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	history, err := b.argoRevisionHistory(context.Background(), start, end)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := b.githubCommit(context.Background(), revision, start, end)
	if err != nil {
		t.Fatal(err)
	}
	diff, err := b.githubDiff(context.Background(), revision, start, end)
	if err != nil {
		t.Fatal(err)
	}
	all := string(application) + string(history) + string(commit) + string(diff)
	for _, leaked := range []string{"argo-secret", "github-secret", "must-not-leak", "private@example.invalid", "user:password"} {
		if strings.Contains(all, leaked) {
			t.Fatalf("sensitive value %q leaked in %s", leaked, all)
		}
	}
	var projected struct {
		Deployments []struct {
			Revision string `json:"revision"`
		} `json:"deployments"`
	}
	if json.Unmarshal(history, &projected) != nil || len(projected.Deployments) != 1 || projected.Deployments[0].Revision != revision {
		t.Fatalf("history was not windowed: %s", history)
	}
	if requests.Load() != 6 { // app + history + (Argo validation + GitHub) for each GitHub read
		t.Fatalf("unexpected request count %d: %v", requests.Load(), seen)
	}
	if !strings.Contains(string(diff), "48Mi") || !strings.Contains(string(diff), "redacted") {
		t.Fatalf("diff lost diagnostic content or redaction: %s", diff)
	}
}

func TestChangeSourcesRejectUnsafeConfigurationAndRevision(t *testing.T) {
	t.Parallel()
	cases := []Backend{
		{ArgoCD: "https://argo.example", ArgoCDToken: "token", ArgoCDApplication: "../secret", ArgoCDProject: "default", GitHub: "https://api.github.com", GitHubRepository: "owner/repo"},
		{ArgoCD: "https://argo.example", ArgoCDToken: "token", ArgoCDApplication: "app", ArgoCDProject: "default", GitHub: "https://token@example.com", GitHubRepository: "owner/repo"},
		{ArgoCD: "https://argo.example", ArgoCDToken: "token", ArgoCDApplication: "app", ArgoCDProject: "default", GitHub: "https://api.github.com", GitHubRepository: "../repo"},
		{ArgoCD: "http://argo.example", ArgoCDToken: "token", ArgoCDApplication: "app", ArgoCDProject: "default", GitHub: "https://api.github.com", GitHubRepository: "owner/repo"},
	}
	for _, backend := range cases {
		if err := backend.ValidateChangeSources(); err == nil {
			t.Fatalf("unsafe config accepted: %+v", backend)
		}
	}
	local := Backend{ArgoCD: "http://argocd-server.argocd.svc.cluster.local", ArgoCDToken: "token", ArgoCDApplication: "app", ArgoCDProject: "default", GitHub: "https://api.github.com", GitHubRepository: "owner/repo"}
	if err := local.ValidateChangeSources(); err != nil {
		t.Fatalf("in-cluster HTTP endpoint rejected: %v", err)
	}
	var calls atomic.Int64
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, fmt.Errorf("should not be called")
	})}
	b := Backend{Client: client, ArgoCD: "https://argo.example", ArgoCDToken: "token", ArgoCDApplication: "app", ArgoCDProject: "default", GitHub: "https://api.github.com", GitHubRepository: "owner/repo"}
	now := time.Now().UTC()
	if _, err := b.githubCommit(context.Background(), "main", now.Add(-time.Minute), now); err == nil {
		t.Fatal("non-commit revision accepted")
	}
	if calls.Load() != 0 {
		t.Fatal("unsafe revision reached the network")
	}
}

func TestArgoMultiSourceApplicationIsRejected(t *testing.T) {
	t.Parallel()
	raw := json.RawMessage(`{"metadata":{"name":"app"},"spec":{"project":"default","sources":[{"repoURL":"one"},{"repoURL":"two"}]},"status":{}}`)
	if _, err := projectArgoApplication(raw, "app", "default"); err == nil || !strings.Contains(err.Error(), "multi-source") {
		t.Fatalf("multi-source application was accepted: %v", err)
	}
}

func TestGitHubRevisionMustAppearInBoundedArgoHistory(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC().Truncate(time.Second)
	allowed := strings.Repeat("a", 40)
	requested := strings.Repeat("b", 40)
	var githubCalls atomic.Int64
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.HasPrefix(r.URL.Path, "/api/v1/applications/") {
			body := fmt.Sprintf(`{"metadata":{"name":"app"},"spec":{"project":"default"},"status":{"history":[{"id":1,"revision":%q,"deployedAt":%q}]}}`, allowed, now.Add(-time.Minute).Format(time.RFC3339))
			return jsonResponse(body), nil
		}
		githubCalls.Add(1)
		return jsonResponse(`{}`), nil
	})}
	b := Backend{Client: client, ArgoCD: "https://argo.example", ArgoCDToken: "token", ArgoCDApplication: "app", ArgoCDProject: "default", GitHub: "https://api.github.example", GitHubRepository: "owner/repo"}
	if _, err := b.githubCommit(context.Background(), requested, now.Add(-5*time.Minute), now); err == nil || !strings.Contains(err.Error(), "not present") {
		t.Fatalf("uncorrelated revision accepted: %v", err)
	}
	if githubCalls.Load() != 0 {
		t.Fatal("uncorrelated revision reached GitHub")
	}
}

func jsonResponse(body string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}
