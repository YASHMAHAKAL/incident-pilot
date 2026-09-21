package investigation

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

var (
	configuredName      = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]{0,61}[a-z0-9])?$`)
	repositoryName      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,99}/[A-Za-z0-9][A-Za-z0-9_.-]{0,99}$`)
	revisionName        = regexp.MustCompile(`^[0-9a-fA-F]{7,64}$`)
	sensitiveChangeText = regexp.MustCompile(`(?i)\b(password|token|authorization|secret)\b\s*[:=]\s*[^\s,;}]+`)
)

// ValidateChangeSources keeps every change lookup pinned to administrator
// configuration. An empty configuration disables Phase 8 without weakening
// the existing investigation path.
func (b Backend) ValidateChangeSources() error {
	argoSet := b.ArgoCD != "" || b.ArgoCDToken != "" || b.ArgoCDApplication != "" || b.ArgoCDProject != ""
	githubSet := b.GitHub != "" || b.GitHubRepository != "" || b.GitHubToken != ""
	if !argoSet && !githubSet {
		return nil
	}
	if b.ArgoCD == "" || b.ArgoCDToken == "" || !configuredName.MatchString(b.ArgoCDApplication) || !configuredName.MatchString(b.ArgoCDProject) {
		return errors.New("Argo CD change source requires a URL, token, application, and project")
	}
	if b.GitHub == "" || !validRepository(b.GitHubRepository) {
		return errors.New("GitHub change source requires an API URL and owner/repository")
	}
	for _, raw := range []string{b.ArgoCD, b.GitHub} {
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return errors.New("change source URLs must be fixed http(s) base URLs")
		}
		if u.Scheme == "http" && !localHTTPHost(u.Hostname()) {
			return errors.New("unencrypted change source URLs are allowed only for loopback or in-cluster services")
		}
	}
	return nil
}

func localHTTPHost(host string) bool {
	host = strings.ToLower(host)
	return host == "localhost" || host == "127.0.0.1" || host == "::1" || strings.HasSuffix(host, ".svc") || strings.HasSuffix(host, ".svc.cluster.local")
}

func validRepository(repository string) bool {
	if !repositoryName.MatchString(repository) {
		return false
	}
	for _, part := range strings.Split(repository, "/") {
		if part == "." || part == ".." || strings.Contains(part, "..") {
			return false
		}
	}
	return true
}

func (b Backend) changeSourcesConfigured() bool {
	return b.ValidateChangeSources() == nil && b.ArgoCD != ""
}

func (b Backend) argoApplication(ctx context.Context) (json.RawMessage, error) {
	if !b.changeSourcesConfigured() {
		return nil, errors.New("change sources are not configured")
	}
	headers := make(http.Header)
	headers.Set("Authorization", "Bearer "+b.ArgoCDToken)
	raw, err := b.getJSON(ctx, b.ArgoCD, "/api/v1/applications/"+url.PathEscape(b.ArgoCDApplication), url.Values{"project": {b.ArgoCDProject}}, headers, 256<<10)
	if err != nil {
		return nil, err
	}
	return projectArgoApplication(raw, b.ArgoCDApplication, b.ArgoCDProject)
}

func (b Backend) argoRevisionHistory(ctx context.Context, start, end time.Time) (json.RawMessage, error) {
	if err := ValidateWindow(start, end); err != nil {
		return nil, err
	}
	if !b.changeSourcesConfigured() {
		return nil, errors.New("change sources are not configured")
	}
	headers := make(http.Header)
	headers.Set("Authorization", "Bearer "+b.ArgoCDToken)
	raw, err := b.getJSON(ctx, b.ArgoCD, "/api/v1/applications/"+url.PathEscape(b.ArgoCDApplication), url.Values{"project": {b.ArgoCDProject}}, headers, 256<<10)
	if err != nil {
		return nil, err
	}
	return projectArgoHistory(raw, b.ArgoCDApplication, b.ArgoCDProject, start, end)
}

