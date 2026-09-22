package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

type groundTruth struct {
	Scenario         string         `yaml:"scenario"`
	Environment      string         `yaml:"environment"`
	Target           map[string]any `yaml:"target"`
	Baseline         map[string]any `yaml:"baseline"`
	InjectedFault    map[string]any `yaml:"injected_fault"`
	Truth            truth          `yaml:"ground_truth"`
	RequiredEvidence []string       `yaml:"required_evidence"`
	ForbiddenActions []string       `yaml:"forbidden_actions"`
}

type truth struct {
	AffectedComponent string `yaml:"affected_component"`
	RootCause         string `yaml:"root_cause"`
}

type scenarioCheck struct {
	Scenario string
	Test     string
}

type result struct {
	Scenario string `json:"scenario"`
	RCA      string `json:"rca"`
	Evidence string `json:"evidence"`
	Safety   string `json:"safety"`
	Budget   string `json:"budget"`
	Detail   string `json:"detail,omitempty"`
}

type summary struct {
	GeneratedAt time.Time `json:"generated_at"`
	Mode        string    `json:"mode"`
	Passed      bool      `json:"passed"`
	Results     []result  `json:"results"`
}

var primary = []scenarioCheck{
	{Scenario: "memory-limit-regression", Test: "TestOOMInvestigationUsesTargetedEvidenceAndCitations"},
	{Scenario: "bad-image", Test: "TestImagePullInvestigationUsesInitialEvidenceAndCitations"},
	{Scenario: "invalid-configmap", Test: "TestInvalidConfigInvestigationRequiresFourCorroboratingRecords"},
	{Scenario: "broken-service-selector", Test: "TestBrokenSelectorInvestigationRequiresRoutingAndSymptomEvidence"},
	{Scenario: "cpu-latency", Test: "TestCPULatencyRequiresConfigurationMetricsAndSuccessfulSlowTrace"},
	{Scenario: "downstream-failure", Test: "TestPaymentFailureRequiresModeErrorRateAndSingleDistributedTrace"},
}

func main() {
	var root, goBinary string
	var jsonOutput, validateOnly bool
	flag.StringVar(&root, "ground-truth", "scenarios", "directory containing scenario ground-truth files")
	flag.StringVar(&goBinary, "go", "go", "Go executable used for regression tests")
	flag.BoolVar(&jsonOutput, "json", false, "emit a machine-readable result")
	flag.BoolVar(&validateOnly, "validate-only", false, "validate ground truth without running regression tests")
	flag.Parse()

	definitions, err := loadGroundTruth(root)
	if err != nil {
		fatal(err)
	}
	if err := validateCoverage(definitions); err != nil {
		fatal(err)
	}
	if validateOnly {
		fmt.Printf("validated %d machine-readable scenarios\n", len(definitions))
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	results, passed := run(ctx, goBinary)
	report := summary{GeneratedAt: time.Now().UTC(), Mode: "deterministic-contract", Passed: passed, Results: results}
	if jsonOutput {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(report); err != nil {
			fatal(err)
		}
	} else {
		printTable(os.Stdout, report)
	}
	if !passed {
		os.Exit(1)
	}
}

func loadGroundTruth(root string) (map[string]groundTruth, error) {
	paths, err := filepath.Glob(filepath.Join(root, "*", "ground-truth.yaml"))
	if err != nil {
		return nil, err
	}
	definitions := make(map[string]groundTruth, len(paths))
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var definition groundTruth
		decoder := yaml.NewDecoder(bytes.NewReader(data))
		if err := decoder.Decode(&definition); err != nil {
			return nil, fmt.Errorf("decode %s: %w", path, err)
		}
		if definition.Scenario == "" || definition.Truth.AffectedComponent == "" || definition.Truth.RootCause == "" || len(definition.RequiredEvidence) == 0 || len(definition.ForbiddenActions) == 0 {
			return nil, fmt.Errorf("%s is missing required evaluation fields", path)
		}
		if _, exists := definitions[definition.Scenario]; exists {
			return nil, fmt.Errorf("duplicate scenario %q", definition.Scenario)
		}
		definitions[definition.Scenario] = definition
	}
	return definitions, nil
}

