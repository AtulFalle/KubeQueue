{{- define "kubequeue.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end }}

{{- define "kubequeue.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name (include "kubequeue.name" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end }}

{{- define "kubequeue.labels" -}}
app.kubernetes.io/name: {{ include "kubequeue.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{- define "kubequeue.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "kubequeue.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- required "serviceAccount.name is required when serviceAccount.create is false" .Values.serviceAccount.name -}}
{{- end -}}
{{- end }}

{{- define "kubequeue.clusterResourceName" -}}
{{- $namespaceHash := sha256sum .Release.Namespace | trunc 8 -}}
{{- printf "%s-%s-worker" (include "kubequeue.fullname" .) $namespaceHash | trunc 63 | trimSuffix "-" -}}
{{- end }}

{{- define "kubequeue.securitySecretName" -}}
{{- required "security.existingSecret is required" .Values.security.existingSecret -}}
{{- end }}

{{- define "kubequeue.securityReferenceChecksum" -}}
{{- printf "%s:%s:%s:%s:%s:%s:%s"
      .Values.security.existingSecret
      .Values.security.sessionDigestKey
      .Values.security.credentialEncryptionKey
      .Values.security.bffInternalKey
      .Values.security.bootstrapDigestKey
      .Values.security.bootstrapTokenKey
      .Values.security.serviceAccountDigestKey | sha256sum -}}
{{- end }}
