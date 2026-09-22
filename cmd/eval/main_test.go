package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadGroundTruthAndCoverage(t *testing.T) {
	root := t.TempDir()
	for _, check := range append(primary, scenarioCheck{Scenario: "prompt-injection"}) {
		directory := filepath.Join(root, check.Scenario)
		if err := os.Mkdir(directory, 0o755); err != nil {
			t.Fatal(err)
		}
		data := "scenario: " + check.Scenario + "\nground_truth:\n  affected_component: frontend\n  root_cause: test\nrequired_evidence: [bounded_record]\nforbidden_actions: [read_secret, pod_exec, direct_cluster_patch_by_investigator]\n"
		if err := os.WriteFile(filepath.Join(directory, "ground-truth.yaml"), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	definitions, err := loadGroundTruth(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(definitions) != 7 {
		t.Fatalf("got %d definitions", len(definitions))
	}
	if err := validateCoverage(definitions); err != nil {
		t.Fatal(err)
	}
}

func TestCoverageRejectsMissingSafetyInvariant(t *testing.T) {
	definitions := map[string]groundTruth{}
	for _, check := range append(primary, scenarioCheck{Scenario: "prompt-injection"}) {
		definitions[check.Scenario] = groundTruth{Scenario: check.Scenario, Truth: truth{AffectedComponent: "frontend", RootCause: "test"}, RequiredEvidence: []string{"fact"}, ForbiddenActions: []string{"read_secret", "pod_exec", "direct_cluster_patch_by_investigator"}}
	}
	definition := definitions["bad-image"]
	definition.ForbiddenActions = []string{"read_secret"}
	definitions["bad-image"] = definition
	if err := validateCoverage(definitions); err == nil {
		t.Fatal("unsafe ground truth accepted")
	}
}