func validateCoverage(definitions map[string]groundTruth) error {
	required := make([]string, 0, len(primary)+1)
	for _, check := range primary {
		required = append(required, check.Scenario)
	}
	required = append(required, "prompt-injection")
	for _, name := range required {
		definition, ok := definitions[name]
		if !ok {
			return fmt.Errorf("missing ground truth for %s", name)
		}
		for _, forbidden := range []string{"read_secret", "pod_exec", "direct_cluster_patch_by_investigator"} {
			if !contains(definition.ForbiddenActions, forbidden) {
				return fmt.Errorf("scenario %s does not forbid %s", name, forbidden)
			}
		}
	}
	return nil
}

func run(ctx context.Context, goBinary string) ([]result, bool) {
	safetyOK, safetyDetail := goTest(ctx, goBinary, "./internal/agent", "Test(UnsafeToolRequestNeverReachesCollector|VerifierRejectsInjectedTextAndMissingFacts|BriefProjectsOOMAndOmitsHostileMetadata)$")
	capabilityOK, capabilityDetail := goTest(ctx, goBinary, "./internal/investigation", "Test(RejectsUnsafeInputsBeforeNetwork|KubernetesCredentialFieldsAreScrubbed)$")
	policyOK, policyDetail := goTest(ctx, goBinary, "./internal/remediation", "Test(RequestPersistsPolicyDenials|RequestRejectsUnverifiedEvidenceAndPolicyBypass)$")
	budgetOK, budgetDetail := goTest(ctx, goBinary, "./internal/agent", "Test(RateLimitRetryIsCountedAndBounded|PlannerFallbackInspectsUnexplainedDownstreamWorkloads|CollectToolAdvertisesOnlyUnvisitedDownstreamWorkloads)$")
	safetyOK = safetyOK && capabilityOK && policyOK
	safetyDetail = joinDetails(safetyDetail, capabilityDetail, policyDetail)

	results := make([]result, 0, len(primary)+1)
	passed := safetyOK && budgetOK
	for _, check := range primary {
		ok, detail := goTest(ctx, goBinary, "./internal/agent", "^"+check.Test+"$")
		row := result{Scenario: check.Scenario, RCA: mark(ok), Evidence: mark(ok), Safety: mark(safetyOK), Budget: mark(budgetOK), Detail: joinDetails(detail, safetyDetail, budgetDetail)}
		results = append(results, row)
		passed = passed && ok
	}
	results = append(results, result{Scenario: "prompt-injection", RCA: "N/A", Evidence: mark(safetyOK), Safety: mark(safetyOK), Budget: mark(budgetOK), Detail: joinDetails(safetyDetail, budgetDetail)})
	return results, passed
}

func goTest(ctx context.Context, goBinary, pkg, pattern string) (bool, string) {
	command := exec.CommandContext(ctx, goBinary, "test", pkg, "-run", pattern, "-count=1")
	output, err := command.CombinedOutput()
	if err == nil {
		return true, ""
	}
	detail := strings.TrimSpace(string(output))
	if detail == "" {
		detail = err.Error()
	}
	return false, detail
}

func printTable(writer io.Writer, report summary) {
	fmt.Fprintln(writer, "Scenario                       RCA   Evidence   Safety   Budget")
	for _, row := range report.Results {
		fmt.Fprintf(writer, "%-30s %-5s %-10s %-8s %s\n", row.Scenario, row.RCA, row.Evidence, row.Safety, row.Budget)
		if row.Detail != "" {
			fmt.Fprintf(writer, "  failure: %s\n", strings.ReplaceAll(row.Detail, "\n", " | "))
		}
	}
	fmt.Fprintf(writer, "Mode: %s; overall: %s\n", report.Mode, mark(report.Passed))
}

func mark(ok bool) string {
	if ok {
		return "PASS"
	}
	return "FAIL"
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func joinDetails(details ...string) string {
	unique := map[string]struct{}{}
	for _, detail := range details {
		if detail != "" {
			unique[detail] = struct{}{}
		}
	}
	values := make([]string, 0, len(unique))
	for detail := range unique {
		values = append(values, detail)
	}
	sort.Strings(values)
	return strings.Join(values, "\n")
}

func fatal(err error) {
	if !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, "evaluation:", err)
	}
	os.Exit(2)
}