type argoApplicationResponse struct {
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Spec struct {
		Project string `json:"project"`
		Source  struct {
			TargetRevision string `json:"targetRevision"`
		} `json:"source"`
		Sources []json.RawMessage `json:"sources"`
	} `json:"spec"`
	Status struct {
		Sync struct {
			Status   string `json:"status"`
			Revision string `json:"revision"`
		} `json:"sync"`
		Health struct {
			Status string `json:"status"`
		} `json:"health"`
		History []struct {
			ID         int64     `json:"id"`
			Revision   string    `json:"revision"`
			DeployedAt time.Time `json:"deployedAt"`
		} `json:"history"`
	} `json:"status"`
}

func decodeArgo(raw json.RawMessage, application string) (argoApplicationResponse, error) {
	var app argoApplicationResponse
	if err := json.Unmarshal(raw, &app); err != nil {
		return app, errors.New("Argo CD returned an invalid application")
	}
	if app.Metadata.Name != application {
		return app, errors.New("Argo CD returned an unexpected application")
	}
	return app, nil
}

func projectArgoApplication(raw json.RawMessage, application, project string) (json.RawMessage, error) {
	app, err := decodeArgo(raw, application)
	if err != nil {
		return nil, err
	}
	if app.Spec.Project != "" && app.Spec.Project != project {
		return nil, errors.New("Argo CD returned an unexpected project")
	}
	if len(app.Spec.Sources) > 0 {
		return nil, errors.New("multi-source Argo CD applications are not supported")
	}
	return json.Marshal(map[string]any{
		"application":      application,
		"project":          project,
		"sync_status":      app.Status.Sync.Status,
		"health_status":    app.Status.Health.Status,
		"current_revision": app.Status.Sync.Revision,
		"target_revision":  app.Spec.Source.TargetRevision,
	})
}

func projectArgoHistory(raw json.RawMessage, application, project string, start, end time.Time) (json.RawMessage, error) {
	app, err := decodeArgo(raw, application)
	if err != nil {
		return nil, err
	}
	if (app.Spec.Project != "" && app.Spec.Project != project) || len(app.Spec.Sources) > 0 {
		return nil, errors.New("Argo CD returned an unsupported application")
	}
	type deployment struct {
		ID         int64     `json:"id"`
		Revision   string    `json:"revision"`
		DeployedAt time.Time `json:"deployed_at"`
	}
	deployments := make([]deployment, 0, len(app.Status.History))
	lowerBound := start.Add(-time.Hour)
	for _, item := range app.Status.History {
		revision := item.Revision
		if !revisionName.MatchString(revision) || item.DeployedAt.IsZero() || item.DeployedAt.Before(lowerBound) || item.DeployedAt.After(end.Add(30*time.Second)) {
			continue
		}
		deployments = append(deployments, deployment{ID: item.ID, Revision: strings.ToLower(revision), DeployedAt: item.DeployedAt.UTC()})
	}
	sort.Slice(deployments, func(i, j int) bool { return deployments[i].DeployedAt.After(deployments[j].DeployedAt) })
	if len(deployments) > 20 {
		deployments = deployments[:20]
	}
	return json.Marshal(map[string]any{"application": application, "deployments": deployments})
}

func (b Backend) githubCommit(ctx context.Context, revision string, start, end time.Time) (json.RawMessage, error) {
	raw, err := b.githubRevision(ctx, revision, start, end)
	if err != nil {
		return nil, err
	}
	return projectGitHubCommit(raw, b.GitHubRepository)
}

func (b Backend) githubDiff(ctx context.Context, revision string, start, end time.Time) (json.RawMessage, error) {
	raw, err := b.githubRevision(ctx, revision, start, end)
	if err != nil {
		return nil, err
	}
	return projectGitHubDiff(raw, b.GitHubRepository)
}

