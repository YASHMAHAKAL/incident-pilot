# IncidentPilot EKS infrastructure

This Terraform root creates the Phase 14 AWS boundary: one VPC, public and private subnets across two availability zones, an internet gateway, configurable NAT gateways, routing, encrypted EKS control plane, managed private nodes, IAM roles, control-plane logs, and an additional cluster security group.

It intentionally does not install Kubernetes applications or create application credentials. Terraform owns AWS infrastructure; Argo CD and Helm own the in-cluster architecture. State is local until you configure an organization-owned encrypted S3 backend with locking.

Before any apply, copy `terraform.tfvars.example`, replace the documentation-only CIDR, review current EKS version availability in the selected region, estimate NAT/EKS costs, and configure AWS credentials outside the repository.

```sh
terraform init
terraform fmt -check
terraform validate
terraform plan -out=/tmp/incidentpilot.tfplan
```

`terraform apply` is intentionally not part of repository automation because it creates billable resources. For a cost-conscious portfolio deployment, destroy the environment after capturing the demonstration and retain no credentials or Terraform state in Git.
