package onboarding

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// Profile is operator-owned scope. Empty configuration retains the local demo.
// The external profile deliberately grants investigation reads, not RCA or fixes.
type Profile struct {
	Mode      string     `json:"mode"`
	Namespace string     `json:"namespace,omitempty"`
	Workloads []Workload `json:"workloads,omitempty"`
	Alerts    []Alert    `json:"alerts,omitempty"`
	Verifiers []string   `json:"verifiers,omitempty"`
}

type Workload struct {
	Name          string `json:"name"`
	Container     string `json:"container"`
	PodLabelKey   string `json:"podLabelKey"`
	PodLabelValue string `json:"podLabelValue"`
}

type Alert struct {
	Name     string `json:"name"`
	Workload string `json:"workload"`
}

var dnsLabel = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
var labelKey = regexp.MustCompile(`^([a-z0-9]([-a-z0-9.]*[a-z0-9])?/)?[A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?$`)
var labelValue = regexp.MustCompile(`^([A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?)?$`)
var alertName = regexp.MustCompile(`^[A-Za-z_:][A-Za-z0-9_:]*$`)

func Demo() Profile { return Profile{Mode: "demo", Namespace: "incidentpilot-demo"} }

func Parse(raw string) (Profile, error) {
	if raw == "" {
		return Demo(), nil
	}
	if len(raw) > 32<<10 {
		return Profile{}, errors.New("onboarding profile exceeds 32 KiB")
	}
	var p Profile
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&p); err != nil {
		return Profile{}, fmt.Errorf("invalid onboarding profile: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return Profile{}, errors.New("onboarding profile contains trailing or invalid data")
	}
	if p.Mode == "demo" && p.Namespace == "" && len(p.Workloads) == 0 && len(p.Alerts) == 0 && len(p.Verifiers) == 0 {
		return Demo(), nil
	}
	if p.Mode != "external" {
		return Profile{}, errors.New("onboarding mode must be demo or external; demo scope is fixed")
	}
	if !validDNSLabel(p.Namespace) || strings.HasPrefix(p.Namespace, "kube-") || p.Namespace == "default" || p.Namespace == "incidentpilot-system" || p.Namespace == "incidentpilot-observability" {
		return Profile{}, errors.New("external namespace must be an unprotected Kubernetes namespace")
	}
	if len(p.Workloads) == 0 || len(p.Workloads) > 20 || len(p.Alerts) == 0 || len(p.Alerts) > 50 {
		return Profile{}, errors.New("external profile requires 1-20 workloads and 1-50 alerts")
	}
	names := make(map[string]bool, len(p.Workloads))
	for _, w := range p.Workloads {
		if !validDNSLabel(w.Name) || !validDNSLabel(w.Container) || names[w.Name] || len(w.PodLabelKey) > 253 || !labelKey.MatchString(w.PodLabelKey) || len(w.PodLabelValue) > 63 || !labelValue.MatchString(w.PodLabelValue) || w.PodLabelValue == "" {
			return Profile{}, errors.New("invalid or duplicate external workload or pod selector")
		}
		names[w.Name] = true
	}
	alerts := make(map[string]bool, len(p.Alerts))
	for _, a := range p.Alerts {
		if len(a.Name) > 128 || !alertName.MatchString(a.Name) || alerts[a.Name] || !names[a.Workload] {
			return Profile{}, errors.New("invalid, duplicate, or unscoped external alert")
		}
		alerts[a.Name] = true
	}
	if len(p.Verifiers) > 2 {
		return Profile{}, errors.New("external profile accepts at most two verifiers")
	}
	verifiers := map[string]bool{}
	for _, verifier := range p.Verifiers {
		if verifier != "kubernetes_oom" && verifier != "image_pull" || verifiers[verifier] {
			return Profile{}, errors.New("unknown or duplicate external verifier")
		}
		verifiers[verifier] = true
	}
	return p, nil
}

func validDNSLabel(name string) bool {
	return len(name) > 0 && len(name) <= 63 && dnsLabel.MatchString(name)
}

func (p Profile) Effective() Profile {
	if p.Mode == "" {
		return Demo()
	}
	return p
}

func (p Profile) AllowsWorkload(name string) bool {
	p = p.Effective()
	if p.Mode == "demo" {
		return name == "frontend" || name == "orders-api" || name == "payments-api"
	}
	for _, w := range p.Workloads {
		if w.Name == name {
			return true
		}
	}
	return false
}

func (p Profile) PodSelector(name string) (string, bool) {
	p = p.Effective()
	if p.Mode == "demo" && p.AllowsWorkload(name) {
		return "app=" + name, true
	}
	key, value, ok := p.PodLabel(name)
	if ok {
		return key + "=" + value, true
	}
	return "", false
}

func (p Profile) PodLabel(name string) (string, string, bool) {
	p = p.Effective()
	if p.Mode == "demo" && p.AllowsWorkload(name) {
		return "app", name, true
	}
	for _, w := range p.Workloads {
		if w.Name == name {
			return w.PodLabelKey, w.PodLabelValue, true
		}
	}
	return "", "", false
}

func (p Profile) Workload(name string) (Workload, bool) {
	for _, workload := range p.Workloads {
		if workload.Name == name {
			return workload, true
		}
	}
	return Workload{}, false
}

func (p Profile) EnablesVerifier(name string) bool {
	for _, verifier := range p.Verifiers {
		if verifier == name {
			return true
		}
	}
	return false
}

func (p Profile) AllowsIncident(namespace, workload, alert string) bool {
	p = p.Effective()
	if namespace != p.Namespace || !p.AllowsWorkload(workload) {
		return false
	}
	if p.Mode == "demo" {
		return true
	}
	for _, a := range p.Alerts {
		if a.Name == alert && a.Workload == workload {
			return true
		}
	}
	return false
}
