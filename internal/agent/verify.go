package agent

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"incidentpilot/internal/evidence"
)

var (
	memoryLimitPattern    = regexp.MustCompile(`^[1-9][0-9]*(Mi|Gi)$`)
	imageReferencePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9./:_@-]{0,254}$`)
)

const (
	causeMemoryLimitOOM   = "memory_limit_oom"
	causeUnpullableImage  = "deployment_references_unpullable_image"
	causeInvalidOrderMode = "unsupported_order_mode_from_configmap"
	causeBrokenSelector   = "service_selector_matches_no_payment_pods"
	causeExcessiveCPU     = "excessive_cpu_work_per_order"
	causePaymentFailure   = "payment_processor_failure_mode_enabled"
)

func verifyClaim(records []evidence.Record, cited []string, component, cause string) (*RootCause, string) {
	switch component {
	case "payments-api":
		switch cause {
		case causeMemoryLimitOOM:
			if verified := verifyOOM(records, cited); verified != nil {
				return verified, ""
			}
			return nil, "missing_cited_oom_or_limit"
		case causeBrokenSelector:
			if verified := verifyBrokenServiceSelector(records, cited); verified != nil {
				return verified, ""
			}
			return nil, "missing_cited_service_routing_evidence"
		case causePaymentFailure:
			if verified := verifyPaymentFailure(records, cited); verified != nil {
				return verified, ""
			}
			return nil, "missing_cited_payment_trace_evidence"
		default:
			return nil, "unsupported_cause"
		}
	case "orders-api":
		switch cause {
		case causeUnpullableImage:
			if verified := verifyImagePull(records, cited); verified != nil {
				return verified, ""
			}
			return nil, "missing_cited_image_pull_evidence"
		case causeInvalidOrderMode:
			if verified := verifyInvalidOrderConfig(records, cited); verified != nil {
				return verified, ""
			}
			return nil, "missing_cited_invalid_config_evidence"
		case causeExcessiveCPU:
			if verified := verifyExcessiveCPU(records, cited); verified != nil {
				return verified, ""
			}
			return nil, "missing_cited_latency_trace_evidence"
		default:
			return nil, "unsupported_cause"
		}
	default:
		return nil, "unsupported_component"
	}
}

func evidenceAlreadySupportsCause(records []evidence.Record, cited []string) bool {
	return verifyOOM(records, cited) != nil || verifyImagePull(records, cited) != nil || verifyInvalidOrderConfig(records, cited) != nil || verifyBrokenServiceSelector(records, cited) != nil || verifyExcessiveCPU(records, cited) != nil || verifyPaymentFailure(records, cited) != nil
}

func verifyExcessiveCPU(records []evidence.Record, cited []string) *RootCause {
	allowed := citedSet(cited)
	var deploymentID, frontendMetricID, ordersMetricID, traceID string
	for _, record := range records {
		if !allowed[record.ID] {
			continue
		}
		switch record.Tool {
		case "kubernetes_get_deployment":
			if record.Source == "kubernetes/deployment" && record.ResourceRef == "incidentpilot-demo/orders-api" && deploymentRuntimeValue(record.Payload, "orders-api", "DEMO_ORDER_CPU_BURN_MS") == "1000" {
				deploymentID = record.ID
			}
		case "prometheus_get_demo_metric_range":
			if record.Source != "prometheus/range" || metricParameter(record.Parameters) != "success_latency_avg" {
				continue
			}
			if record.ResourceRef == "incidentpilot-demo/frontend" && prometheusRangeHasSampleAtLeast(record.Payload, 0.75) {
				frontendMetricID = record.ID
			}
			if record.ResourceRef == "incidentpilot-demo/orders-api" && prometheusRangeHasSampleAtLeast(record.Payload, 0.9) {
				ordersMetricID = record.ID
			}
		case "tempo_get_trace":
			if record.Source == "tempo" && traceShowsSlowSuccessfulCheckout(record.Payload) {
				traceID = record.ID
			}
		}
	}
	if deploymentID == "" || frontendMetricID == "" || ordersMetricID == "" || traceID == "" {
		return nil
	}
	return &RootCause{
		Conclusion:  "orders-api was configured with DEMO_ORDER_CPU_BURN_MS=1000, producing at least 900 ms orders spans and elevated successful request latency while checkout still returned HTTP 200",
		Component:   "orders-api",
		EvidenceIDs: []string{deploymentID, frontendMetricID, ordersMetricID, traceID},
	}
}

func verifyPaymentFailure(records []evidence.Record, cited []string) *RootCause {
	allowed := citedSet(cited)
	var deploymentID, metricID, traceID string
	for _, record := range records {
		if !allowed[record.ID] {
			continue
		}
		switch record.Tool {
		case "kubernetes_get_deployment":
			if record.Source == "kubernetes/deployment" && record.ResourceRef == "incidentpilot-demo/payments-api" && deploymentRuntimeValue(record.Payload, "payments-api", "DEMO_PAYMENT_MODE") == "fail" {
				deploymentID = record.ID
			}
		case "prometheus_get_demo_metric_range":
			if record.Source == "prometheus/range" && record.ResourceRef == "incidentpilot-demo/frontend" && metricParameter(record.Parameters) == "error_rate" && prometheusRangeHasPositiveSample(record.Payload) {
				metricID = record.ID
			}
		case "tempo_get_trace":
			if record.Source == "tempo" && traceShowsPaymentFailure(record.Payload) {
				traceID = record.ID
			}
		}
	}
	if deploymentID == "" || metricID == "" || traceID == "" {
		return nil
	}
	return &RootCause{
		Conclusion:  "payments-api was configured with DEMO_PAYMENT_MODE=fail; one distributed trace records the originating payment HTTP 503 error and propagated orders-api and frontend HTTP 502 errors",
		Component:   "payments-api",
		EvidenceIDs: []string{deploymentID, traceID, metricID},
	}
}

func citedSet(cited []string) map[string]bool {
	allowed := make(map[string]bool, len(cited))
	for _, id := range cited {
		allowed[id] = true
	}
	return allowed
}

func deploymentRuntimeValue(raw json.RawMessage, container, environment string) string {
	var deployment struct {
		Runtime []struct {
			Container   string `json:"container"`
			Environment string `json:"environmentVariable"`
			Value       string `json:"value"`
		} `json:"diagnosticRuntimeConfig"`
	}
	if json.Unmarshal(raw, &deployment) != nil {
		return ""
	}
	for _, item := range deployment.Runtime {
		if item.Container == container && item.Environment == environment {
			return item.Value
		}
	}
	return ""
}

func metricParameter(raw json.RawMessage) string {
	var parameters struct {
		Metric string `json:"metric"`
	}
	_ = json.Unmarshal(raw, &parameters)
	return parameters.Metric
}

func prometheusRangeHasSampleAtLeast(raw json.RawMessage, minimum float64) bool {
	var result struct {
		Result []struct {
			Values [][]json.RawMessage `json:"values"`
		} `json:"result"`
	}
	if json.Unmarshal(raw, &result) != nil {
		return false
	}
	for _, series := range result.Result {
		for _, sample := range series.Values {
			if len(sample) != 2 {
				continue
			}
			var value string
			if json.Unmarshal(sample[1], &value) != nil {
				continue
			}
			parsed, err := strconv.ParseFloat(value, 64)
			if err == nil && parsed >= minimum {
				return true
			}
		}
	}
	return false
}

func traceShowsSlowSuccessfulCheckout(raw json.RawMessage) bool {
	frontend, orders := false, false
	for _, span := range projectTrace(raw) {
		if span.Service == "frontend" && span.Name == "frontend.http" && span.HTTPStatus == 200 && span.DurationMS >= 900 {
			frontend = true
		}
		if span.Service == "orders-api" && span.Name == "orders-api.http" && span.HTTPStatus == 200 && span.DurationMS >= 900 {
			orders = true
		}
	}
	return frontend && orders
}

func traceShowsPaymentFailure(raw json.RawMessage) bool {
	frontend, orders, payments := false, false, false
	for _, span := range projectTrace(raw) {
		errorStatus := span.StatusCode == "STATUS_CODE_ERROR" || span.StatusCode == "ERROR"
		switch {
		case span.Service == "frontend" && span.Name == "frontend.http" && span.HTTPStatus == 502 && errorStatus:
			frontend = true
		case span.Service == "orders-api" && span.Name == "orders-api.http" && span.HTTPStatus == 502 && errorStatus:
			orders = true
		case span.Service == "payments-api" && span.Name == "payments-api.http" && span.HTTPStatus == 503 && errorStatus:
			payments = true
		}
	}
	return frontend && orders && payments
}

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

type imagePullPod struct {
	Name, Image, Reason string
}

type deploymentImage struct {
	Image, PullPolicy string
}

func verifyImagePull(records []evidence.Record, cited []string) *RootCause {
	allowed := make(map[string]bool, len(cited))
	for _, id := range cited {
		allowed[id] = true
	}
	var podRecordID, deploymentRecordID, eventRecordID string
	var failedPod imagePullPod
	var configured deploymentImage
	var eventPayload json.RawMessage
	for _, record := range records {
		if !allowed[record.ID] || record.ResourceRef != "incidentpilot-demo/orders-api" {
			continue
		}
		switch record.Tool {
		case "kubernetes_get_pods":
			if pod := podWaitingForImage(record.Payload); record.Source == "kubernetes/pods" && pod != nil {
				podRecordID, failedPod = record.ID, *pod
			}
		case "kubernetes_get_deployment":
			if deployment := ordersDeploymentImage(record.Payload); record.Source == "kubernetes/deployment" && deployment != nil {
				deploymentRecordID, configured = record.ID, *deployment
			}
		case "kubernetes_get_events":
			if record.Source == "kubernetes/events" {
				eventRecordID, eventPayload = record.ID, record.Payload
			}
		}
	}
	if podRecordID == "" || deploymentRecordID == "" || eventRecordID == "" || failedPod.Image != configured.Image || configured.PullPolicy != "Always" || !imageReferencePattern.MatchString(configured.Image) || !eventsShowFailedPull(eventPayload, failedPod.Name, configured.Image) {
		return nil
	}
	return &RootCause{
		Conclusion:  fmt.Sprintf("orders-api Deployment references image %s with pull policy Always; its new pod entered %s after Kubernetes failed to pull that image", configured.Image, failedPod.Reason),
		Component:   "orders-api",
		EvidenceIDs: []string{podRecordID, deploymentRecordID, eventRecordID},
	}
}

func podWaitingForImage(raw json.RawMessage) *imagePullPod {
	var pods struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Spec struct {
				Containers []struct {
					Name  string `json:"name"`
					Image string `json:"image"`
				} `json:"containers"`
			} `json:"spec"`
			Status struct {
				ContainerStatuses []struct {
					Name  string `json:"name"`
					Image string `json:"image"`
					State struct {
						Waiting struct {
							Reason string `json:"reason"`
						} `json:"waiting"`
					} `json:"state"`
				} `json:"containerStatuses"`
			} `json:"status"`
		} `json:"items"`
	}
	if json.Unmarshal(raw, &pods) != nil {
		return nil
	}
	for _, pod := range pods.Items {
		image := ""
		for _, container := range pod.Spec.Containers {
			if container.Name == "orders-api" {
				image = container.Image
				break
			}
		}
		for _, status := range pod.Status.ContainerStatuses {
			if status.Name != "orders-api" || (status.State.Waiting.Reason != "ImagePullBackOff" && status.State.Waiting.Reason != "ErrImagePull") {
				continue
			}
			if status.Image != "" {
				image = status.Image
			}
			if pod.Metadata.Name != "" && imageReferencePattern.MatchString(image) {
				return &imagePullPod{Name: pod.Metadata.Name, Image: image, Reason: status.State.Waiting.Reason}
			}
		}
	}
	return nil
}

func ordersDeploymentImage(raw json.RawMessage) *deploymentImage {
	var deployment struct {
		Spec struct {
			Template struct {
				Spec struct {
					Containers []struct {
						Name       string `json:"name"`
						Image      string `json:"image"`
						PullPolicy string `json:"imagePullPolicy"`
					} `json:"containers"`
				} `json:"spec"`
			} `json:"template"`
		} `json:"spec"`
	}
	if json.Unmarshal(raw, &deployment) != nil {
		return nil
	}
	for _, container := range deployment.Spec.Template.Spec.Containers {
		if container.Name == "orders-api" && imageReferencePattern.MatchString(container.Image) {
			return &deploymentImage{Image: container.Image, PullPolicy: container.PullPolicy}
		}
	}
	return nil
}

func eventsShowFailedPull(raw json.RawMessage, pod, image string) bool {
	var events struct {
		Items []struct {
			Reason  string `json:"reason"`
			Message string `json:"message"`
			Object  struct {
				Kind string `json:"kind"`
				Name string `json:"name"`
			} `json:"involvedObject"`
		} `json:"items"`
	}
	if json.Unmarshal(raw, &events) != nil {
		return false
	}
	for _, event := range events.Items {
		message := strings.ToLower(event.Message)
		if event.Object.Kind == "Pod" && event.Object.Name == pod && event.Reason == "Failed" && strings.Contains(message, "pull image") && strings.Contains(event.Message, image) {
			return true
		}
	}
	return false
}

func verifyInvalidOrderConfig(records []evidence.Record, cited []string) *RootCause {
	allowed := make(map[string]bool, len(cited))
	for _, id := range cited {
		allowed[id] = true
	}
	var configID, deploymentID, podID, logID, failedPod string
	for _, record := range records {
		if !allowed[record.ID] {
			continue
		}
		switch record.Tool {
		case "kubernetes_get_orders_config":
			if record.Source == "kubernetes/configmap" && record.ResourceRef == "incidentpilot-demo/configmap/orders-config" && ordersConfigIsUnsupported(record.Payload) {
				configID = record.ID
			}
		case "kubernetes_get_deployment":
			if record.Source == "kubernetes/deployment" && record.ResourceRef == "incidentpilot-demo/orders-api" && deploymentReferencesOrdersConfig(record.Payload) {
				deploymentID = record.ID
			}
		case "kubernetes_get_pods":
			if record.Source == "kubernetes/pods" && record.ResourceRef == "incidentpilot-demo/orders-api" {
				if pod := ordersCrashLoopPod(record.Payload); pod != "" {
					podID, failedPod = record.ID, pod
				}
			}
		}
	}
	if failedPod != "" {
		for _, record := range records {
			if allowed[record.ID] && record.Tool == "kubernetes_get_pod_logs" && record.Source == "kubernetes/pod_logs" && record.ResourceRef == "incidentpilot-demo/"+failedPod && logsShowInvalidOrderMode(record.Payload) {
				logID = record.ID
				break
			}
		}
	}
	if configID == "" || deploymentID == "" || podID == "" || logID == "" {
		return nil
	}
	return &RootCause{
		Conclusion:  fmt.Sprintf("orders-api consumed orders-config key order_mode=unsupported through DEMO_ORDER_MODE; pod %s entered CrashLoopBackOff and logged an invalid order mode", failedPod),
		Component:   "orders-api",
		EvidenceIDs: []string{configID, deploymentID, podID, logID},
	}
}

func ordersConfigIsUnsupported(raw json.RawMessage) bool {
	var config struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
		Data struct {
			OrderMode string `json:"order_mode"`
		} `json:"data"`
	}
	return json.Unmarshal(raw, &config) == nil && config.Metadata.Name == "orders-config" && config.Data.OrderMode == "unsupported"
}

func deploymentReferencesOrdersConfig(raw json.RawMessage) bool {
	var deployment struct {
		References []struct {
			Container   string `json:"container"`
			Environment string `json:"environmentVariable"`
			ConfigMap   string `json:"configMap"`
			Key         string `json:"key"`
		} `json:"diagnosticConfigReferences"`
	}
	if json.Unmarshal(raw, &deployment) != nil {
		return false
	}
	for _, ref := range deployment.References {
		if ref.Container == "orders-api" && ref.Environment == "DEMO_ORDER_MODE" && ref.ConfigMap == "orders-config" && ref.Key == "order_mode" {
			return true
		}
	}
	return false
}

func ordersCrashLoopPod(raw json.RawMessage) string {
	var pods struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Status struct {
				ContainerStatuses []struct {
					Name  string `json:"name"`
					State struct {
						Waiting struct {
							Reason string `json:"reason"`
						} `json:"waiting"`
					} `json:"state"`
				} `json:"containerStatuses"`
			} `json:"status"`
		} `json:"items"`
	}
	if json.Unmarshal(raw, &pods) != nil {
		return ""
	}
	for _, pod := range pods.Items {
		for _, status := range pod.Status.ContainerStatuses {
			if status.Name == "orders-api" && status.State.Waiting.Reason == "CrashLoopBackOff" && imageReferencePattern.MatchString(pod.Metadata.Name) {
				return pod.Metadata.Name
			}
		}
	}
	return ""
}

func logsShowInvalidOrderMode(raw json.RawMessage) bool {
	var log struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &log) != nil {
		return false
	}
	return strings.Contains(log.Text, `invalid order mode "unsupported"`) || strings.Contains(log.Text, `invalid order mode \"unsupported\"`)
}

