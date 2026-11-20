{{- define "notely.fullname" -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "notely.selectorLabels" -}}
app.kubernetes.io/name: notely
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "notely.labels" -}}
{{ include "notely.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version }}
{{- end -}}

{{- define "notely.image" -}}
{{- $tag := .Values.image.tag | default .Chart.AppVersion -}}
{{- if .Values.image.digest -}}
{{- printf "%s:%s@%s" .Values.image.repository $tag .Values.image.digest -}}
{{- else -}}
{{- printf "%s:%s" .Values.image.repository $tag -}}
{{- end -}}
{{- end -}}

{{/* Service DNS names, the names in the backend certificate. */}}
{{- define "notely.serviceHost" -}}
{{- printf "%s.%s.svc" (include "notely.fullname" .) .Release.Namespace -}}
{{- end -}}
