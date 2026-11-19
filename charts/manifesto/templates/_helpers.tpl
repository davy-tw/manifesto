{{/* Labels on every object. */}}
{{- define "manifesto.labels" -}}
app.kubernetes.io/name: manifesto
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" }}
{{- end }}

{{/* Labels of one component: (list $ "herald"). */}}
{{- define "manifesto.componentLabels" -}}
{{ include "manifesto.labels" (index . 0) }}
app.kubernetes.io/component: {{ index . 1 }}
{{- end }}

{{/* An image reference: (list $ .Values.press.image). */}}
{{- define "manifesto.image" -}}
{{- $root := index . 0 -}}
{{- $image := index . 1 -}}
{{- $repo := $image.repository -}}
{{- with $root.Values.imageRegistry }}{{ $repo = printf "%s/%s" (trimSuffix "/" .) $repo }}{{ end -}}
{{- printf "%s:%s" $repo (default $root.Chart.AppVersion $image.tag) -}}
{{- end }}

{{/* Pod settings shared by every component: (list $ .Values.herald). */}}
{{- define "manifesto.podScheduling" -}}
{{- $root := index . 0 -}}
{{- $c := index . 1 -}}
{{- with $root.Values.imagePullSecrets }}
imagePullSecrets:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- with $c.nodeSelector }}
nodeSelector:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- with $c.tolerations }}
tolerations:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- with $c.affinity }}
affinity:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- end }}

{{/* The container security context every component runs with. */}}
{{- define "manifesto.containerSecurity" -}}
allowPrivilegeEscalation: false
capabilities:
  drop: [ALL]
readOnlyRootFilesystem: true
{{- end }}

{{/*
manifesto.generated renders a file of generated objects (files/*.yaml),
moving namespaced objects and ServiceAccount subjects into the release
namespace and adding the chart's labels: (list $ "files/rbac.yaml" annotations).
*/}}
{{- define "manifesto.generated" -}}
{{- $root := index . 0 -}}
{{- $annotations := index . 2 -}}
{{- range $doc := splitList "\n---\n" ($root.Files.Get (index . 1)) }}
{{- $o := fromYaml $doc }}
{{- if $o.kind }}
{{- if $o.metadata.namespace }}{{ $_ := set $o.metadata "namespace" $root.Release.Namespace }}{{ end }}
{{- range $o.subjects }}{{ if .namespace }}{{ $_ := set . "namespace" $root.Release.Namespace }}{{ end }}{{ end }}
{{- $_ := set $o.metadata "labels" (merge (fromYaml (include "manifesto.labels" $root)) (default dict $o.metadata.labels)) }}
{{- with $annotations }}{{ $_ := set $o.metadata "annotations" (merge (deepCopy .) (default dict $o.metadata.annotations)) }}{{ end }}
---
{{ toYaml $o }}
{{- end }}
{{- end }}
{{- end }}