func verifyBrokenServiceSelector(records []evidence.Record, cited []string) *RootCause {
	allowed := make(map[string]bool, len(cited))
	for _, id := range cited {
		allowed[id] = true
	}
	var serviceID, endpointID, podID, metricID string
	for _, record := range records {
		if !allowed[record.ID] {
			continue
		}
		switch record.Tool {
		case "kubernetes_get_service":
			if record.Source == "kubernetes/service" && record.ResourceRef == "incidentpilot-demo/payments-api" && paymentsServiceSelector(record.Payload) == "payments-api-disconnected" {
				serviceID = record.ID
			}
		case "kubernetes_get_endpointslices":
			if record.Source == "kubernetes/endpointslices" && record.ResourceRef == "incidentpilot-demo/payments-api" && paymentEndpointSlicesAreEmpty(record.Payload) {
				endpointID = record.ID
			}
		case "kubernetes_get_pods":
			if record.Source == "kubernetes/pods" && record.ResourceRef == "incidentpilot-demo/payments-api" && paymentsPodIsReady(record.Payload) {
				podID = record.ID
			}
		case "prometheus_get_demo_metric_range":
			if record.Source == "prometheus/range" && record.ResourceRef == "incidentpilot-demo/frontend" && prometheusRangeHasPositiveSample(record.Payload) {
				metricID = record.ID
			}
		}
	}
	if serviceID == "" || endpointID == "" || podID == "" || metricID == "" {
		return nil
	}
	return &RootCause{
		Conclusion:  "payments-api Service selector app=payments-api-disconnected matched no endpoint addresses while payments-api pods remained ready, causing frontend upstream errors",
		Component:   "payments-api",
		EvidenceIDs: []string{serviceID, endpointID, podID, metricID},
	}
}

