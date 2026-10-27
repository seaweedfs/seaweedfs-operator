{{/*
Expand the name of the chart.
*/}}
{{- define "seaweedfs-operator.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "seaweedfs-operator.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "seaweedfs-operator.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "seaweedfs-operator.labels" -}}
helm.sh/chart: {{ include "seaweedfs-operator.chart" . }}
{{ include "seaweedfs-operator.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "seaweedfs-operator.selectorLabels" -}}
app.kubernetes.io/name: {{ include "seaweedfs-operator.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Resolve a Docker image reference. global.imageRegistry, when set, overrides the
image's own registry (chart-wide mirror, e.g. air-gapped).
Usage:
  include "seaweedfs-operator.image" (dict "imageRoot" .Values.image "tag" "1.2.3" "global" .Values.global)
*/}}
{{- define "seaweedfs-operator.image" -}}
{{- $registry := .imageRoot.registry -}}
{{- if .global -}}
{{- $registry = default .imageRoot.registry .global.imageRegistry -}}
{{- end -}}
{{- if $registry -}}
{{- printf "%s/%s:%s" $registry .imageRoot.repository .tag -}}
{{- else -}}
{{- printf "%s:%s" .imageRoot.repository .tag -}}
{{- end -}}
{{- end -}}

{{/*
Docker registry image pull secret
*/}}
{{- define "seaweedfs-operator.imagePullSecret" }}
{{- $auth := printf "%s:%s" .username .password | b64enc -}}
{{- printf "{\"auths\": {\"%s\": {\"auth\": \"%s\"}}}" .registry $auth | b64enc }}
{{- end }}

{{- define "seaweedfs-operator.createPullSecret" -}}
{{- if and .Values.image.credentials (not .Values.image.pullSecrets) }}
  {{- true -}}
{{- else -}}
{{- end -}}
{{- end -}}

{{- define "seaweedfs-operator.pullSecretName" -}}
{{- if .Values.image.pullSecrets -}}
  {{- printf "%s" (tpl .Values.image.pullSecrets .) -}}
{{- else -}}
  {{- printf "%s-container-registry" (include "seaweedfs-operator.fullname" .) -}}
{{- end -}}
{{- end -}}

{{/*
Create the name of the service account to use
*/}}
{{- define "seaweedfs-operator.serviceAccountName" -}}
{{- if .Values.rbac.serviceAccount.create -}}
  {{- default (include "seaweedfs-operator.fullname" .) .Values.rbac.serviceAccount.name -}}
{{- else -}}
  {{- default "default" .Values.rbac.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{/*
Mutating webhook path
*/}}
{{- define "seaweedfs-operator.mutatingWebhookPath" -}}/mutate-seaweed-seaweedfs-com-v1-seaweed{{- end -}}

{{/*
Validating webhook path
*/}}
{{- define "seaweedfs-operator.validatingWebhookPath" -}}/validate-seaweed-seaweedfs-com-v1-seaweed{{- end -}}

{{/*
Webhook Pod Security Context
*/}}
{{- define "seaweedfs-operator.webhookPodSecurityContext" -}}
{{- with .Values.webhook.podSecurityContext }}
securityContext:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- end -}}

{{/*
Webhook Container Security Context
*/}}
{{- define "seaweedfs-operator.webhookContainerSecurityContext" -}}
{{- with .Values.webhook.securityContext }}
securityContext:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- end -}}

{{/*
Pod scheduling constraints (nodeSelector, affinity, tolerations) shared by
the operator Deployment and the helm-managed webhook certificate Jobs so a
single set of values controls where every operator-produced pod runs.
*/}}
{{- define "seaweedfs-operator.scheduling" -}}
{{- with .Values.nodeSelector }}
nodeSelector:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- with .Values.affinity }}
affinity:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- with .Values.tolerations }}
tolerations:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- end -}}

