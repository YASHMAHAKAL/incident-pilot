{{- define "incidentpilot.labels" -}}
app.kubernetes.io/part-of: incidentpilot
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{- define "incidentpilot.securityContext" -}}
allowPrivilegeEscalation: false
readOnlyRootFilesystem: true
capabilities:
  drop: ["ALL"]
runAsNonRoot: true
runAsUser: 65532
{{- end }}