func paymentsServiceSelector(raw json.RawMessage) string {
	var service struct {
		Spec struct {
			Selector map[string]string `json:"selector"`
		} `json:"spec"`
	}
	if json.Unmarshal(raw, &service) != nil {
		return ""
	}
	return service.Spec.Selector["app"]
}

func paymentEndpointSlicesAreEmpty(raw json.RawMessage) bool {
	var slices struct {
		Items []struct {
			Metadata struct {
				Labels map[string]string `json:"labels"`
			} `json:"metadata"`
			Endpoints []struct {
				Addresses []string `json:"addresses"`
			} `json:"endpoints"`
		} `json:"items"`
	}
	if json.Unmarshal(raw, &slices) != nil || len(slices.Items) == 0 {
		return false
	}
	for _, slice := range slices.Items {
		if slice.Metadata.Labels["kubernetes.io/service-name"] != "payments-api" {
			return false
		}
		for _, endpoint := range slice.Endpoints {
			if len(endpoint.Addresses) != 0 {
				return false
			}
		}
	}
	return true
}

func paymentsPodIsReady(raw json.RawMessage) bool {
	var pods struct {
		Items []struct {
			Metadata struct {
				Labels map[string]string `json:"labels"`
			} `json:"metadata"`
			Status struct {
				Phase             string `json:"phase"`
				ContainerStatuses []struct {
					Name  string `json:"name"`
					Ready bool   `json:"ready"`
				} `json:"containerStatuses"`
			} `json:"status"`
		} `json:"items"`
	}
	if json.Unmarshal(raw, &pods) != nil {
		return false
	}
	for _, pod := range pods.Items {
		if pod.Metadata.Labels["app"] != "payments-api" || pod.Status.Phase != "Running" {
			continue
		}
		for _, status := range pod.Status.ContainerStatuses {
			if status.Name == "payments-api" && status.Ready {
				return true
			}
		}
	}
	return false
}

func prometheusRangeHasPositiveSample(raw json.RawMessage) bool {
	var result struct {
		Result []struct {
			Values [][]json.RawMessage `json:"values"`
		} `json:"result"`
	}
	if json.Unmarshal(raw, &result) != nil {
		return false
	}
	for _, series := range result.Result {
		for _, sample := range series.Values {
			if len(sample) != 2 {
				continue
			}
			var value string
			if json.Unmarshal(sample[1], &value) != nil {
				continue
			}
			parsed, err := strconv.ParseFloat(value, 64)
			if err == nil && parsed > 0 {
				return true
			}
		}
	}
	return false
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
