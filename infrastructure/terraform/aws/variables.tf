variable "aws_region" {
  description = "AWS region for the EKS cluster."
  type        = string
  default     = "ap-south-1"
}

variable "name" {
  description = "Prefix used for AWS resources."
  type        = string
  default     = "incidentpilot"

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{2,30}$", var.name))
    error_message = "name must be a lower-case DNS-style name between 3 and 31 characters."
  }
}

variable "vpc_cidr" {
  description = "CIDR allocated to the cluster VPC."
  type        = string
  default     = "10.42.0.0/16"
}

variable "kubernetes_version" {
  description = "EKS control-plane version; verify regional support before applying."
  type        = string
  default     = "1.34"
}

variable "public_access_cidrs" {
  description = "Explicit CIDRs allowed to reach the public EKS API endpoint."
  type        = list(string)

  validation {
    condition     = length(var.public_access_cidrs) > 0 && !contains(var.public_access_cidrs, "0.0.0.0/0")
    error_message = "Provide at least one trusted CIDR; unrestricted 0.0.0.0/0 access is rejected."
  }
}

variable "node_instance_types" {
  description = "Managed node group instance types."
  type        = list(string)
  default     = ["t3.large"]
}

variable "node_min_size" {
  type    = number
  default = 2
}

variable "node_desired_size" {
  type    = number
  default = 2
}

variable "node_max_size" {
  type    = number
  default = 4
}

variable "single_nat_gateway" {
  description = "Use one NAT gateway for lower-cost non-production environments; false creates one per AZ."
  type        = bool
  default     = true
}

variable "tags" {
  description = "Additional tags applied to AWS resources."
  type        = map(string)
  default     = {}
}
