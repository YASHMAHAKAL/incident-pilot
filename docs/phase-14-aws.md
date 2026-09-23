# Phase 14: AWS and GitOps

Phase 14 keeps the local architecture boundaries on EKS. Terraform owns the VPC, two-AZ subnet/routing layout, NAT/Internet gateways, EKS control plane, private managed nodes, IAM, KMS encryption, control-plane logs, and security group. Argo CD owns the IncidentPilot Helm release plus pinned Prometheus/Grafana, Loki, Tempo, and OpenTelemetry Collector applications.

When installing into a cluster that already has an application, use the [external onboarding profile](onboarding-external-cluster.md) and deploy only the IncidentPilot chart and the dependencies you actually need. The demo application and its alert rules are disabled in that profile.

No repository command automatically applies Terraform. EKS, NAT gateways, nodes, public IPv4 addresses, storage, and telemetry retention incur AWS charges.

## 1. Validate and review infrastructure

```sh
cp infrastructure/terraform/aws/terraform.tfvars.example infrastructure/terraform/aws/terraform.tfvars
# Set a real trusted public /32 or VPN CIDR and review region/version/cost.
make infra-check
terraform -chdir=infrastructure/terraform/aws plan -out=/tmp/incidentpilot.tfplan
```

Configure an encrypted remote backend before using this with a team. Do not commit `.tfstate`, plan files, AWS credentials, kubeconfigs, or secret values. Applying and destroying are explicit operator actions outside repository automation.

## 2. Publish immutable images

Build the API, MCP, agent, demo, and traffic images in CI, scan them, publish them to a private registry, and replace the `latest` placeholders in Helm values with immutable digests. Argo CD should not deploy local kind image names.

## 3. Bootstrap Argo CD once

After an authorized Terraform apply and `aws eks update-kubeconfig`, bootstrap the controller with the pinned chart, then configure private-repository access using a read-only deploy key or GitHub App:

```sh
helm repo add argo https://argoproj.github.io/argo-helm
helm upgrade --install argocd argo/argo-cd --version 10.9.2 \
  --namespace argocd --create-namespace --set configs.params.server\.insecure=false

argocd repo add git@github.com:YASHMAHAKAL/incident-pilot.git \
  --ssh-private-key-path /secure/path/to/read-only-deploy-key
kubectl apply -f deploy/argocd/root-application.yaml
```

The root application discovers the child applications. Pin updates are reviewed Git changes; Argo performs reconciliation.

## 4. Supply secrets outside Git

Before syncing IncidentPilot, create `incidentpilot-runtime` in `incidentpilot-system` through your secret manager. It must contain `database_url`, `postgres_password`, `webhook_token`, `mcp_token`, and `remediation_token`. Tokens must satisfy the application length constraints. If GitHub remediation is enabled, create the separately named secret configured by `runtime.githubSecret` with the existing GitHub adapter environment keys.

The current v1 Rego policy denies remediation when `runtime.environment` is `production`; investigation and reporting still work. Enabling production PR creation requires an explicit, separately reviewed policy change—it is not activated by this deployment chart.

Alertmanager cannot mount a Secret from another namespace. Create `incidentpilot-alertmanager-auth` in `incidentpilot-observability` with a `token` key containing the same webhook token. Prefer External Secrets or another audited secret controller rather than imperative long-lived values.

## Production-like, not production-certified

The chart uses two replicas and disruption budgets for stateless services, persistent telemetry/PostgreSQL storage, private worker nodes, encrypted Kubernetes Secrets, and restricted MCP RBAC. A real production review still needs organization-specific DNS/TLS/Ingress, backup/restore, managed database choice, network policy/CNI controls, IAM access entries, image provenance, autoscaling, disaster recovery, budget alarms, and retention requirements. The one-shot investigator remains operator-triggered, as in local v1.
