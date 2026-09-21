package agent

import (
	"encoding/json"

	"incidentpilot/internal/evidence"
)

// diagnosticBrief projects known diagnostic fields, keeping arbitrary labels,
// annotations, and long Kubernetes objects out of the model prompt.
func diagnosticBrief(record evidence.Record) string {
	switch record.Tool {
	case "kubernetes_get_deployment":
		var dep struct {
			Spec struct {
				Replicas int `json:"replicas"`
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
			Status struct {
				AvailableReplicas int `json:"availableReplicas"`
			} `json:"status"`
		}
		if json.Unmarshal(record.Payload, &dep) != nil {
			return ""
		}
		out := struct {
			Desired    int `json:"desired"`
			Available  int `json:"available"`
			Containers []struct {
				Name        string `json:"name"`
				MemoryLimit string `json:"memory_limit"`
			} `json:"containers"`
		}{Desired: dep.Spec.Replicas, Available: dep.Status.AvailableReplicas}
		for _, container := range dep.Spec.Template.Spec.Containers {
			if len(out.Containers) >= 4 {
				break
			}
			out.Containers = append(out.Containers, struct {
				Name        string `json:"name"`
				MemoryLimit string `json:"memory_limit"`
			}{container.Name, container.Resources.Limits["memory"]})
		}
		encoded, _ := json.Marshal(out)
		return string(encoded)
	case "kubernetes_get_pods":
		var pods struct {
			Items []struct {
				Metadata struct {
					Name string `json:"name"`
				} `json:"metadata"`
				Status struct {
					Phase             string `json:"phase"`
					ContainerStatuses []struct {
						Name         string `json:"name"`
						RestartCount int    `json:"restartCount"`
						LastState    struct {
							Terminated struct {
								Reason string `json:"reason"`
							} `json:"terminated"`
						} `json:"lastState"`
						State struct {
							Waiting struct {
								Reason string `json:"reason"`
							} `json:"waiting"`
						} `json:"state"`
					} `json:"containerStatuses"`
				} `json:"status"`
			} `json:"items"`
		}
		if json.Unmarshal(record.Payload, &pods) != nil {
			return ""
		}
		type status struct {
			Name           string `json:"name"`
			Restarts       int    `json:"restarts"`
			LastTerminated string `json:"last_terminated"`
			Waiting        string `json:"waiting"`
		}
		type pod struct {
			Name       string   `json:"name"`
			Phase      string   `json:"phase"`
			Containers []status `json:"containers"`
		}
		out := make([]pod, 0, 10)
		for _, item := range pods.Items {
			if len(out) >= 10 {
				break
			}
			entry := pod{Name: item.Metadata.Name, Phase: item.Status.Phase}
			for _, container := range item.Status.ContainerStatuses {
				if len(entry.Containers) >= 4 {
					break
				}
				entry.Containers = append(entry.Containers, status{container.Name, container.RestartCount, container.LastState.Terminated.Reason, container.State.Waiting.Reason})
			}
			out = append(out, entry)
		}
		encoded, _ := json.Marshal(out)
		return string(encoded)
	case "argocd_get_application":
		var app struct {
			Application     string `json:"application"`
			SyncStatus      string `json:"sync_status"`
			HealthStatus    string `json:"health_status"`
			CurrentRevision string `json:"current_revision"`
			TargetRevision  string `json:"target_revision"`
		}
		if json.Unmarshal(record.Payload, &app) != nil {
			return ""
		}
		encoded, _ := json.Marshal(app)
		return string(encoded)
	case "argocd_get_revision_history":
		var history struct {
			Application string `json:"application"`
			Deployments []struct {
				Revision   string `json:"revision"`
				DeployedAt string `json:"deployed_at"`
			} `json:"deployments"`
		}
		if json.Unmarshal(record.Payload, &history) != nil {
			return ""
		}
		if len(history.Deployments) > 5 {
			history.Deployments = history.Deployments[:5]
		}
		encoded, _ := json.Marshal(history)
		return string(encoded)
	case "github_get_commit":
		var commit struct {
			Repository  string `json:"repository"`
			SHA         string `json:"sha"`
			Message     string `json:"message"`
			CommittedAt string `json:"committed_at"`
		}
		if json.Unmarshal(record.Payload, &commit) != nil {
			return ""
		}
		if len(commit.Message) > 512 {
			commit.Message = commit.Message[:512]
		}
		encoded, _ := json.Marshal(commit)
		return string(encoded)
	case "github_get_diff":
		var diff struct {
			Repository string `json:"repository"`
			SHA        string `json:"sha"`
			Files      []struct {
				Filename string `json:"filename"`
				Status   string `json:"status"`
				Changes  int    `json:"changes"`
				Patch    string `json:"patch"`
			} `json:"files"`
		}
		if json.Unmarshal(record.Payload, &diff) != nil {
			return ""
		}
		if len(diff.Files) > 10 {
			diff.Files = diff.Files[:10]
		}
		for index := range diff.Files {
			if len(diff.Files[index].Patch) > 2048 {
				diff.Files[index].Patch = diff.Files[index].Patch[:2048]
			}
		}
		encoded, _ := json.Marshal(diff)
		return string(encoded)
	}
	return ""
}