func (b Backend) githubRevision(ctx context.Context, revision string, start, end time.Time) (json.RawMessage, error) {
	if !b.changeSourcesConfigured() {
		return nil, errors.New("change sources are not configured")
	}
	if !revisionName.MatchString(revision) {
		return nil, errors.New("revision must be a hexadecimal commit ID")
	}
	history, err := b.argoRevisionHistory(ctx, start, end)
	if err != nil {
		return nil, err
	}
	if !historyContainsRevision(history, revision) {
		return nil, errors.New("revision is not present in bounded Argo CD history")
	}
	parts := strings.Split(b.GitHubRepository, "/")
	headers := make(http.Header)
	headers.Set("Accept", "application/vnd.github+json")
	headers.Set("X-GitHub-Api-Version", "2026-03-10")
	if b.GitHubToken != "" {
		headers.Set("Authorization", "Bearer "+b.GitHubToken)
	}
	path := "/repos/" + url.PathEscape(parts[0]) + "/" + url.PathEscape(parts[1]) + "/commits/" + strings.ToLower(revision)
	raw, err := b.getJSON(ctx, b.GitHub, path, url.Values{"page": {"1"}, "per_page": {"30"}}, headers, 384<<10)
	if err != nil {
		return nil, err
	}
	commit, err := decodeGitHub(raw)
	if err != nil || !strings.HasPrefix(strings.ToLower(commit.SHA), strings.ToLower(revision)) {
		return nil, errors.New("GitHub returned an unexpected revision")
	}
	return raw, nil
}

func historyContainsRevision(raw json.RawMessage, revision string) bool {
	var history struct {
		Deployments []struct {
			Revision string `json:"revision"`
		} `json:"deployments"`
	}
	if json.Unmarshal(raw, &history) != nil {
		return false
	}
	want := strings.ToLower(revision)
	for _, deployment := range history.Deployments {
		if strings.ToLower(deployment.Revision) == want {
			return true
		}
	}
	return false
}

type githubCommitResponse struct {
	SHA    string `json:"sha"`
	Commit struct {
		Message string `json:"message"`
		Author  struct {
			Date time.Time `json:"date"`
		} `json:"author"`
		Committer struct {
			Date time.Time `json:"date"`
		} `json:"committer"`
	} `json:"commit"`
	Files []struct {
		Filename  string `json:"filename"`
		Status    string `json:"status"`
		Additions int    `json:"additions"`
		Deletions int    `json:"deletions"`
		Changes   int    `json:"changes"`
		Patch     string `json:"patch"`
	} `json:"files"`
}

func decodeGitHub(raw json.RawMessage) (githubCommitResponse, error) {
	var commit githubCommitResponse
	if err := json.Unmarshal(raw, &commit); err != nil || !revisionName.MatchString(commit.SHA) {
		return commit, errors.New("GitHub returned an invalid commit")
	}
	return commit, nil
}

func projectGitHubCommit(raw json.RawMessage, repository string) (json.RawMessage, error) {
	commit, err := decodeGitHub(raw)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{
		"repository":   repository,
		"sha":          strings.ToLower(commit.SHA),
		"message":      sanitizeChangeText(truncateText(commit.Commit.Message, 4096)),
		"authored_at":  commit.Commit.Author.Date.UTC(),
		"committed_at": commit.Commit.Committer.Date.UTC(),
	})
}

func projectGitHubDiff(raw json.RawMessage, repository string) (json.RawMessage, error) {
	commit, err := decodeGitHub(raw)
	if err != nil {
		return nil, err
	}
	type file struct {
		Filename  string `json:"filename"`
		Status    string `json:"status"`
		Additions int    `json:"additions"`
		Deletions int    `json:"deletions"`
		Changes   int    `json:"changes"`
		Patch     string `json:"patch,omitempty"`
	}
	files := make([]file, 0, len(commit.Files))
	for _, changed := range commit.Files {
		if len(files) == 30 {
			break
		}
		files = append(files, file{
			Filename: truncateText(changed.Filename, 512),
			Status:   changed.Status, Additions: changed.Additions,
			Deletions: changed.Deletions, Changes: changed.Changes,
			Patch: sanitizeChangeText(truncateText(changed.Patch, 8192)),
		})
	}
	return json.Marshal(map[string]any{"repository": repository, "sha": strings.ToLower(commit.SHA), "files": files})
}

func sanitizeChangeText(value string) string {
	return sensitiveChangeText.ReplaceAllString(value, "$1=<redacted>")
}

func truncateText(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}
