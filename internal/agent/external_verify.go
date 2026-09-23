package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"incidentpilot/internal/evidence"
	"incidentpilot/internal/incident"
	"incidentpilot/internal/onboarding"
)

// External verifiers use only current Kubernetes facts from the scoped initial
// collection. They do not authorize remediation.
func externalVerifiedCause(profile onboarding.Profile, inc incident.Incident, records []evidence.Record) *RootCause {
	workload, ok := profile.Workload(inc.Service)
	if !ok {
		return nil
	}
	ref := inc.Namespace + "/" + inc.Service
	byTool := make(map[string]evidence.Record)
	for _, record := range records {
		if record.ResourceRef == ref && (record.Tool == "kubernetes_get_deployment" && record.Source == "kubernetes/deployment" || record.Tool == "kubernetes_get_pods" && record.Source == "kubernetes/pods" || record.Tool == "kubernetes_get_events" && record.Source == "kubernetes/events") {
			byTool[record.Tool] = record
		}
	}
	deployment, pods := byTool["kubernetes_get_deployment"], byTool["kubernetes_get_pods"]
	if deployment.ID == "" || pods.ID == "" {
		return nil
	}
	for _, verifier := range profile.Verifiers {
		switch verifier {
		case "kubernetes_oom":
			if cause := externalOOM(profile, inc, workload, deployment, pods); cause != nil {
				return cause
			}
		case "image_pull":
			if cause := externalImagePull(profile, inc, workload, deployment, pods, byTool["kubernetes_get_events"]); cause != nil {
				return cause
			}
		}
	}
	return nil
}

type scopedPod struct {
	Metadata struct {
		Name   string            `json:"name"`
		Labels map[string]string `json:"labels"`
	} `json:"metadata"`
	Spec struct {
		Containers []struct {
			Name      string `json:"name"`
			Image     string `json:"image"`
			Resources struct {
				Limits map[string]string `json:"limits"`
			} `json:"resources"`
		} `json:"containers"`
	} `json:"spec"`
	Status struct {
		ContainerStatuses []struct {
			Name      string `json:"name"`
			Image     string `json:"image"`
			LastState struct {
				Terminated struct {
					Reason     string    `json:"reason"`
					FinishedAt time.Time `json:"finishedAt"`
				} `json:"terminated"`
			} `json:"lastState"`
			State struct {
				Waiting struct {
					Reason string `json:"reason"`
				} `json:"waiting"`
				Terminated struct {
					Reason     string    `json:"reason"`
					FinishedAt time.Time `json:"finishedAt"`
				} `json:"terminated"`
			} `json:"state"`
		} `json:"containerStatuses"`
	} `json:"status"`
}

func scopedPods(raw json.RawMessage, profile onboarding.Profile, workload onboarding.Workload) []scopedPod {
	var list struct {
		Items []scopedPod `json:"items"`
	}
	if json.Unmarshal(raw, &list) != nil {
		return nil
	}
	key, value, _ := profile.PodLabel(workload.Name)
	var pods []scopedPod
	for _, pod := range list.Items {
		if strings.HasPrefix(pod.Metadata.Name, workload.Name+"-") && pod.Metadata.Labels[key] == value {
			pods = append(pods, pod)
		}
	}
	return pods
}

func scopedDeploymentContainer(raw json.RawMessage, workload onboarding.Workload) (image, pullPolicy, memoryLimit string) {
	var deployment struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
		Spec struct {
			Template struct {
				Spec struct {
					Containers []struct {
						Name            string `json:"name"`
						Image           string `json:"image"`
						ImagePullPolicy string `json:"imagePullPolicy"`
						Resources       struct {
							Limits map[string]string `json:"limits"`
						} `json:"resources"`
					} `json:"containers"`
				} `json:"spec"`
			} `json:"template"`
		} `json:"spec"`
	}
	if json.Unmarshal(raw, &deployment) != nil || deployment.Metadata.Name != workload.Name {
		return "", "", ""
	}
	for _, container := range deployment.Spec.Template.Spec.Containers {
		if container.Name == workload.Container {
			return container.Image, container.ImagePullPolicy, container.Resources.Limits["memory"]
		}
	}
	return "", "", ""
}

