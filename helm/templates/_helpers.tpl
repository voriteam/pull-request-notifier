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

{{- define "pull-request-notifier.litestreamConfig" -}}
{{- $_ := required "litestream.replicaURL is required when litestream.enabled" .Values.litestream.replicaURL -}}
dbs:
  - path: {{ .Values.config.dbPath }}
    replica:
      url: {{ .Values.litestream.replicaURL }}
      sync-interval: {{ .Values.litestream.syncInterval }}
{{- end }}