{{/*
Webhook init container for waiting until webhook service is ready
*/}}
{{- define "seaweedfs-operator.webhookWaitInitContainer" -}}
- name: wait-for-webhook
  image: {{ .Values.webhook.initContainer.image }}
  {{- include "seaweedfs-operator.webhookContainerSecurityContext" . | nindent 2 }}
  command: ['sh', '-c', 'set -e; until curl -sk --fail --head --max-time 5 https://{{ include "seaweedfs-operator.fullname" . }}-webhook.{{ .Release.Namespace }}.svc:443{{ .webhookPath }} >/dev/null; do echo waiting for webhook; sleep 1; done;']
{{- end -}}

{{/*
Mutating webhook configuration name
*/}}
{{- define "seaweedfs-operator.mutatingWebhookName" -}}
{{- $suffix := "-mutating-webhook-configuration" -}}
{{- printf "%s%s" (include "seaweedfs-operator.fullname" . | trunc (int (sub 63 (len $suffix))) | trimSuffix "-") $suffix -}}
{{- end -}}

{{/*
Validating webhook configuration name
*/}}
{{- define "seaweedfs-operator.validatingWebhookName" -}}
{{- $suffix := "-validating-webhook-configuration" -}}
{{- printf "%s%s" (include "seaweedfs-operator.fullname" . | trunc (int (sub 63 (len $suffix))) | trimSuffix "-") $suffix -}}
{{- end -}}

{{/*
cert-manager resource names
*/}}
{{- define "seaweedfs-operator.webhookIssuerName" -}}
{{- printf "%s-webhook-selfsigned-issuer" (include "seaweedfs-operator.fullname" .) -}}
{{- end -}}

{{- define "seaweedfs-operator.webhookCertificateName" -}}
{{- printf "%s-webhook-cert" (include "seaweedfs-operator.fullname" .) -}}
{{- end -}}

{{- define "seaweedfs-operator.webhookCertificateSecretName" -}}
{{- printf "%s-webhook-server-cert" (include "seaweedfs-operator.fullname" .) -}}
{{- end -}}

{{/*
Namespaced rules of the manager role — everything the controllers need
inside watched namespaces. Rendered into a per-namespace Role when
watchNamespaces is set, or into the cluster-wide ClusterRole otherwise.
The
leader-election lease has its own Role in the release namespace.
*/}}
{{- define "seaweedfs-operator.managerNamespacedRules" -}}
- apiGroups:
  - ""
  resources:
  - configmaps
  - secrets
  - services
  verbs:
  - create
  - delete
  - get
  - list
  - patch
  - update
  - watch
- apiGroups:
  - ""
  resources:
  - events
  verbs:
  - create
  - patch
- apiGroups:
  - ""
  resources:
  - pods
  verbs:
  - get
  - list
  - watch
- apiGroups:
  - apps
  resources:
  - daemonsets
  - deployments
  - statefulsets
  verbs:
  - create
  - delete
  - get
  - list
  - patch
  - update
  - watch
- apiGroups:
  - batch
  resources:
  - cronjobs
  - jobs
  verbs:
  - create
  - delete
  - get
  - list
  - patch
  - update
  - watch
- apiGroups:
  - cert-manager.io
  resources:
  - certificates
  - issuers
  verbs:
  - create
  - delete
  - get
  - list
  - patch
  - update
  - watch
- apiGroups:
  - extensions
  - networking.k8s.io
  resources:
  - ingresses
  verbs:
  - create
  - delete
  - get
  - list
  - patch
  - update
  - watch
- apiGroups:
  - monitoring.coreos.com
  resources:
  - servicemonitors
  verbs:
  - create
  - delete
  - get
  - list
  - patch
  - update
  - watch
- apiGroups:
  - seaweed.seaweedfs.com
  resources:
  - adminscripts
  - bucketlifecyclepolicies
  - bucketnotifications
  - buckets
  - s3credentials
  - s3identities
  - s3oidcproviders
  - s3policies
  - s3policybindings
  - s3roles
  - seaweedbackups
  - seaweedrestores
  - seaweeds
  verbs:
  - create
  - delete
  - get
  - list
  - patch
  - update
  - watch
