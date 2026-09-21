package agent

import (
	"encoding/json"
	"fmt"
	"regexp"

	"incidentpilot/internal/evidence"
)

var memoryLimitPattern = regexp.MustCompile(`^[1-9][0-9]*(Mi|Gi)$`)

func verifyOOM(records []evidence.Record, cited []string) *RootCause {
	allowed := make(map[string]bool, len(cited))
	for _, id := range cited {
		allowed[id] = true
	}
	var podID, deploymentID, memoryLimit string
	for _, record := range records {
		if !allowed[record.ID] || record.ResourceRef != "incidentpilot-demo/payments-api" {
			continue
		}
		switch record.Tool {
		case "kubernetes_get_pods":
			if record.Source == "kubernetes/pods" && podsShowOOM(record.Payload) {
				podID = record.ID
			}
		case "kubernetes_get_deployment":
			if limit := deploymentMemoryLimit(record.Payload); record.Source == "kubernetes/deployment" && memoryLimitPattern.MatchString(limit) {
				deploymentID = record.ID
				memoryLimit = limit
			}
		}
	}
	if podID == "" || deploymentID == "" {
		return nil
	}
	return &RootCause{
		Conclusion:  fmt.Sprintf("payments-api was OOMKilled while its Deployment memory limit was %s; the limit did not sustain the workload", memoryLimit),
		Component:   "payments-api",
		EvidenceIDs: []string{podID, deploymentID},
	}
}

func podsShowOOM(raw json.RawMessage) bool {
	var pods struct {
		Items []struct {
			Status struct {
				ContainerStatuses []struct {
					Name      string `json:"name"`
					LastState struct {
						Terminated struct {
							Reason string `json:"reason"`
						} `json:"terminated"`
					} `json:"lastState"`
					State struct {
						Terminated struct {
							Reason string `json:"reason"`
						} `json:"terminated"`
					} `json:"state"`
				} `json:"containerStatuses"`
			} `json:"status"`
		} `json:"items"`
	}
	if json.Unmarshal(raw, &pods) != nil {
		return false
	}
	for _, pod := range pods.Items {
		for _, status := range pod.Status.ContainerStatuses {
			if status.Name == "payments-api" && (status.LastState.Terminated.Reason == "OOMKilled" || status.State.Terminated.Reason == "OOMKilled") {
				return true
			}
		}
	}
	return false
}

func deploymentMemoryLimit(raw json.RawMessage) string {
	var dep struct {
		Spec struct {
			Template struct {
				Spec struct {
					Containers []struct {
						Name      string `json:"name"`
						Resources struct {
							Limits map[string]string `json:"limits"`
						} `json:"resources"`
					} `json:"containers"`
				} `json:"spec"`
			} `json:"template"`
		} `json:"spec"`
	}
	if json.Unmarshal(raw, &dep) != nil {
		return ""
	}
	for _, container := range dep.Spec.Template.Spec.Containers {
		if container.Name == "payments-api" {
			return container.Resources.Limits["memory"]
		}
	}
	return ""
}
