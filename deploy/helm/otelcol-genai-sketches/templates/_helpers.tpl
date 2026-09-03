{{/* SPDX-License-Identifier: Apache-2.0 */}}
{{/* Code authors: Vijay Erramilli and Codex */}}
{{- define "otelcol-genai-sketches.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "otelcol-genai-sketches.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name (include "otelcol-genai-sketches.name" .) | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}

{{- define "otelcol-genai-sketches.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | quote }}
app.kubernetes.io/name: {{ include "otelcol-genai-sketches.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{- define "otelcol-genai-sketches.selectorLabels" -}}
app.kubernetes.io/name: {{ include "otelcol-genai-sketches.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "otelcol-genai-sketches.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "otelcol-genai-sketches.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{- define "otelcol-genai-sketches.image" -}}
{{- if .Values.image.digest }}
{{- printf "%s@%s" .Values.image.repository .Values.image.digest }}
{{- else }}
{{- printf "%s:%s" .Values.image.repository (default .Chart.AppVersion .Values.image.tag) }}
{{- end }}
{{- end }}