func recent(when, start time.Time) bool {
	return !when.IsZero() && !start.IsZero() && !when.Before(start.Add(-2*time.Minute)) && !when.After(time.Now().Add(30*time.Second))
}

func externalOOM(profile onboarding.Profile, inc incident.Incident, workload onboarding.Workload, deployment, pods evidence.Record) *RootCause {
	_, _, limit := scopedDeploymentContainer(deployment.Payload, workload)
	if !memoryLimitPattern.MatchString(limit) {
		return nil
	}
	for _, pod := range scopedPods(pods.Payload, profile, workload) {
		matchingLimit := false
		for _, container := range pod.Spec.Containers {
			if container.Name == workload.Container && container.Resources.Limits["memory"] == limit {
				matchingLimit = true
			}
		}
		if !matchingLimit {
			continue
		}
		for _, status := range pod.Status.ContainerStatuses {
			if status.Name != workload.Container {
				continue
			}
			terminated := status.LastState.Terminated
			if terminated.Reason != "OOMKilled" {
				terminated = status.State.Terminated
			}
			if terminated.Reason == "OOMKilled" && recent(terminated.FinishedAt, inc.StartedAt) {
				return &RootCause{Conclusion: fmt.Sprintf("%s container in pod %s was OOMKilled with a configured %s memory limit", workload.Container, pod.Metadata.Name, limit), Component: workload.Name, Cause: causeMemoryLimitOOM, EvidenceIDs: []string{pods.ID, deployment.ID}}
			}
		}
	}
	return nil
}

func externalImagePull(profile onboarding.Profile, inc incident.Incident, workload onboarding.Workload, deployment, pods, events evidence.Record) *RootCause {
	if events.ID == "" {
		return nil
	}
	image, pullPolicy, _ := scopedDeploymentContainer(deployment.Payload, workload)
	if !imageReferencePattern.MatchString(image) || pullPolicy != "Always" && pullPolicy != "IfNotPresent" {
		return nil
	}
	for _, pod := range scopedPods(pods.Payload, profile, workload) {
		podImage := ""
		for _, container := range pod.Spec.Containers {
			if container.Name == workload.Container {
				podImage = container.Image
			}
		}
		if podImage != image {
			continue
		}
		for _, status := range pod.Status.ContainerStatuses {
			if status.Name == workload.Container && (status.State.Waiting.Reason == "ErrImagePull" || status.State.Waiting.Reason == "ImagePullBackOff") && (status.Image == "" || status.Image == image) && recentPullEvent(events.Payload, pod.Metadata.Name, image, inc.StartedAt) {
				return &RootCause{Conclusion: fmt.Sprintf("%s Deployment references image %s; pod %s entered %s after Kubernetes failed to pull it", workload.Name, image, pod.Metadata.Name, status.State.Waiting.Reason), Component: workload.Name, Cause: causeUnpullableImage, EvidenceIDs: []string{deployment.ID, pods.ID, events.ID}}
			}
		}
	}
	return nil
}

func recentPullEvent(raw json.RawMessage, pod, image string, started time.Time) bool {
	var events struct {
		Items []struct {
			Reason        string    `json:"reason"`
			Message       string    `json:"message"`
			LastTimestamp time.Time `json:"lastTimestamp"`
			Metadata      struct {
				CreationTimestamp time.Time `json:"creationTimestamp"`
			} `json:"metadata"`
			InvolvedObject struct {
				Kind string `json:"kind"`
				Name string `json:"name"`
			} `json:"involvedObject"`
		} `json:"items"`
	}
	if json.Unmarshal(raw, &events) != nil {
		return false
	}
	for _, event := range events.Items {
		when := event.LastTimestamp
		if when.IsZero() {
			when = event.Metadata.CreationTimestamp
		}
		if event.Reason == "Failed" && event.InvolvedObject.Kind == "Pod" && event.InvolvedObject.Name == pod && strings.Contains(strings.ToLower(event.Message), "pull image") && strings.Contains(event.Message, image) && recent(when, started) {
			return true
		}
	}
	return false
}
