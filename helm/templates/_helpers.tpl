{{- define "pull-request-notifier.name" -}}
{{- .Values.nameOverride | default .Chart.Name }}
{{- end }}

{{- define "pull-request-notifier.containerName" -}}
{{- .Values.containerName | default .Chart.Name }}
{{- end }}

{{- define "pull-request-notifier.serviceAccountName" -}}
{{- if .Values.serviceAccount.name }}
{{- .Values.serviceAccount.name }}
{{- else }}
{{- .Release.Name }}
{{- end }}
{{- end }}
