{{/*
Expand the name of the chart.
*/}}
{{- define "lightsout.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "lightsout.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- printf "%s" $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "lightsout.labels" -}}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version | replace "+" "_" }}
app.kubernetes.io/name: {{ include "lightsout.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "lightsout.selectorLabels" -}}
app.kubernetes.io/name: {{ include "lightsout.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "lightsout.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "lightsout.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Webhook certificate secret name
*/}}
{{- define "lightsout.webhookCertSecret" -}}
{{- printf "%s-webhook-cert" (include "lightsout.fullname" .) }}
{{- end }}

{{/*
Webhook service name
*/}}
{{- define "lightsout.webhookServiceName" -}}
{{- printf "%s-webhook" (include "lightsout.fullname" .) }}
{{- end }}
