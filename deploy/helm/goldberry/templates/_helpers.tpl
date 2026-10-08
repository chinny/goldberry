{{- define "goldberry.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "goldberry.fullname" -}}
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

{{- define "goldberry.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" }}
{{ include "goldberry.selectorLabels" . }}
app.kubernetes.io/version: {{ .Values.image.tag | default .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{- define "goldberry.selectorLabels" -}}
app.kubernetes.io/name: {{ include "goldberry.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/* True when the database is SQLite (no external Postgres configured). */}}
{{- define "goldberry.sqlite" -}}
{{- if and (not .Values.postgres.external.url) (not .Values.postgres.external.existingSecret) }}true{{ end }}
{{- end }}

{{- define "goldberry.secretName" -}}
{{- .Values.secretKey.existingSecret | default (include "goldberry.fullname" .) }}
{{- end }}
