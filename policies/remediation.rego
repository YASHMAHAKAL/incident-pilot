package incidentpilot.remediation

import rego.v1

deny contains "environment_not_development" if input.environment != "development"
deny contains "repository_not_allowed" if not input.repository_allowed
deny contains "path_not_allowed" if not input.path_allowed
deny contains "protected_namespace" if input.protected_namespace
deny contains "secret_target" if input.secret_target
deny contains "direct_cluster_mutation" if input.direct_mutation
deny contains "destructive_operation" if input.destructive
deny contains "evidence_not_verified" if not input.evidence_verified
deny contains "unsupported_root_cause" if input.root_cause != "memory_limit_oom"
deny contains "unsupported_operation" if input.operation != "update_resource_limit"
deny contains "unsupported_target" if {
    input.target_kind != "Deployment"
}
deny contains "unsupported_target" if {
    input.target_name != "payments-api"
}
deny contains "unsupported_target" if {
    input.container != "payments-api"
}
deny contains "unsupported_field" if input.field != "memory_limit"
deny contains "unsafe_resource_change" if not input.resource_change_safe

default allow := false

allow if count(deny) == 0

decision := {
    "allow": allow,
    "reasons": sort(deny),
}
