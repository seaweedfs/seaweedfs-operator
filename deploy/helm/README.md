# seaweedfs-operator

![Version: 0.1.35](https://img.shields.io/badge/Version-0.1.35-informational?style=flat-square) ![Type: application](https://img.shields.io/badge/Type-application-informational?style=flat-square) ![AppVersion: 1.0.32](https://img.shields.io/badge/AppVersion-1.0.32-informational?style=flat-square)

A Helm chart for the seaweedfs-operator

## Maintainers

| Name | Email | Url |
| ---- | ------ | --- |
| chrislusf |  | <https://github.com/chrislusf> |

## Values

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| affinity | object | `{}` |  |
| bucketUsage | object | `{"refreshInterval":""}` | Bucket usage stats refresh configuration. The operator periodically calls collection.list per Seaweed cluster and patches status.usage on each Bucket. Leave refreshInterval empty to use the in-binary default (5m). Set to "0s" to disable the loop entirely. |
| commonAnnotations | object | `{}` | Annotations for all the deployed objects |
| commonLabels | object | `{}` | Labels for all the deployed objects |
| crds.create | bool | `true` | Install the Seaweed CRD as part of the release. Set to false when the CRD is managed out-of-band (e.g., cluster-scoped GitOps or a shared CRD across namespaces) so `helm install/upgrade` won't try to create or adopt it. |
| fullnameOverride | string | `""` | String to fully override common.names.fullname template |
| global | object | `{"imageRegistry":""}` | Global Docker image parameters. global.imageRegistry, when set, overrides the registry of every image in the chart; leave empty to use each image's own. |
| grafanaDashboard.enabled | bool | `true` | Enable or disable Grafana Dashboard configmap |
| image.pullPolicy | string | `"IfNotPresent"` | Specify a imagePullPolicy # Defaults to 'Always' if image tag is 'latest', else set to 'IfNotPresent' # ref: http://kubernetes.io/docs/user-guide/images/#pre-pulling-images |
| image.registry | string | `"chrislusf"` |  |
| image.repository | string | `"seaweedfs-operator"` |  |
| image.tag | string | `""` | tag of image to use. Defaults to appVersion in Chart.yaml |
| nameOverride | string | `""` | String to partially override common.names.fullname template (will maintain the release name) |
| nodeSelector | object | `{}` |  |
| podSecurityContext.fsGroup | int | `65532` |  |
| podSecurityContext.runAsNonRoot | bool | `true` |  |
| podSecurityContext.runAsUser | int | `65532` |  |
| port.name | string | `"http"` | name of the container port to use for the Kubernete service and ingress |
| port.number | int | `8080` | container port number to use for the Kubernete service and ingress |
| rbac.serviceAccount.annotations | object | `{}` | Annotations to add to the service account |
| rbac.serviceAccount.automount | bool | `true` | Automount service account token for the server service account |
| rbac.serviceAccount.create | bool | `true` | Specifies whether a service account should be created |
| rbac.serviceAccount.name | string | `""` | The name of the service account to use. If not set and create is true, a name is generated using the fullname template If set to "default", no ServiceAccount will be created and the default one will be used |
| replicaCount | int | `1` | Set number of pod replicas |
| resources.limits.cpu | string | `"500m"` | seaweedfs-operator containers' cpu limit (maximum allowed CPU) |
| resources.limits.memory | string | `"500Mi"` | seaweedfs-operator containers' memory limit (maximum allowed memory) |
| resources.requests.cpu | string | `"100m"` | seaweedfs-operator containers' cpu request (how much is requested by default) |
| resources.requests.memory | string | `"50Mi"` | seaweedfs-operator containers' memory request (how much is requested by default) |
| securityContext.allowPrivilegeEscalation | bool | `false` |  |
| securityContext.capabilities.drop[0] | string | `"ALL"` |  |
| securityContext.readOnlyRootFilesystem | bool | `true` |  |
| securityContext.runAsNonRoot | bool | `true` |  |
| service.port | int | `8080` | port to use for Kubernetes service |
| service.portName | string | `"http"` | name of the port to use for Kubernetes service |
| serviceMonitor.additionalLabels | object | `{}` | Used to pass Labels that are used by the Prometheus installed in your cluster to select Service Monitors to work with |
| serviceMonitor.enabled | bool | `false` | Enable or disable ServiceMonitor for prometheus metrics |
| serviceMonitor.honorLabels | bool | `true` | Specify honorLabels parameter to add the scrape endpoint |
| serviceMonitor.interval | string | `"10s"` | Specify the interval at which metrics should be scraped |
| serviceMonitor.scrapeTimeout | string | `"10s"` | Specify the timeout after which the scrape is ended |
| tolerations | list | `[]` |  |
| webhook.certManager.enabled | bool | `false` | Use cert-manager to provision the webhook TLS certificate instead of the built-in certgen Jobs. Requires cert-manager to be installed in the cluster. When enabled the certgen Jobs and their RBAC are not created. |
| webhook.certManager.issuerRef | object | `{}` | Point the webhook serving Certificate at an existing Issuer or ClusterIssuer instead of the self-signed Issuer created by default. The issuer must populate ca.crt in the issued secret so cert-manager can inject the CA into the webhook configurations; CA, Vault, and self-signed issuers do, ACME issuers such as Let's Encrypt do not. |
| webhook.certgen.registry | string | `"registry.k8s.io"` |  |
| webhook.certgen.repository | string | `"ingress-nginx/kube-webhook-certgen"` |  |
| webhook.certgen.tag | string | `"v20231011-8b53cabe0"` |  |
| webhook.enabled | bool | `true` | Enable or disable webhooks |
| webhook.initContainer | object | `{"image":"curlimages/curl:8.8.0"}` | Configuration for webhook certificate jobs |
| webhook.initContainer.image | string | `"curlimages/curl:8.8.0"` | Image for webhook readiness check init container |
| webhook.podSecurityContext | object | `{"fsGroup":65532,"runAsNonRoot":true,"runAsUser":65532,"seccompProfile":{"type":"RuntimeDefault"}}` | Pod security context for webhook jobs ref: https://kubernetes.io/docs/tasks/configure-pod-container/security-context/ |
| webhook.securityContext | object | `{"allowPrivilegeEscalation":false,"capabilities":{"drop":["ALL"]},"readOnlyRootFilesystem":true,"runAsNonRoot":true}` | Container security context for webhook jobs |
