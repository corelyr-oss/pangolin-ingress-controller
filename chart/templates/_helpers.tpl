{{/*
Expand the name of the chart.
*/}}
{{- define "pangolin-ingress-controller.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "pangolin-ingress-controller.fullname" -}}
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

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "pangolin-ingress-controller.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "pangolin-ingress-controller.labels" -}}
helm.sh/chart: {{ include "pangolin-ingress-controller.chart" . }}
{{ include "pangolin-ingress-controller.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "pangolin-ingress-controller.selectorLabels" -}}
app.kubernetes.io/name: {{ include "pangolin-ingress-controller.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app: pangolin-ingress-controller
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "pangolin-ingress-controller.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "pangolin-ingress-controller.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Get the namespace for the API key secret
*/}}
{{- define "pangolin-ingress-controller.apiKeyNamespace" -}}
{{- if .Values.pangolin.apiKeyNamespace }}
{{- .Values.pangolin.apiKeyNamespace }}
{{- else }}
{{- .Release.Namespace }}
{{- end }}
{{- end }}
