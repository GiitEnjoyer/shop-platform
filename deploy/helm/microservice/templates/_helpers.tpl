{{- define "ms.name" -}}
{{- default .Release.Name .Values.nameOverride | trunc 50 | trimSuffix "-" -}}
{{- end -}}

{{- define "ms.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version }}
app.kubernetes.io/name: {{ include "ms.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: shop
{{- end -}}

{{- define "ms.selector" -}}
app.kubernetes.io/name: {{ include "ms.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{/*
Variants to render as Deployments. Each item:
  suffix   - appended to the Deployment name
  track    - stable | canary   (canary mode)
  color    - blue | green      (blue-green mode)
  tag      - image tag
  replicas - replica count (omitted when HPA manages the variant, except in
             blue-green mode: there it is always explicit so a color switch
             never drops capacity, see scripts/blue-green.sh)
  hpa      - whether an HPA targets this variant
  env      - extra env vars
*/}}
{{- define "ms.variants" -}}
{{- $v := list -}}
{{- if .Values.blueGreen.enabled -}}
  {{- range $color := list "blue" "green" -}}
    {{- $cfg := index $.Values.blueGreen $color -}}
    {{- if $cfg.tag -}}
      {{- $active := eq $color $.Values.blueGreen.activeColor -}}
      {{- $activeReplicas := $.Values.blueGreen.activeReplicas | default $.Values.replicaCount -}}
      {{- $v = append $v (dict "suffix" (printf "-%s" $color) "track" "stable" "color" $color "tag" $cfg.tag "replicas" (ternary $activeReplicas $.Values.blueGreen.idleReplicas $active) "hpa" $active "env" dict) -}}
    {{- end -}}
  {{- end -}}
{{- else -}}
  {{- $v = append $v (dict "suffix" "" "track" "stable" "color" "" "tag" (required "image.tag is required" .Values.image.tag) "replicas" .Values.replicaCount "hpa" .Values.autoscaling.enabled "env" dict) -}}
  {{- if .Values.canary.enabled -}}
    {{- $v = append $v (dict "suffix" "-canary" "track" "canary" "color" "" "tag" (required "canary.tag is required when canary.enabled" .Values.canary.tag) "replicas" .Values.canary.replicas "hpa" false "env" .Values.canary.env) -}}
  {{- end -}}
{{- end -}}
{{- toJson $v -}}
{{- end -}}

{{- define "ms.vaultAnnotations" -}}
{{- if .Values.vault.enabled }}
vault.hashicorp.com/agent-inject: "true"
vault.hashicorp.com/role: {{ required "vault.role is required" .Values.vault.role | quote }}
vault.hashicorp.com/agent-requests-cpu: "10m"
vault.hashicorp.com/agent-requests-mem: "32Mi"
{{- range $file, $s := .Values.vault.secrets }}
vault.hashicorp.com/agent-inject-secret-{{ $file }}: {{ $s.path | quote }}
vault.hashicorp.com/agent-inject-template-{{ $file }}: |
{{ $s.template | indent 2 }}
{{- end }}
{{- end }}
{{- end -}}
