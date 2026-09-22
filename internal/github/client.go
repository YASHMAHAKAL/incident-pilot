package github

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.yaml.in/yaml/v3"

	"incidentpilot/internal/remediation"
)

const maxGitHubBody = 512 << 10

var (
	repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}/[A-Za-z0-9_.-]{1,100}$`)
	branchPattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	commitPattern     = regexp.MustCompile(`^[0-9a-f]{40,64}$`)
	pathPattern       = regexp.MustCompile(`^[A-Za-z0-9_./-]{1,256}$`)
)

type Client struct {
	HTTP       *http.Client
	BaseURL    string
	Token      string
	Repository string
	BaseBranch string
	Path       string
}

type apiError struct{ status int }

func (e apiError) Error() string { return fmt.Sprintf("GitHub returned status %d", e.status) }

func NewClient(httpClient *http.Client, baseURL, token, repository, baseBranch, path string) (*Client, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("GitHub write API URL must be a fixed http(s) base URL")
	}
	if parsed.Scheme == "http" && !localHost(parsed.Hostname()) {
		return nil, errors.New("unencrypted GitHub write API is allowed only for loopback or in-cluster services")
	}
	if httpClient == nil || len(token) < 20 || !validRepository(repository) || !branchPattern.MatchString(baseBranch) || !safePath(path) {
		return nil, errors.New("GitHub write client requires HTTP, token, repository, base branch, and safe path configuration")
	}
	return &Client{HTTP: httpClient, BaseURL: strings.TrimRight(parsed.String(), "/"), Token: token, Repository: repository, BaseBranch: baseBranch, Path: path}, nil
}

func (client *Client) CreatePullRequest(ctx context.Context, proposal remediation.Proposal, decision remediation.PolicyDecision) (remediation.PullRequest, error) {
	result := remediation.PullRequest{Repository: client.Repository, BaseBranch: client.BaseBranch, HeadBranch: remediationBranch(proposal.InvestigationID)}
	fail := func(code string, err error) (remediation.PullRequest, error) {
		result.FailureCode = code
		return result, err
	}
	if !decision.Allowed || decision.PolicyVersion == "" || proposal.Target.Repository != client.Repository || proposal.Target.Path != client.Path {
		return fail("authorization_invalid", errors.New("GitHub remediation authorization is invalid"))
	}
	ctx, span := otel.Tracer("incidentpilot/github").Start(ctx, "github.create_pr")
	defer span.End()
	span.SetAttributes(attribute.String("github.operation", "create_remediation_pr"))
	if existing, found, err := client.findOpenPullRequest(ctx, result.HeadBranch); err != nil {
		return fail("github_pr_lookup_failed", err)
	} else if found {
		existing.Reused = true
		return existing, nil
	}
	baseSHA, err := client.getRef(ctx, client.BaseBranch)
	if err != nil {
		return fail("github_base_ref_failed", err)
	}
	baseFile, err := client.getContent(ctx, client.BaseBranch)
	if err != nil {
		return fail("github_content_read_failed", err)
	}
	patched, err := GenerateMemoryLimitPatch(baseFile.Content, proposal)
	if err != nil {
		return fail("patch_not_applicable", err)
	}
	branchSHA, branchExists, err := client.findRef(ctx, result.HeadBranch)
	if err != nil {
		return fail("github_branch_lookup_failed", err)
	}
	file := baseFile
	needsCommit := true
	if !branchExists {
		if err := client.createRef(ctx, result.HeadBranch, baseSHA); err != nil {
			return fail("github_branch_create_failed", err)
		}
	} else {
		branchFile, err := client.getContent(ctx, result.HeadBranch)
		if err != nil {
			return fail("github_branch_content_failed", err)
		}
		file = branchFile
		if bytes.Equal(branchFile.Content, patched) {
			needsCommit = false
			result.CommitSHA = branchSHA
		} else if !bytes.Equal(branchFile.Content, baseFile.Content) {
			return fail("github_branch_diverged", errors.New("remediation branch contains unexpected content"))
		}
	}
	if needsCommit {
		commitSHA, err := client.updateContent(ctx, result.HeadBranch, file.SHA, patched, proposal.IncidentID)
		if err != nil {
			return fail("github_commit_failed", err)
		}
		result.CommitSHA = commitSHA
	}
	created, err := client.createPR(ctx, result.HeadBranch, proposal, decision)
	if err != nil {
		if existing, found, lookupErr := client.findOpenPullRequest(ctx, result.HeadBranch); lookupErr == nil && found {
			existing.Reused = true
			return existing, nil
		}
		return fail("github_pr_create_failed", err)
	}
	created.CommitSHA = result.CommitSHA
	return created, nil
}

type contentResponse struct {
	Type     string `json:"type"`
	Path     string `json:"path"`
	SHA      string `json:"sha"`
	Encoding string `json:"encoding"`
	Content  string `json:"content"`
}

type repositoryFile struct {
	SHA     string
	Content []byte
}

func (client *Client) getContent(ctx context.Context, ref string) (repositoryFile, error) {
	var response contentResponse
	path := client.repositoryPath("contents") + "/" + escapePath(client.Path)
	if err := client.doJSON(ctx, http.MethodGet, path, url.Values{"ref": {ref}}, nil, &response, http.StatusOK); err != nil {
		return repositoryFile{}, err
	}
	if response.Type != "file" || response.Path != client.Path || response.Encoding != "base64" || !commitPattern.MatchString(strings.ToLower(response.SHA)) {
		return repositoryFile{}, errors.New("GitHub returned unexpected repository content metadata")
	}
	encoded := strings.NewReplacer("\n", "", "\r", "", " ", "", "\t", "").Replace(response.Content)
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(decoded) == 0 || len(decoded) > 256<<10 {
		return repositoryFile{}, errors.New("GitHub returned invalid repository content")
	}
	return repositoryFile{SHA: strings.ToLower(response.SHA), Content: decoded}, nil
}

func (client *Client) getRef(ctx context.Context, branch string) (string, error) {
	sha, found, err := client.findRef(ctx, branch)
	if err != nil {
		return "", err
	}
	if !found {
		return "", errors.New("configured GitHub base branch was not found")
	}
	return sha, nil
}

func (client *Client) findRef(ctx context.Context, branch string) (string, bool, error) {
	var response struct {
		Ref    string `json:"ref"`
		Object struct {
			Type string `json:"type"`
			SHA  string `json:"sha"`
		} `json:"object"`
	}
	err := client.doJSON(ctx, http.MethodGet, client.repositoryPath("git/ref/heads")+"/"+url.PathEscape(branch), nil, nil, &response, http.StatusOK)
	var status apiError
	if errors.As(err, &status) && status.status == http.StatusNotFound {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	sha := strings.ToLower(response.Object.SHA)
	if response.Ref != "refs/heads/"+branch || response.Object.Type != "commit" || !commitPattern.MatchString(sha) {
		return "", false, errors.New("GitHub returned an unexpected branch reference")
	}
	return sha, true, nil
}

func (client *Client) createRef(ctx context.Context, branch, sha string) error {
	payload := map[string]string{"ref": "refs/heads/" + branch, "sha": sha}
	return client.doJSON(ctx, http.MethodPost, client.repositoryPath("git/refs"), nil, payload, nil, http.StatusCreated)
}

func (client *Client) updateContent(ctx context.Context, branch, blobSHA string, content []byte, incidentID string) (string, error) {
	payload := map[string]string{
		"message": "fix: restore payments-api memory limit for incident " + incidentID,
		"content": base64.StdEncoding.EncodeToString(content), "sha": blobSHA, "branch": branch,
	}
	var response struct {
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	path := client.repositoryPath("contents") + "/" + escapePath(client.Path)
	if err := client.doJSON(ctx, http.MethodPut, path, nil, payload, &response, http.StatusOK, http.StatusCreated); err != nil {
		return "", err
	}
	sha := strings.ToLower(response.Commit.SHA)
	if !commitPattern.MatchString(sha) {
		return "", errors.New("GitHub returned an invalid remediation commit")
	}
	return sha, nil
}

type pullResponse struct {
	Number  int    `json:"number"`
	HTMLURL string `json:"html_url"`
	State   string `json:"state"`
	Head    struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

func (client *Client) findOpenPullRequest(ctx context.Context, branch string) (remediation.PullRequest, bool, error) {
	owner := strings.Split(client.Repository, "/")[0]
	var response []pullResponse
	query := url.Values{"state": {"open"}, "head": {owner + ":" + branch}, "base": {client.BaseBranch}, "per_page": {"2"}}
	if err := client.doJSON(ctx, http.MethodGet, client.repositoryPath("pulls"), query, nil, &response, http.StatusOK); err != nil {
		return remediation.PullRequest{}, false, err
	}
	if len(response) == 0 {
		return remediation.PullRequest{}, false, nil
	}
	if len(response) != 1 {
		return remediation.PullRequest{}, false, errors.New("GitHub returned multiple remediation pull requests")
	}
	result, err := client.projectPull(response[0], branch)
	return result, err == nil, err
}

func (client *Client) createPR(ctx context.Context, branch string, proposal remediation.Proposal, decision remediation.PolicyDecision) (remediation.PullRequest, error) {
	evidence := make([]string, len(proposal.EvidenceIDs))
	for index, id := range proposal.EvidenceIDs {
		evidence[index] = "- `" + id + "`"
	}
	body := "IncidentPilot policy-approved remediation.\n\n" +
		"Incident: `" + proposal.IncidentID + "`\n\nInvestigation: `" + proposal.InvestigationID + "`\n\n" +
		"Policy: `" + decision.PolicyVersion + "`\n\nEvidence:\n" + strings.Join(evidence, "\n") + "\n\n" +
		"Change: payments-api memory limit `" + proposal.Change.Before + "` -> `" + proposal.Change.After + "`.\n\nHuman review and merge are required."
	payload := map[string]any{
		"title": "fix: restore payments-api memory limit", "head": branch, "base": client.BaseBranch,
		"body": body, "maintainer_can_modify": false,
	}
	var response pullResponse
	if err := client.doJSON(ctx, http.MethodPost, client.repositoryPath("pulls"), nil, payload, &response, http.StatusCreated); err != nil {
		return remediation.PullRequest{}, err
	}
	return client.projectPull(response, branch)
}

func (client *Client) projectPull(response pullResponse, branch string) (remediation.PullRequest, error) {
	commit := strings.ToLower(response.Head.SHA)
	if response.Number < 1 || response.State != "open" || response.Head.Ref != branch || response.Base.Ref != client.BaseBranch || !commitPattern.MatchString(commit) || !validPullURL(response.HTMLURL) {
		return remediation.PullRequest{}, errors.New("GitHub returned an invalid pull request")
	}
	return remediation.PullRequest{Repository: client.Repository, BaseBranch: client.BaseBranch, HeadBranch: branch, Number: response.Number, URL: response.HTMLURL, CommitSHA: commit}, nil
}

func (client *Client) doJSON(ctx context.Context, method, path string, query url.Values, input, output any, statuses ...int) error {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil || len(encoded) > 384<<10 {
			return errors.New("encode GitHub request")
		}
		body = bytes.NewReader(encoded)
	}
	endpoint := client.BaseURL + path
	if len(query) != 0 {
		endpoint += "?" + query.Encode()
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return errors.New("create GitHub request")
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("Authorization", "Bearer "+client.Token)
	request.Header.Set("X-GitHub-Api-Version", "2026-03-10")
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.HTTP.Do(request)
	if err != nil {
		return fmt.Errorf("call GitHub: %w", err)
	}
	defer response.Body.Close()
	allowed := false
	for _, status := range statuses {
		allowed = allowed || response.StatusCode == status
	}
	if !allowed {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxGitHubBody))
		return apiError{status: response.StatusCode}
	}
	if output == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxGitHubBody))
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxGitHubBody+1))
	if err != nil || len(data) > maxGitHubBody || json.Unmarshal(data, output) != nil {
		return errors.New("GitHub returned invalid JSON")
	}
	return nil
}

func (client *Client) repositoryPath(resource string) string {
	parts := strings.Split(client.Repository, "/")
	return "/repos/" + url.PathEscape(parts[0]) + "/" + url.PathEscape(parts[1]) + "/" + resource
}

func remediationBranch(investigationID string) string {
	compact := strings.ReplaceAll(strings.ToLower(investigationID), "-", "")
	if len(compact) > 12 {
		compact = compact[:12]
	}
	return "incidentpilot-oom-" + compact
}

func escapePath(path string) string {
	parts := strings.Split(path, "/")
	for index := range parts {
		parts[index] = url.PathEscape(parts[index])
	}
	return strings.Join(parts, "/")
}

func localHost(host string) bool {
	host = strings.ToLower(host)
	return host == "localhost" || host == "127.0.0.1" || host == "::1" || strings.HasSuffix(host, ".svc") || strings.HasSuffix(host, ".svc.cluster.local")
}

func safePath(path string) bool {
	if !pathPattern.MatchString(path) || strings.HasPrefix(path, "/") || strings.Contains(path, "..") {
		return false
	}
	for _, part := range strings.Split(path, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func validRepository(repository string) bool {
	if !repositoryPattern.MatchString(repository) {
		return false
	}
	for _, part := range strings.Split(repository, "/") {
		if part == "." || part == ".." || strings.Contains(part, "..") {
			return false
		}
	}
	return true
}

func validPullURL(raw string) bool {
	parsed, err := url.Parse(raw)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil && parsed.Fragment == "" && len(raw) <= 2048
}

func GenerateMemoryLimitPatch(content []byte, proposal remediation.Proposal) ([]byte, error) {
	if len(content) == 0 || len(content) > 256<<10 || proposal.Operation != "update_resource_limit" || proposal.Target.Kind != "Deployment" || proposal.Target.Namespace != "incidentpilot-demo" || proposal.Target.Name != "payments-api" || proposal.Target.Container != "payments-api" || proposal.Change.Field != "memory_limit" {
		return nil, errors.New("proposal is not an allowed memory-limit patch")
	}
	decoder := yaml.NewDecoder(bytes.NewReader(content))
	var matches []*yaml.Node
	for {
		var document yaml.Node
		err := decoder.Decode(&document)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, errors.New("repository manifest is invalid YAML")
		}
		root := documentRoot(&document)
		if scalar(mappingValue(root, "kind")) != "Deployment" || scalar(mappingValue(mappingValue(root, "metadata"), "name")) != "payments-api" {
			continue
		}
		containers := mappingValue(mappingValue(mappingValue(mappingValue(root, "spec"), "template"), "spec"), "containers")
		if containers == nil || containers.Kind != yaml.SequenceNode {
			continue
		}
		for _, container := range containers.Content {
			if scalar(mappingValue(container, "name")) != "payments-api" {
				continue
			}
			memory := mappingValue(mappingValue(mappingValue(container, "resources"), "limits"), "memory")
			if memory != nil && memory.Kind == yaml.ScalarNode {
				matches = append(matches, memory)
			}
		}
	}
	if len(matches) != 1 || matches[0].Value != proposal.Change.Before || matches[0].Line < 1 || matches[0].Column < 1 {
		return nil, errors.New("repository manifest does not contain exactly one expected memory limit")
	}
	lines := bytes.SplitAfter(content, []byte("\n"))
	lineIndex, column := matches[0].Line-1, matches[0].Column-1
	if lineIndex >= len(lines) || column+len(proposal.Change.Before) > len(lines[lineIndex]) || string(lines[lineIndex][column:column+len(proposal.Change.Before)]) != proposal.Change.Before {
		return nil, errors.New("repository memory limit location is ambiguous")
	}
	line := lines[lineIndex]
	lines[lineIndex] = append(append(append([]byte{}, line[:column]...), proposal.Change.After...), line[column+len(proposal.Change.Before):]...)
	patched := bytes.Join(lines, nil)
	if bytes.Equal(content, patched) || bytes.Count(patched, []byte(proposal.Change.After)) < 1 {
		return nil, errors.New("repository patch made no expected change")
	}
	return patched, nil
}

func documentRoot(document *yaml.Node) *yaml.Node {
	if document != nil && document.Kind == yaml.DocumentNode && len(document.Content) == 1 {
		return document.Content[0]
	}
	return document
}

func mappingValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for index := 0; index+1 < len(node.Content); index += 2 {
		if scalar(node.Content[index]) == key {
			return node.Content[index+1]
		}
	}
	return nil
}

func scalar(node *yaml.Node) string {
	if node == nil || node.Kind != yaml.ScalarNode {
		return ""
	}
	return node.Value
}
