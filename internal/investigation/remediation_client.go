package investigation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"incidentpilot/internal/remediation"
)

const maxRemediationResponseBytes = 64 << 10

type RemediationClient struct {
	Client   *http.Client
	Endpoint string
	Token    string
}

func NewRemediationClient(client *http.Client, endpoint, token string) (*RemediationClient, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.Path != "/api/v1/remediations" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("remediation endpoint must be an http(s) /api/v1/remediations URL")
	}
	if client == nil || len(token) < 24 {
		return nil, errors.New("remediation client and 24-byte token are required")
	}
	return &RemediationClient{Client: client, Endpoint: parsed.String(), Token: token}, nil
}

func (client *RemediationClient) Request(ctx context.Context, request remediation.Request) (remediation.Result, error) {
	payload, err := json.Marshal(request)
	if err != nil || len(payload) > 32<<10 {
		return remediation.Result{}, errors.New("invalid remediation request")
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, client.Endpoint, bytes.NewReader(payload))
	if err != nil {
		return remediation.Result{}, errors.New("create remediation request")
	}
	httpRequest.Header.Set("Authorization", "Bearer "+client.Token)
	httpRequest.Header.Set("Content-Type", "application/json")
	response, err := client.Client.Do(httpRequest)
	if err != nil {
		return remediation.Result{}, fmt.Errorf("submit remediation request: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxRemediationResponseBytes+1))
	if err != nil || len(body) > maxRemediationResponseBytes {
		return remediation.Result{}, errors.New("invalid remediation response")
	}
	if response.StatusCode != http.StatusCreated {
		return remediation.Result{}, fmt.Errorf("remediation API returned status %d", response.StatusCode)
	}
	if contentType := response.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
		return remediation.Result{}, errors.New("remediation API returned non-JSON content")
	}
	var result remediation.Result
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return remediation.Result{}, errors.New("decode remediation response")
	}
	if result.Proposal.ID == "" || result.Decision.ID == "" || result.Audit.ID == "" {
		return remediation.Result{}, errors.New("remediation API returned an incomplete result")
	}
	return result, nil
}
