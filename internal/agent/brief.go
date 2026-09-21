package agent

import (
	"encoding/json"
	"strconv"
	"strings"

	"incidentpilot/internal/evidence"
)

// diagnosticBrief projects known diagnostic fields, keeping arbitrary labels,
// annotations, and long Kubernetes objects out of the model prompt.
func diagnosticBrief(record evidence.Record) string {
	switch record.Tool {
	case "kubernetes_get_deployment":
		var dep struct {
			RuntimeConfig []struct {
				Container   string `json:"container"`
				Environment string `json:"environmentVariable"`
				Value       string `json:"value"`
			} `json:"diagnosticRuntimeConfig"`
			ConfigReferences []struct {
				Container   string `json:"container"`
				Environment string `json:"environmentVariable"`
				ConfigMap   string `json:"configMap"`
				Key         string `json:"key"`
			} `json:"diagnosticConfigReferences"`
			Spec struct {
				Replicas int `json:"replicas"`
				Template struct {
					Spec struct {
						Containers []struct {
							Name       string `json:"name"`
							Image      string `json:"image"`
							PullPolicy string `json:"imagePullPolicy"`
							Resources  struct {
								Limits map[string]string `json:"limits"`
							} `json:"resources"`
						} `json:"containers"`
					} `json:"spec"`
				} `json:"template"`
			} `json:"spec"`
			Status struct {
				UpdatedReplicas     int `json:"updatedReplicas"`
				AvailableReplicas   int `json:"availableReplicas"`
				UnavailableReplicas int `json:"unavailableReplicas"`
			} `json:"status"`
		}
		if json.Unmarshal(record.Payload, &dep) != nil {
			return ""
		}
		out := struct {
			Desired     int `json:"desired"`
			Updated     int `json:"updated"`
			Available   int `json:"available"`
			Unavailable int `json:"unavailable"`
			ConfigRefs  []struct {
				Container   string `json:"container"`
				Environment string `json:"environmentVariable"`
				ConfigMap   string `json:"configMap"`
				Key         string `json:"key"`
			} `json:"config_references,omitempty"`
			RuntimeConfig []struct {
				Container   string `json:"container"`
				Environment string `json:"environmentVariable"`
				Value       string `json:"value"`
			} `json:"runtime_config,omitempty"`
			Containers []struct {
				Name        string `json:"name"`
				Image       string `json:"image"`
				PullPolicy  string `json:"pull_policy"`
				MemoryLimit string `json:"memory_limit"`
			} `json:"containers"`
		}{Desired: dep.Spec.Replicas, Updated: dep.Status.UpdatedReplicas, Available: dep.Status.AvailableReplicas, Unavailable: dep.Status.UnavailableReplicas, ConfigRefs: dep.ConfigReferences, RuntimeConfig: dep.RuntimeConfig}
		for _, container := range dep.Spec.Template.Spec.Containers {
			if len(out.Containers) >= 4 {
				break
			}
			out.Containers = append(out.Containers, struct {
				Name        string `json:"name"`
				Image       string `json:"image"`
				PullPolicy  string `json:"pull_policy"`
				MemoryLimit string `json:"memory_limit"`
			}{container.Name, container.Image, container.PullPolicy, container.Resources.Limits["memory"]})
		}
		encoded, _ := json.Marshal(out)
		return string(encoded)
	case "kubernetes_get_orders_config":
		var config struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Data struct {
				OrderMode string `json:"order_mode"`
			} `json:"data"`
		}
		if json.Unmarshal(record.Payload, &config) != nil {
			return ""
		}
		encoded, _ := json.Marshal(config)
		return string(encoded)
	case "kubernetes_get_service":
		var service struct {
			Spec struct {
				Selector map[string]string `json:"selector"`
			} `json:"spec"`
		}
		if json.Unmarshal(record.Payload, &service) != nil {
			return ""
		}
		encoded, _ := json.Marshal(struct {
			Selector map[string]string `json:"selector"`
		}{Selector: service.Spec.Selector})
		return string(encoded)
	case "kubernetes_get_endpointslices":
		var slices struct {
			Items []struct {
				Metadata struct {
					Name string `json:"name"`
				} `json:"metadata"`
				AddressType string `json:"addressType"`
				Endpoints   []struct {
					Addresses  []string `json:"addresses"`
					Conditions struct {
						Ready *bool `json:"ready"`
					} `json:"conditions"`
				} `json:"endpoints"`
			} `json:"items"`
		}
		if json.Unmarshal(record.Payload, &slices) != nil {
			return ""
		}
		if len(slices.Items) > 20 {
			slices.Items = slices.Items[:20]
		}
		for index := range slices.Items {
			if len(slices.Items[index].Endpoints) > 20 {
				slices.Items[index].Endpoints = slices.Items[index].Endpoints[:20]
			}
			for endpoint := range slices.Items[index].Endpoints {
				if len(slices.Items[index].Endpoints[endpoint].Addresses) > 8 {
					slices.Items[index].Endpoints[endpoint].Addresses = slices.Items[index].Endpoints[endpoint].Addresses[:8]
				}
			}
		}
		encoded, _ := json.Marshal(slices.Items)
		return string(encoded)
	case "kubernetes_get_pods":
		var pods struct {
			Items []struct {
				Metadata struct {
					Name   string            `json:"name"`
					Labels map[string]string `json:"labels"`
				} `json:"metadata"`
				Spec struct {
					Containers []struct {
						Name  string `json:"name"`
						Image string `json:"image"`
					} `json:"containers"`
				} `json:"spec"`
				Status struct {
					Phase             string `json:"phase"`
					ContainerStatuses []struct {
						Name         string `json:"name"`
						Image        string `json:"image"`
						Ready        bool   `json:"ready"`
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
			Image          string `json:"image"`
			Ready          bool   `json:"ready"`
			Restarts       int    `json:"restarts"`
			LastTerminated string `json:"last_terminated"`
			Waiting        string `json:"waiting"`
		}
		type pod struct {
			Name       string   `json:"name"`
			AppLabel   string   `json:"app_label"`
			Phase      string   `json:"phase"`
			Containers []status `json:"containers"`
		}
		out := make([]pod, 0, 10)
		for _, item := range pods.Items {
			if len(out) >= 10 {
				break
			}
			entry := pod{Name: item.Metadata.Name, AppLabel: item.Metadata.Labels["app"], Phase: item.Status.Phase}
			for _, container := range item.Status.ContainerStatuses {
				if len(entry.Containers) >= 4 {
					break
				}
				image := container.Image
				if image == "" {
					for _, declared := range item.Spec.Containers {
						if declared.Name == container.Name {
							image = declared.Image
							break
						}
					}
				}
				entry.Containers = append(entry.Containers, status{container.Name, image, container.Ready, container.RestartCount, container.LastState.Terminated.Reason, container.State.Waiting.Reason})
			}
			out = append(out, entry)
		}
		encoded, _ := json.Marshal(out)
		return string(encoded)
	case "kubernetes_get_events":
		var events struct {
			Items []struct {
				Type    string `json:"type"`
				Reason  string `json:"reason"`
				Message string `json:"message"`
				Object  struct {
					Kind string `json:"kind"`
					Name string `json:"name"`
				} `json:"involvedObject"`
			} `json:"items"`
		}
		if json.Unmarshal(record.Payload, &events) != nil {
			return ""
		}
		if len(events.Items) > 10 {
			events.Items = events.Items[:10]
		}
		for index := range events.Items {
			events.Items[index].Message = strings.ToValidUTF8(events.Items[index].Message, "")
			if len(events.Items[index].Message) > 512 {
				events.Items[index].Message = events.Items[index].Message[:512]
			}
		}
		encoded, _ := json.Marshal(events.Items)
		return string(encoded)
	case "kubernetes_get_pod_logs":
		var log struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(record.Payload, &log) != nil {
			return ""
		}
		log.Text = strings.ToValidUTF8(log.Text, "")
		if len(log.Text) > 2048 {
			log.Text = log.Text[len(log.Text)-2048:]
		}
		encoded, _ := json.Marshal(log)
		return string(encoded)
	case "prometheus_get_demo_metric_range":
		var metric struct {
			Result []struct {
				Values [][]json.RawMessage `json:"values"`
			} `json:"result"`
		}
		var params struct {
			Metric string `json:"metric"`
		}
		if json.Unmarshal(record.Payload, &metric) != nil || json.Unmarshal(record.Parameters, &params) != nil {
			return ""
		}
		if len(metric.Result) > 4 {
			metric.Result = metric.Result[:4]
		}
		for index := range metric.Result {
			if len(metric.Result[index].Values) > 20 {
				metric.Result[index].Values = metric.Result[index].Values[len(metric.Result[index].Values)-20:]
			}
		}
		encoded, _ := json.Marshal(struct {
			Metric string `json:"metric"`
			Series any    `json:"series"`
		}{Metric: params.Metric, Series: metric.Result})
		return string(encoded)
	case "tempo_get_trace":
		spans := projectTrace(record.Payload)
		if len(spans) == 0 {
			return ""
		}
		encoded, _ := json.Marshal(spans)
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

type projectedTraceSpan struct {
	Service    string `json:"service"`
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	DurationMS int64  `json:"duration_ms"`
	HTTPStatus int64  `json:"http_status,omitempty"`
	StatusCode string `json:"status_code,omitempty"`
}

type otelAttribute struct {
	Key   string `json:"key"`
	Value struct {
		StringValue string          `json:"stringValue"`
		IntValue    json.RawMessage `json:"intValue"`
	} `json:"value"`
}

func projectTrace(raw json.RawMessage) []projectedTraceSpan {
	var trace struct {
		Batches []struct {
			Resource struct {
				Attributes []otelAttribute `json:"attributes"`
			} `json:"resource"`
			ScopeSpans []struct {
				Spans []struct {
					Name       string          `json:"name"`
					Kind       string          `json:"kind"`
					Start      json.RawMessage `json:"startTimeUnixNano"`
					End        json.RawMessage `json:"endTimeUnixNano"`
					Attributes []otelAttribute `json:"attributes"`
					Status     struct {
						Code string `json:"code"`
					} `json:"status"`
				} `json:"spans"`
			} `json:"scopeSpans"`
		} `json:"batches"`
	}
	if json.Unmarshal(raw, &trace) != nil {
		return nil
	}
	out := make([]projectedTraceSpan, 0, 12)
	for _, batch := range trace.Batches {
		service := traceStringAttribute(batch.Resource.Attributes, "service.name")
		if service != "frontend" && service != "orders-api" && service != "payments-api" {
			continue
		}
		for _, scope := range batch.ScopeSpans {
			for _, span := range scope.Spans {
				if len(out) >= 24 {
					return out
				}
				start, startOK := traceInteger(span.Start)
				end, endOK := traceInteger(span.End)
				if !startOK || !endOK || end < start {
					continue
				}
				name := strings.ToValidUTF8(span.Name, "")
				if len(name) > 128 {
					name = name[:128]
				}
				out = append(out, projectedTraceSpan{Service: service, Name: name, Kind: span.Kind, DurationMS: (end - start) / 1_000_000, HTTPStatus: traceIntAttribute(span.Attributes, "http.response.status_code"), StatusCode: span.Status.Code})
			}
		}
	}
	return out
}

func traceStringAttribute(attributes []otelAttribute, key string) string {
	for _, attribute := range attributes {
		if attribute.Key == key {
			return attribute.Value.StringValue
		}
	}
	return ""
}

func traceIntAttribute(attributes []otelAttribute, key string) int64 {
	for _, attribute := range attributes {
		if attribute.Key == key {
			value, _ := traceInteger(attribute.Value.IntValue)
			return value
		}
	}
	return 0
}

func traceInteger(raw json.RawMessage) (int64, bool) {
	if len(raw) == 0 {
		return 0, false
	}
	var text string
	if raw[0] == '"' {
		if json.Unmarshal(raw, &text) != nil {
			return 0, false
		}
	} else {
		text = string(raw)
	}
	value, err := strconv.ParseInt(text, 10, 64)
	return value, err == nil
}
