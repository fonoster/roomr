{{- define "roomr.name" -}}{{ .Release.Name }}-roomr{{- end }}

{{- define "roomr.labels" -}}
app.kubernetes.io/name: roomr
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version }}
{{- end }}

{{- define "roomr.selector" -}}
app.kubernetes.io/name: roomr
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/* The port after the last colon of a host:port. */}}
{{- define "roomr.port" -}}{{ splitList ":" . | last | int }}{{- end }}

{{/* The rendered configuration. */}}
{{- define "roomr.config" -}}
{{- $cfg := deepCopy .Values.config }}
{{- if not $cfg.sessionEndpoint }}{{ fail "config.sessionEndpoint is required: the host:port of the app's Session server" }}{{- end }}
{{- $_ := set $cfg "drainWait" (printf "%ds" (.Values.drainGraceSeconds | int)) }}
{{- if and (not .Values.hostNetwork) .Values.advertisePodIP }}
{{- $control := $cfg.control | default dict }}
{{- $grpc := $control.grpc | default dict }}
{{- $_ := set $grpc "advertise" (printf "${POD_IP}:%s" (include "roomr.port" (default "0.0.0.0:50061" $grpc.listen))) }}
{{- $_ := set $control "grpc" $grpc }}
{{- $_ := set $cfg "control" $control }}
{{- end }}
{{- toYaml $cfg }}
{{- end }}