- apiGroups:
  - seaweed.seaweedfs.com
  resources:
  - adminscripts/finalizers
  - bucketlifecyclepolicies/finalizers
  - bucketnotifications/finalizers
  - buckets/finalizers
  - s3credentials/finalizers
  - s3identities/finalizers
  - s3oidcproviders/finalizers
  - s3policies/finalizers
  - s3policybindings/finalizers
  - s3roles/finalizers
  - seaweedbackups/finalizers
  - seaweedrestores/finalizers
  - seaweeds/finalizers
  verbs:
  - update
- apiGroups:
  - seaweed.seaweedfs.com
  resources:
  - adminscripts/status
  - bucketlifecyclepolicies/status
  - bucketnotifications/status
  - buckets/status
  - s3credentials/status
  - s3identities/status
  - s3oidcproviders/status
  - s3policies/status
  - s3policybindings/status
  - s3roles/status
  - seaweedbackups/status
  - seaweedrestores/status
  - seaweeds/status
  verbs:
  - get
  - patch
  - update
- apiGroups:
  - seaweed.seaweedfs.com
  resources:
  - resourcereferencegrants
  verbs:
  - get
  - list
  - watch
- apiGroups:
  - ""
  resources:
  - persistentvolumeclaims
  verbs:
  - get
  - list
  - patch
  - update
  - watch
{{- end }}

{{/*
Cluster-scoped rules of the manager role — the only grants that must be
cluster-wide when the operator is restricted to watchNamespaces, plus
the rules only the SeaweedCSIDriver controller needs (the driver deploys
cluster-global objects, so enabling it keeps its grants cluster-wide).
*/}}
{{- define "seaweedfs-operator.managerClusterRules" -}}
- apiGroups:
  - ""
  resources:
  - namespaces
  verbs:
  - get
  - list
  - watch
- apiGroups:
  - storage.k8s.io
  resources:
  - storageclasses
  verbs:
  - get
  - list
  - watch
{{- if .Values.csiDriver }}
{{- if .Values.csiDriver.enabled }}
# Rules only the SeaweedCSIDriver controller needs, from the kubebuilder
# markers in internal/controller/seaweedcsidriver_controller.go. The manager
# registers that controller only when csiDriver.enabled sets
# ENABLE_CSI_DRIVER=true, so the same value gates the grants: a default
# install cannot manage CSIDrivers, cluster-wide RBAC (bind/escalate), nodes,
# or PersistentVolumes. test/helm/rbac_drift_test.go checks both renders
# against the markers.
- apiGroups:
  - ""
  resources:
  - events
  - nodes
  - persistentvolumeclaims
  - persistentvolumes
  - pods
  - serviceaccounts
  verbs:
  - create
  - delete
  - get
  - list
  - patch
  - update
  - watch
- apiGroups:
  - coordination.k8s.io
  resources:
  - leases
  verbs:
  - create
  - delete
  - get
  - list
  - patch
  - update
  - watch
- apiGroups:
  - rbac.authorization.k8s.io
  resources:
  - clusterrolebindings
  - clusterroles
  - rolebindings
  - roles
  verbs:
  - bind
  - create
  - delete
  - escalate
  - get
  - list
  - patch
  - update
  - watch
- apiGroups:
  - seaweed.seaweedfs.com
  resources:
  - seaweedcsidrivers
  verbs:
  - create
  - delete
  - get
  - list
  - patch
  - update
  - watch
- apiGroups:
  - seaweed.seaweedfs.com
  resources:
  - seaweedcsidrivers/finalizers
  verbs:
  - update
- apiGroups:
  - seaweed.seaweedfs.com
  resources:
  - seaweedcsidrivers/status
  verbs:
  - get
  - patch
  - update
- apiGroups:
  - snapshot.storage.k8s.io
  resources:
  - volumesnapshotcontents
  - volumesnapshots
  verbs:
  - get
  - list
- apiGroups:
  - storage.k8s.io
  resources:
  - csidrivers
  - storageclasses
  verbs:
  - create
  - delete
  - get
  - list
  - patch
  - update
  - watch
- apiGroups:
  - storage.k8s.io
  resources:
  - csinodes
  - volumeattachments
  verbs:
  - get
  - list
  - patch
  - update
  - watch
{{- end }}
{{- end }}
{{- end }}
