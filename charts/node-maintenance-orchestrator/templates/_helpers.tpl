{{- define "nmo.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "nmo.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{- define "nmo.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "nmo.labels" -}}
helm.sh/chart: {{ include "nmo.chart" . }}
{{ include "nmo.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{- define "nmo.selectorLabels" -}}
control-plane: controller-manager
app.kubernetes.io/name: {{ include "nmo.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "nmo.serviceAccountName" -}}
{{- if .Values.serviceAccount.name }}
{{- .Values.serviceAccount.name }}
{{- else }}
{{- include "nmo.fullname" . }}
{{- end }}
{{- end }}

{{- define "nmo.webhookServiceName" -}}
{{- printf "%s-webhook-service" (include "nmo.fullname" .) }}
{{- end }}

{{- define "nmo.webhookConfigName" -}}
{{- printf "%s-validating-webhook-configuration" (include "nmo.fullname" .) }}
{{- end }}

{{- define "nmo.tlsCertSecretName" -}}
{{- printf "%s-tls-cert" (include "nmo.fullname" .) }}
{{- end }}

{{- define "nmo.metricsServiceName" -}}
{{- printf "%s-metrics" (include "nmo.fullname" .) }}
{{- end }}
