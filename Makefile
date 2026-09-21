GO ?= go
GOFMT ?= gofmt
DOCKER ?= docker
KIND ?= kind
KUBECTL ?= kubectl
KIND_NAME ?= incidentpilot
KIND_CONTEXT := kind-$(KIND_NAME)

.PHONY: fmt fmt-check vet test lint build check run container-build demo-images kind-create kind-load kind-deploy kind-refresh kind-up kind-status kind-down scenario-oom-inject scenario-oom-check scenario-oom-reset scenario-bad-image-inject scenario-bad-image-check scenario-bad-image-reset scenario-invalid-configmap-inject scenario-invalid-configmap-check scenario-invalid-configmap-reset scenario-broken-selector-inject scenario-broken-selector-check scenario-broken-selector-reset scenario-cpu-latency-inject scenario-cpu-latency-check scenario-cpu-latency-reset scenario-downstream-failure-inject scenario-downstream-failure-check scenario-downstream-failure-reset

fmt:
	$(GOFMT) -w .

fmt-check:
	@test -z "$$($(GOFMT) -l .)" || { echo "Go files need formatting; run make fmt"; exit 1; }

vet:
	$(GO) vet ./...

test:
	$(GO) test ./...

lint: fmt-check vet

build:
	$(GO) build -buildvcs=false -o bin/incidentpilot-api ./cmd/api
	$(GO) build -buildvcs=false -o bin/incidentpilot-agent ./cmd/agent
	$(GO) build -buildvcs=false -o bin/incidentpilot-demo ./cmd/demo
	$(GO) build -buildvcs=false -o bin/incidentpilot-traffic ./cmd/traffic
	$(GO) build -buildvcs=false -o bin/incidentpilot-mcp ./cmd/mcp

check: lint test build

run:
	$(GO) run -buildvcs=false ./cmd/api

container-build:
	$(DOCKER) build -t incidentpilot-api:local .
	$(DOCKER) build -f agent/Dockerfile -t incidentpilot-agent:local .
	$(DOCKER) build -f mcp/Dockerfile -t incidentpilot-mcp:local .
	$(MAKE) demo-images

demo-images:
	$(DOCKER) build -f demo/Dockerfile --build-arg TARGET=demo -t incidentpilot-demo:local .
	$(DOCKER) build -f demo/Dockerfile --build-arg TARGET=traffic -t incidentpilot-traffic:local .

kind-create:
	$(KIND) get clusters | grep -qx '$(KIND_NAME)' || $(KIND) create cluster --name $(KIND_NAME)

kind-load:
	$(KIND) load docker-image --name $(KIND_NAME) incidentpilot-api:local incidentpilot-agent:local incidentpilot-mcp:local incidentpilot-demo:local incidentpilot-traffic:local

kind-deploy:
	$(KUBECTL) --context $(KIND_CONTEXT) apply -f deploy/kind/00-namespaces.yaml
	$(KUBECTL) --context $(KIND_CONTEXT) apply -f deploy/kind/10-config.yaml
	$(KUBECTL) --context $(KIND_CONTEXT) apply -f deploy/kind/20-observability.yaml
	$(KUBECTL) --context $(KIND_CONTEXT) apply -f deploy/kind/30-demo.yaml
	$(KUBECTL) --context $(KIND_CONTEXT) apply -f deploy/kind/40-incident.yaml
	$(KUBECTL) --context $(KIND_CONTEXT) apply -f deploy/kind/50-mcp.yaml

kind-refresh:
	$(KUBECTL) --context $(KIND_CONTEXT) -n incidentpilot-observability rollout restart daemonset/otel-collector deployment/prometheus deployment/loki deployment/tempo deployment/grafana
	$(KUBECTL) --context $(KIND_CONTEXT) -n incidentpilot-demo rollout restart deployment/frontend deployment/orders-api deployment/payments-api deployment/traffic-generator
	$(KUBECTL) --context $(KIND_CONTEXT) -n incidentpilot-system rollout restart deployment/incidentpilot-api deployment/alertmanager deployment/incidentpilot-mcp

kind-up: kind-create container-build kind-load kind-deploy kind-refresh

kind-status:
	$(KUBECTL) --context $(KIND_CONTEXT) -n incidentpilot-observability get pods
	$(KUBECTL) --context $(KIND_CONTEXT) -n incidentpilot-demo get pods
	$(KUBECTL) --context $(KIND_CONTEXT) -n incidentpilot-system get pods

kind-down:
	$(KIND) delete cluster --name $(KIND_NAME)

scenario-oom-inject:
	KUBECTL=$(KUBECTL) sh scenarios/oom-memory-limit/run.sh inject

scenario-oom-check:
	KUBECTL=$(KUBECTL) sh scenarios/oom-memory-limit/run.sh check

scenario-oom-reset:
	KUBECTL=$(KUBECTL) sh scenarios/oom-memory-limit/run.sh reset

scenario-bad-image-inject:
	KUBECTL=$(KUBECTL) sh scenarios/bad-image/run.sh inject

scenario-bad-image-check:
	KUBECTL=$(KUBECTL) sh scenarios/bad-image/run.sh check

scenario-bad-image-reset:
	KUBECTL=$(KUBECTL) sh scenarios/bad-image/run.sh reset

scenario-invalid-configmap-inject:
	KUBECTL=$(KUBECTL) sh scenarios/invalid-configmap/run.sh inject

scenario-invalid-configmap-check:
	KUBECTL=$(KUBECTL) sh scenarios/invalid-configmap/run.sh check

scenario-invalid-configmap-reset:
	KUBECTL=$(KUBECTL) sh scenarios/invalid-configmap/run.sh reset

scenario-broken-selector-inject:
	KUBECTL=$(KUBECTL) sh scenarios/broken-service-selector/run.sh inject

scenario-broken-selector-check:
	KUBECTL=$(KUBECTL) sh scenarios/broken-service-selector/run.sh check

scenario-broken-selector-reset:
	KUBECTL=$(KUBECTL) sh scenarios/broken-service-selector/run.sh reset

scenario-cpu-latency-inject:
	KUBECTL=$(KUBECTL) sh scenarios/cpu-latency/run.sh inject

scenario-cpu-latency-check:
	KUBECTL=$(KUBECTL) sh scenarios/cpu-latency/run.sh check

scenario-cpu-latency-reset:
	KUBECTL=$(KUBECTL) sh scenarios/cpu-latency/run.sh reset

scenario-downstream-failure-inject:
	KUBECTL=$(KUBECTL) sh scenarios/downstream-failure/run.sh inject

scenario-downstream-failure-check:
	KUBECTL=$(KUBECTL) sh scenarios/downstream-failure/run.sh check

scenario-downstream-failure-reset:
	KUBECTL=$(KUBECTL) sh scenarios/downstream-failure/run.sh reset
