package policies

import _ "embed"

const RemediationVersion = "phase10-v1"

// Remediation is compiled once by the trusted in-process policy evaluator.
//
//go:embed remediation.rego
var Remediation string
