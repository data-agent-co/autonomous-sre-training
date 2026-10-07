{{/* Helpers for the watcher chart. The helm-test helpers are in tests/_helpers.tpl. */}}

{{- define "k8s-watcher.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "k8s-watcher.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "k8s-watcher.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "k8s-watcher.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{- define "k8s-watcher.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "k8s-watcher.labels" -}}
helm.sh/chart: {{ include "k8s-watcher.chart" . }}
{{ include "k8s-watcher.selectorLabels" . }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/component: watcher
{{- end -}}

{{- define "k8s-watcher.selectorLabels" -}}
app.kubernetes.io/name: {{ include "k8s-watcher.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "k8s-watcher.imageRef" -}}
{{- $tag := default .Chart.AppVersion .Values.image.tag -}}
{{- printf "%s:%s" .Values.image.repository $tag -}}
{{- end -}}

{{/*
The port the container's probes use: the EV lane's when it runs, else the CM
lane's. With both lanes on, readiness therefore follows the EV lane only.
*/}}
{{- define "k8s-watcher.probePort" -}}
{{- if .Values.ev.enabled -}}ev-http{{- else -}}cm-http{{- end -}}
{{- end -}}
