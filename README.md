
[![Build Status](https://travis-ci.com/seaweedfs/seaweedfs-operator.svg?branch=master)](https://travis-ci.com/github/seaweedfs/seaweedfs-operator)

# SeaweedFS Operator

This [Kubernetes Operator](https://kubernetes.io/docs/concepts/extend-kubernetes/operator/) is made to easily deploy SeaweedFS onto your Kubernetes cluster.

The operator manages the complete SeaweedFS infrastructure on Kubernetes, including Master servers, Volume servers, and Filer services with S3-compatible API and embedded IAM (Identity and Access Management). This provides a scalable, resilient distributed file system with built-in authentication.

The difference to [seaweedfs-csi-driver](https://github.com/seaweedfs/seaweedfs-csi-driver) is that the infrastructure (SeaweedFS) itself runs on Kubernetes as well (Master, Filer, Volume-Servers) and can as such easily scale with it as you need. It is also by far more resilent to failures then a simple systemD service in regards to handling crashing services or accidental deletes.

By using `make deploy` it will deploy a Resource of type 'Seaweed' onto your current kubectl $KUBECONFIG target (the operator itself) which by default will do nothing unless you configurate it (see examples in config/samples/).

Goals:

- [x] Automatically deploy and manage a SeaweedFS cluster
- [x] Ability to be managed by other Operators
- [x] Compatibility with [seaweedfs-csi-driver](https://github.com/seaweedfs/seaweedfs-csi-driver) (deploy it via the `SeaweedCSIDriver` CR — see [CSI_SUPPORT.md](CSI_SUPPORT.md))
- [x] Auto rolling upgrade and restart
- [x] Ingress for volume server, filer and S3, to support HDFS, REST filer, S3 API and cross-cluster replication
- [x] IAM (Identity and Access Management) service support for S3 API authentication and authorization
- [ ] Support all major cloud Kubernetes: AWS, Google, Azure
- [x] Scheduled backup and restore: filer metadata snapshots + continuous data mirror to S3, Google Cloud Storage, Azure, Backblaze B2, or a PVC (see [BACKUP_SUPPORT.md](BACKUP_SUPPORT.md))
- [ ] Put warm data to cloud storage tier: S3, Google Cloud Storage , Azure
- [x] Grafana dashboard

## Installation

### Helm

```bash
helm repo add seaweedfs-operator https://seaweedfs.github.io/seaweedfs-operator/
helm template seaweedfs-operator seaweedfs-operator/seaweedfs-operator
```

> **Note**: For versions prior to 0.1.2, the legacy repository URL `https://seaweedfs.github.io/seaweedfs-operator/helm` can still be used, but new releases will only be published to the main repository URL above.

#### Upgrading from chart versions <= 0.1.14

Starting in chart version 0.1.15, the `seaweeds.seaweed.seaweedfs.com` CRD is shipped as a templated resource instead of living in `crds/`. This lets `helm upgrade` actually update it — the `crds/` directory is install-only in Helm 3.

If you already have the chart installed, run these once before your next `helm upgrade` so Helm can take over the existing CRD. Look up your release name and namespace first — they must match exactly, or Helm will still refuse to adopt the CRD:

```bash
helm list -A | grep seaweedfs-operator
# Replace the two values below with the NAME and NAMESPACE you see above.
RELEASE=<release-name>
NAMESPACE=<release-namespace>
kubectl label crd seaweeds.seaweed.seaweedfs.com app.kubernetes.io/managed-by=Helm --overwrite
kubectl annotate crd seaweeds.seaweed.seaweedfs.com \
  meta.helm.sh/release-name=$RELEASE \
  meta.helm.sh/release-namespace=$NAMESPACE --overwrite
```

The CRD is annotated with `helm.sh/resource-policy: keep`, so `helm uninstall` will leave it and your `Seaweed` resources in place.

If the CRD is managed outside of this chart (e.g., installed cluster-wide via GitOps), set `--set crds.create=false` on install/upgrade so Helm does not try to own it. Note: `helm --skip-crds` has no effect here because the CRD lives in `templates/`, not `crds/`.

### FluxCD

Add the following files to a new directory called `seaweedfs-operator` under your FluxCD GitRepository (publishing) directory.

kustomization.yaml
```yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - seaweedfs-operator-namespace.yaml
  - seaweedfs-operator-helmrepository.yaml
  - seaweedfs-operator-helmrelease.yaml
```

seaweedfs-operator-namespace.yaml
```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: seaweedfs-operator
```

seaweedfs-operator-helmrepository.yaml
```yaml
apiVersion: source.toolkit.fluxcd.io/v1
kind: HelmRepository
metadata:
  name: seaweedfs-operator
  namespace: seaweedfs-operator
spec:
  interval: 1h
  url: https://seaweedfs.github.io/seaweedfs-operator/
```

seaweedfs-operator-helmrelease.yaml
```yaml
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: seaweedfs-operator
  namespace: seaweedfs-operator
spec:
  interval: 1h
  chart:
    spec:
      chart: seaweedfs-operator
      sourceRef:
        kind: HelmRepository
        name: seaweedfs-operator
        namespace: seaweedfs-operator
```

The webhook is enabled by default. Its server certificate is generated by a
`pre-install,pre-upgrade` Helm hook job, so the `seaweedfs-operator-webhook-server-cert`
secret exists before the operator deployment starts — no manual two-step enable is needed.

### Manual

This operator uses `kustomize` for deployment. Please [install kustomize](https://kubectl.docs.kubernetes.io/installation/kustomize/) if you do not have it.

By default, the defaulting and validation webhooks are disabled, so `make deploy` works on any cluster without `cert-manager`. We strongly recommend enabling the webhooks for production use.

First clone the repository:

```bash
git clone https://github.com/seaweedfs/seaweedfs-operator --depth=1
```

To deploy the operator with webhooks enabled, make sure you have installed the `cert-manager`(Installation docs: <https://cert-manager.io/docs/installation/>) in your cluster, then follow the instructions in the `config/default/kustomization.yaml` file to uncomment all the `[WEBHOOK]` and `[CERTMANAGER]` sections (including the one in `config/crd/kustomization.yaml`).
Uncommenting those sections also flips `ENABLE_WEBHOOKS` to `"true"` for you via `config/default/manager_webhook_patch.yaml`, so no separate edit of `config/manager/manager.yaml` is needed.

Manager image must be locally built and published into a registry accessible from your k8s cluster:

```bash
export IMG=<registry/image:tag>

# Build and push for amd64
export TARGETARCH=amd64

# Optional if you want to change TARGETOS
# export TARGETOS=linux

make docker-build

# Build and push for arm64
export TARGETARCH=arm64
make docker-build
```

Afterwards fire up to install CRDs:

```bash
make install
```

Then run the command to deploy the operator into your cluster using Kustomize or Helm:

```bash
# if using Kustomize
make deploy
# if using Helm
helm install seaweedfs-operator ./deploy/helm
```

Verify it was correctly deployed:

```bash
kubectl get pods --all-namespaces
```

Which may return:

```bash
NAMESPACE                   NAME                                                     READY   STATUS    RESTARTS   AGE
kube-system                 coredns-f9fd979d6-68p4c                                  1/1     Running   0          34m
kube-system                 coredns-f9fd979d6-x992t                                  1/1     Running   0          34m
kube-system                 etcd-kind-control-plane                                  1/1     Running   0          34m
kube-system                 kindnet-rp7wr                                            1/1     Running   0          34m
kube-system                 kube-apiserver-kind-control-plane                        1/1     Running   0          34m
kube-system                 kube-controller-manager-kind-control-plane               1/1     Running   0          34m
kube-system                 kube-proxy-dqfg2                                         1/1     Running   0          34m
kube-system                 kube-scheduler-kind-control-plane                        1/1     Running   0          34m
local-path-storage          local-path-provisioner-78776bfc44-7zvxx                  1/1     Running   0          34m
seaweedfs-operator-system   seaweedfs-operator-controller-manager-54cc768f4c-cwz2k   1/1     Running   0          34m
```

See the next section for example usage - **at this point you only deployed the Operator itself!**

### You need to also deploy a configuration to get it running (see next section)!

## Configuration Examples

### Basic SeaweedFS Deployment

For detailed configuration options and examples, see the sample configurations in the `config/samples/` directory. For a line-by-line walkthrough of a full cluster, start with `config/samples/seaweed_v1_seaweed_annotated.yaml`.

#### Key fields explained

A `Seaweed` cluster is built from three core components, each run as its own
StatefulSet. Every `replicas` value becomes that many Pods:

- **`master`** — coordinates the cluster and assigns volumes. Use 3 replicas for HA, 1 for dev.
- **`volume`** — stores the file data on disk. Each replica is a Pod with its own PersistentVolumeClaim(s). Lowering `volume.replicas` triggers a graceful scale-down: before removing a volume-server Pod the operator evacuates its data to the remaining servers (highest ordinal first, one at a time) and only deletes the Pod once the master confirms the server holds no volumes. A server whose data cannot be moved safely (e.g. no replication-compliant destination) blocks the scale-down rather than risking data loss.
- **`filer`** — serves the namespace and the S3/WebDAV/HTTP APIs.

Fields that commonly cause confusion:

- **`volume.requests.storage`** — the size of each volume-server PVC. This is *where storage comes from*: Kubernetes dynamically provisions a PersistentVolume of this size from the default StorageClass (or `volume.storageClassName` if set) and binds it to the Pod. To use specific or pre-provisioned disks, see `config/samples/seaweed_v1_seaweed_existing_storage.yaml`.
- **`volumeServerDiskCount`** — number of data disks (PVCs) attached to *each* volume-server Pod. They are mounted at `/data0`, `/data1`, … and passed to the volume server as `-dir`. Total PVCs = `volume.replicas × volumeServerDiskCount`. Leave at 1 unless a node exposes multiple disks.
- **`master.volumeSizeLimitMB`** — the max size of a *single logical volume file* before the master allocates a new one (1024 = 1 GiB per file). This is **not** the cluster capacity and **not** the PVC size — total capacity is driven by the volume servers' disks.
- **`hostSuffix`** — optional. Creates a single all-in-one Ingress exposing the cluster under `filer.<hostSuffix>`, `s3.<hostSuffix>`, and `<name>-volume-<n>.<hostSuffix>` (requires an Ingress controller). Omit it for in-cluster-only access, or use the per-component `ingress:` blocks for finer control.
- **`master.config` / `filer.config`** — raw TOML dropped verbatim into that component's config file (`master.toml` / `filer.toml`). Yes, you can paste an existing SeaweedFS filer config here — for example to point the filer's metadata store at Postgres/MySQL/Redis instead of local leveldb2.

To run with a cloud bucket as remote storage (Cloud Drive) backed by a local cache, see `config/samples/seaweed_v1_seaweed_remote_storage.yaml`.

#### Bare-metal volume servers (DaemonSet + node-local disks)

By default volume servers run as a StatefulSet with one or more
dynamically-provisioned PVCs per Pod. On on-prem / bare-metal clusters you often
instead want **one volume server per node, writing straight to that node's
physical disks**. Two `volume` fields enable this:

- **`volume.kind: DaemonSet`** — runs exactly one volume server on every node
  selected by the pod's `nodeSelector` / `affinity` / `tolerations`. `replicas`
  is ignored in this mode (the DaemonSet tracks the node set). The default,
  `StatefulSet`, is unchanged.
- **`volume.hostPath`** — a list of node-local directories to use as data
  directories. Each entry is mounted at `/data0`, `/data1`, … and passed to
  `weed volume -dir`, so a single server can span several physical disks. The
  optional per-entry `maxVolumeCount` caps volumes in that directory (`0` = fill
  the disk); `type` defaults to `DirectoryOrCreate`. When set, no PVCs are
  created.

`hostPath` is required for `kind: DaemonSet` (DaemonSets cannot use
volumeClaimTemplates) and the operator rejects the combination otherwise. It
also works with a `StatefulSet` — pair it with node anti-affinity so two
replicas never share a host directory.

```yaml
spec:
  volume:
    kind: DaemonSet
    replicas: 0            # ignored for DaemonSet
    hostPath:
      - path: /mnt/disks/ssd0
        maxVolumeCount: 100
      - path: /mnt/disks/ssd1
    nodeSelector:
      seaweedfs.com/storage: "true"
    tolerations:
      - key: seaweedfs.com/storage
        operator: Exists
        effect: NoSchedule
```

See `config/samples/seaweed_v1_seaweed_hostpath_daemonset.yaml` for a full
example. For rack/datacenter-aware placement across multiple volume groups, see
[TOPOLOGY_SUPPORT.md](./TOPOLOGY_SUPPORT.md).

### IAM Support

The operator supports IAM (Identity and Access Management) for S3 API authentication. IAM is **embedded in the S3 server** and runs on the same port (8333) as the S3 API.

For complete IAM configuration details, OIDC setup, and troubleshooting, see [IAM_SUPPORT.md](./IAM_SUPPORT.md).

### Example Configuration

```yaml
apiVersion: seaweed.seaweedfs.com/v1
kind: Seaweed
metadata:
  name: seaweed-sample
  namespace: default
spec:
  image: chrislusf/seaweedfs:latest
  volumeServerDiskCount: 1
  hostSuffix: seaweed.abcdefg.com
  master:
    replicas: 3
    volumeSizeLimitMB: 1024
  volume:
    replicas: 1
    requests:
      storage: 2Gi
  filer:
    replicas: 2
    config: |
      [leveldb2]
      enabled = true
      dir = "/data/filerldb2"
  # Standalone S3 gateway — the preferred way to expose the S3 API. Creates a
  # "seaweed-sample-s3" Service on port 8333 (IAM is embedded on the same port
  # by default). See "S3 API" below.
  s3:
    replicas: 1
```

For more examples, see the `config/samples/` directory:
- `seaweed_v1_seaweed.yaml` - Basic deployment with the standalone S3 gateway
- `seaweed_v1_seaweed_annotated.yaml` - Basic deployment with every field explained
- `seaweed_v1_seaweed_existing_storage.yaml` - Specific StorageClass / pre-provisioned PVs for local block storage
- `seaweed_v1_seaweed_hostpath_daemonset.yaml` - Bare-metal volume servers as a DaemonSet on node-local hostPath disks
- `seaweed_v1_seaweed_remote_storage.yaml` - Local cache plus a remote cloud bucket (Cloud Drive)
- `seaweed_v1_seaweed_with_iam_embedded.yaml` - S3 with embedded IAM
- `seaweed_v1_seaweed_with_tls.yaml` - mTLS between components via cert-manager
- `seaweed_v1_seaweed_ingress_tls.yaml` - Expose the S3 API and filer over TLS via per-component Ingress

### S3 API

There are two ways to expose the S3 API. Prefer the **standalone S3 gateway** for new clusters.

**Standalone S3 gateway (recommended)** — set the top-level `spec.s3` block. The
operator runs S3 as its own stateless Deployment and puts a dedicated Service in
front of it named `<cluster-name>-s3` (for a cluster named `seaweed1`, the
Service is `seaweed1-s3`), listening on port `8333`:

```yaml
spec:
  filer:
    replicas: 2          # the gateway dials the filer, so it must be enabled
  s3:
    replicas: 1          # stateless — scale freely
    # port: 8333         # override the default S3 port
    # domainName: s3.example.com   # for virtual-hosted-style buckets
    # metricsPort: 9327  # enable the Prometheus listener + a ServiceMonitor
    configSecret:        # optional: S3 identities (the -s3.config equivalent)
      name: my-s3-config
      key: seaweedfs_s3_config.json
    # service:           # optional: change the Service that fronts the gateway
    #   type: LoadBalancer
    #   annotations: {}
    # ingress:           # optional: per-component Ingress
    #   enabled: true
```

Reach it in-cluster at `http://<cluster-name>-s3.<namespace>.svc:8333`. To expose
it externally, set `spec.s3.service.type: LoadBalancer`, add an `spec.s3.ingress`
block (see **Exposing the cluster via Ingress (TLS)** below), or use the top-level
`hostSuffix`, which publishes `s3.<hostSuffix>` over HTTP.

**Embedded filer S3 (deprecated)** — the older `spec.filer.s3.enabled: true` runs
S3 inside every filer pod and exposes it as the `filer-s3` port on the
`<cluster-name>-filer` Service. There is **no** `<cluster-name>-s3` Service in this
mode. It is retained for backward compatibility but deprecated; the admission
webhook rejects setting both paths at once and warns when the embedded path is
used. Migrate by moving the config to the top-level `spec.s3` block above.

IAM (S3 authentication) is embedded in the S3 server and runs on the same port.
See [IAM_SUPPORT.md](./IAM_SUPPORT.md).

### Exposing the cluster via Ingress (TLS)

By default the operator only creates in-cluster `Service`s. There are two
ways to expose components (S3 API, filer, master/admin UIs, volume servers)
outside the cluster through an Ingress controller. Both require an Ingress
controller (ingress-nginx, Traefik, …) and DNS pointing at it.

- **`hostSuffix`** — the legacy all-in-one helper. One Ingress under
  `filer.<hostSuffix>`, `s3.<hostSuffix>`, and `<name>-volume-<n>.<hostSuffix>`.
  It is **HTTP-only** — it cannot terminate TLS.
- **Per-component `ingress:` blocks** — the recommended path, and the only
  one that supports TLS. Each component carries its own `IngressSpec`, so the
  S3 API and the filer can sit on different hostnames with different
  certificates.

The `ingress:` block is available on `master`, `volume`, `filer`,
`filer.s3Ingress` (the filer's embedded S3 port), `admin`, and the standalone
`s3` and `sftp` gateways. Every block shares the same fields:

| Field | Description |
| --- | --- |
| `enabled` | Create the Ingress for this component. |
| `host` | Hostname the Ingress matches (required when enabled). |
| `className` | `IngressClassName`, e.g. `nginx`. |
| `path` | Path prefix to serve. Defaults to `/`. |
| `annotations` | Controller-specific annotations (cert-manager issuer, nginx body size, …). |
| `tls` | List of `{hosts, secretName}` — terminates TLS using a `kubernetes.io/tls` Secret. |

To reach the S3 API over an `https://` URL, enable the S3 API and give it a
TLS Ingress. With the **filer-embedded** S3 (`filer.s3.enabled`):

```yaml
spec:
  filer:
    replicas: 1
    s3:
      enabled: true            # S3 API on port 8333 (IAM embedded)
    s3Ingress:
      enabled: true
      className: nginx
      host: s3.seaweed.example.com
      annotations:
        # cert-manager issues the cert into secretName below; omit if you
        # created the TLS Secret by hand.
        cert-manager.io/cluster-issuer: letsencrypt-prod
        nginx.ingress.kubernetes.io/proxy-body-size: "0"   # allow large S3 PUTs
      tls:
        - hosts: [s3.seaweed.example.com]
          secretName: seaweed-s3-tls
```

If you are not using cert-manager, create the TLS Secret yourself and drop
the issuer annotation:

```bash
kubectl create secret tls seaweed-s3-tls --cert=tls.crt --key=tls.key
```

Then point any S3 client at the TLS endpoint:

```bash
aws --endpoint-url https://s3.seaweed.example.com s3 ls
```

Prefer the **standalone S3 gateway** (scales independently of the filer) by
putting the same `ingress:` block under the top-level `s3:` instead of
`filer.s3Ingress` — the fields are identical. See
`config/samples/seaweed_v1_seaweed_ingress_tls.yaml` for a complete manifest.

> Note: this Ingress TLS terminates HTTPS at the Ingress controller. It is
> separate from `spec.tls`, which provisions cert-manager-issued **mTLS**
> between SeaweedFS components (master/volume/filer gRPC).

### TLS Between Components (cert-manager)

The operator can provision mTLS between the SeaweedFS components (master, volume,
filer, S3) using [cert-manager](https://cert-manager.io/docs/installation/), which
must be installed in the cluster. When `spec.tls.enabled` is true, the operator
creates a cert-manager `Certificate` covering every component's headless Service
and renders a `security.toml` that wires mTLS into every gRPC endpoint. If the
cert-manager CRDs are absent, the operator records a condition on the `Seaweed` CR
and leaves TLS off instead of failing.

By default (no `issuerRef`) the operator provisions a self-signed `Issuer` + CA
`Certificate` + CA `Issuer` chain owned by the `Seaweed` CR — no external issuer
required:

```yaml
spec:
  tls:
    enabled: true
```

To sign the server certificate from a cert-manager `Issuer` or `ClusterIssuer` you
already manage, set `issuerRef` and the operator skips the self-signed chain:

```yaml
spec:
  tls:
    enabled: true
    issuerRef:
      name: my-ca-issuer
      kind: ClusterIssuer      # or Issuer (the default)
      group: cert-manager.io   # default
```

> Note: this `spec.tls` block configures mTLS *between SeaweedFS components*. It is
> independent of the operator's own admission-webhook serving certificate, which is
> covered under [Installation → Manual](#manual).

See `config/samples/seaweed_v1_seaweed_with_tls.yaml` for a full example.

### Declarative Buckets

The `Bucket` CRD (`seaweed.seaweedfs.com/v1`) provisions S3 buckets
inside an existing `Seaweed` cluster. It mirrors the surface of
`weed shell s3.bucket.*` and `fs.configure` so the same operations
users run manually become declarative manifests that GitOps tools
(FluxCD, ArgoCD, OpenTofu) can apply and reconcile.

A minimal bucket:

```yaml
apiVersion: seaweed.seaweedfs.com/v1
kind: Bucket
metadata:
  name: photos
  namespace: media
spec:
  clusterRef:
    name: seaweed1
    namespace: default
```

Supported per-bucket configuration:

- `versioning`: `Off` (default), `Enabled`, `Suspended`. Once enabled,
  cannot return to `Off` — use `Suspended` to halt new versions while
  retaining version history.
- `objectLock`: enable S3 Object Lock. Requires `versioning: Enabled`
  and is irreversible (matches S3 / SeaweedFS semantics).
- `quota`: cap total stored size with `resource.Quantity` (e.g. `100Gi`)
  and toggle enforcement.
- `owner` / `access`: bind an existing IAM identity as bucket owner and
  grant per-user actions (`Read`, `Write`, `List`, `Tagging`, `Admin`).
  The IAM identity must already exist — the controller does not create
  users on your behalf.
- `placement`: pin replication, disk type, default TTL, fsync, WORM,
  read-only, data center / rack / data node, or pre-grow volumes for
  the bucket's collection. Collection name always equals the bucket
  name and is not configurable.
- `reclaimPolicy`: `Retain` (default) leaves data untouched on CR
  delete; `Delete` removes the bucket on CR delete (refused while
  Object Lock retention applies). `Delete` only removes a bucket this
  CR actually created — a CR whose adoption was refused
  (`BucketAlreadyExists`) never deletes a bucket another resource owns.
- Cross-namespace `clusterRef` is **denied by default**: it resolves only
  when a [`ResourceReferenceGrant`](#cross-namespace-references-resourcereferencegrant)
  in the target `Seaweed`'s namespace permits it. The bucket stays
  `Pending` (condition `ClusterRefForbidden=True`) until a grant exists.
  Same-namespace references never need a grant. Layer Kubernetes RBAC on
  the `Bucket` resource on top if you also want to restrict who can create
  Buckets in the first place.

CEL admission validations enforce: S3-compliant bucket-name regex, the
`objectLock` ↔ `versioning` interlock, immutability of `objectLock` once
enabled, and the "no return to Off" versioning transition rule.

See the `config/samples/seaweed_v1_bucket*.yaml` files for end-to-end
examples (minimal, full-featured, object lock, cross-namespace).

#### Usage stats

The operator periodically refreshes `status.usage` (object count, total
bytes, last-updated timestamp) on every `Bucket` by issuing one
`collection.list` call per Seaweed cluster and patching each bucket's
status. The cadence is configurable via the
`--bucket-usage-refresh-interval` flag (default `5m`). Set to `0` to
disable. The loop is leader-elected so HA deployments do not duplicate
work.

Usage stats are best-effort observation — they do not block reconcile
or affect quota enforcement (the underlying S3 quota check on writes is
authoritative). When a Bucket has not been successfully reconciled yet
(`status.bucketName` empty), it is skipped until the main reconcile
loop has provisioned it.

#### COSI coexistence

The SeaweedFS [COSI driver](https://github.com/seaweedfs/seaweedfs-cosi-driver)
also creates buckets via the upstream `objectstorage.k8s.io/v1alpha1`
API. The two are complementary: COSI is the right choice when an
application needs a bucket-claim lifecycle bound to a workload, while
the `Bucket` CRD is the right choice for cluster- or platform-team-owned
buckets with quotas, placement, and IAM grants. The controller never
adopts or modifies a bucket created by the COSI driver — collisions are
surfaced as `BucketAlreadyExists` in `status` rather than silently
overwriting.

### Declarative IAM (identities, credentials, policies)

Four CRDs (`seaweed.seaweedfs.com/v1`) manage the S3 IAM objects of a
`Seaweed` cluster declaratively, so users, access keys, and permissions
can be GitOps-managed alongside the `Bucket` CRD. They drive the cluster's
embedded IAM service (the IAM gRPC API on the filer — see
[IAM_SUPPORT.md](./IAM_SUPPORT.md)) and mirror `weed shell`'s
`s3.user.*`, `s3.accesskey.*`, and `s3.policy*` commands.

Unlike `Bucket` (which holds data and defaults to `reclaimPolicy: Retain`),
these resources are pure configuration: the CR is the source of truth, so
they default to `reclaimPolicy: Delete` — deleting the CR removes the
underlying IAM object. Set `reclaimPolicy: Retain` to opt out.

- **`S3Identity`** — an IAM user. Created with no credentials by default;
  optionally carries `account` (display name / e-mail) and a `disabled`
  flag. The user name defaults to `metadata.name` (override with
  `spec.name`, which is immutable once set).

  ```yaml
  apiVersion: seaweed.seaweedfs.com/v1
  kind: S3Identity
  metadata: { name: alice, namespace: default }
  spec:
    seaweedRef: { name: seaweed1 }
  ```

- **`S3Credentials`** — an access key / secret key pair for an identity,
  mirrored into a Kubernetes `Secret`. If the referenced `Secret` is
  absent or empty the operator **generates** a key pair and writes it
  (the operator-created `Secret` is annotated as managed and removed with
  the CR under `reclaimPolicy: Delete`); if the `Secret` already holds
  both keys they are **adopted** and registered on the identity. A
  user-managed `Secret` is never deleted by the controller. The secret
  key is written only to the `Secret`, never to status.

  ```yaml
  apiVersion: seaweed.seaweedfs.com/v1
  kind: S3Credentials
  metadata: { name: alice-creds, namespace: default }
  spec:
    seaweedRef: { name: seaweed1 }
    identityRef: { name: alice }
    secretRef: { name: alice-s3-secret }   # accessKeyField/secretKeyField default to accessKey/secretKey
  ```

- **`S3Policy`** — an IAM policy. Author it as structured `statements`
  (assembled into an AWS-style document) or supply a raw `policyDocument`
  JSON string for full control — exactly one is required. In statements,
  `actions` are S3 actions (`s3:GetObject`, …; `*` is shorthand for
  `s3:*`) and `resources` accept bucket-relative shorthand (`my-bucket`,
  `my-bucket/*`), expanded to `arn:aws:s3:::…` ARNs.

  ```yaml
  apiVersion: seaweed.seaweedfs.com/v1
  kind: S3Policy
  metadata: { name: rw-uploads, namespace: default }
  spec:
    seaweedRef: { name: seaweed1 }
    statements:
      - effect: Allow
        actions: [s3:GetObject, s3:PutObject, s3:DeleteObject]
        resources: [my-bucket/uploads/*]
  ```

- **`S3PolicyBinding`** — attaches a policy to a set of identities. The
  controller reconciles to exactly the listed `subjects`; identities
  removed from the list have the policy detached (the identity itself is
  left intact).

  ```yaml
  apiVersion: seaweed.seaweedfs.com/v1
  kind: S3PolicyBinding
  metadata: { name: alice-uploads, namespace: default }
  spec:
    seaweedRef: { name: seaweed1 }
    policyRef: { name: rw-uploads }
    subjects:
      - { kind: S3Identity, name: alice }
      - { kind: S3Identity, name: bob }
  ```

IAM user and policy names are **global to the cluster** while these CRs
are namespaced:

- **Name conflicts** — when CRs in different namespaces claim the same
  IAM name on the same cluster, the oldest claim owns it; later claimants
  are marked `Failed` with a `Ready=False` / `reason: Conflict` condition
  naming the owning CR. Set `spec.name` to give each namespace a distinct
  IAM name.
- **Reference resolution** — `identityRef`, `policyRef`, and `subjects`
  name the referenced `S3Identity` / `S3Policy` **resource** in the same
  namespace and follow its effective IAM name, so a `spec.name` override
  stays transparent to referencing resources. A name with no matching
  resource is used as the IAM name directly, which keeps references to
  IAM objects not managed by any CR (and pre-existing manifests) working.
  Once provisioned, the resolved name is pinned in status so a resource
  created later under the same name cannot silently retarget the
  credential or binding.

`S3Credentials` and `S3PolicyBinding` wait (status `Pending`) until the
identity / policy they reference exists, so apply order does not matter.
As with `Bucket`, a cross-namespace `seaweedRef` (and the `S3Credentials`
`secretRef`) is **denied by default** and requires a
[`ResourceReferenceGrant`](#cross-namespace-references-resourcereferencegrant)
in the target namespace. When the filer enforces `jwt.filer_signing.key`
(rendered into the cluster's `security.toml` whenever a filer or admin is in
spec), the operator reads that key and signs its own IAM gRPC calls with it,
so authenticated filers are handled automatically.

See the `config/samples/seaweed_v1_s3*.yaml` files for end-to-end examples.

### Cross-namespace references (ResourceReferenceGrant)

By default a SeaweedFS resource may only reference resources in **its own
namespace**. A reference that crosses namespaces — a `Bucket`/`S3*`
`seaweedRef`/`clusterRef` pointing at a `Seaweed` in another namespace, or
an `S3Credentials` `secretRef` pointing at a `Secret` in another namespace —
is refused until the **target** namespace publishes a `ResourceReferenceGrant`
that allows it. This mirrors the [Gateway API
`ReferenceGrant`](https://gateway-api.sigs.k8s.io/api-types/referencegrant/):
the namespace that owns the resource being pointed at — not the requester —
decides who may reach in.

The grant lives in the namespace of the resource being referenced. Its
`spec.from` lists the trusted sources — each names a `{group, kind}` plus the
source namespaces, given either as an exact `namespace` or as a
`namespaceSelector` (exactly one per entry) — and its `spec.to` lists the
`{group, kind, name?}` referents in that namespace (omit `name` to allow every
resource of that kind). A reference is allowed when it matches at least one
`from` and one `to` entry.

```yaml
# In the cluster's namespace: let the "media" namespace's Buckets and
# S3Credentials reference the Seaweed cluster "prod".
apiVersion: seaweed.seaweedfs.com/v1
kind: ResourceReferenceGrant
metadata:
  name: allow-media
  namespace: seaweedfs
spec:
  from:
    - { group: seaweed.seaweedfs.com, kind: Bucket, namespace: media }
    - { group: seaweed.seaweedfs.com, kind: S3Credentials, namespace: media }
  to:
    - { group: seaweed.seaweedfs.com, kind: Seaweed }   # any Seaweed here
```

For environments where source namespaces are created on demand (per tenant,
per PR, ...) and cannot be enumerated ahead of time, a `from` entry may select
namespaces by label with `namespaceSelector` instead of naming one. Every
namespace whose labels match is trusted, so labelling a freshly created
namespace grants access without editing the grant. An empty selector (`{}`)
matches **all** namespaces.

```yaml
# Trust every namespace labelled seaweedfs-access=true to reference any
# Seaweed cluster in this namespace.
apiVersion: seaweed.seaweedfs.com/v1
kind: ResourceReferenceGrant
metadata:
  name: allow-labeled-buckets
  namespace: seaweedfs
spec:
  from:
    - group: seaweed.seaweedfs.com
      kind: Bucket
      namespaceSelector:
        matchLabels: { seaweedfs-access: "true" }
  to:
    - { group: seaweed.seaweedfs.com, kind: Seaweed }
```

While a required grant is missing the referencing resource stays `Pending`
(`Bucket` surfaces condition `ClusterRefForbidden=True`; the `S3*` kinds
surface `ReferenceGranted=False`) and reconciles to ready automatically once
the grant is created — at which point the condition is cleared.

Enforcement is reconcile-time and eventually consistent (like every
cross-resource dependency here, and like Gateway API): revoking a grant stops
the operator from (re)provisioning the reference on the next reconcile, but
does **not** retroactively tear down objects already provisioned under it.
Deleting a resource is **never** blocked by a missing grant, so revoking one
cannot strand a finalizer. See
`config/samples/seaweed_v1_resourcereferencegrant.yaml`.

### CSI Driver (mount volumes as PersistentVolumes)

The operator can also deploy the
[seaweedfs-csi-driver](https://github.com/seaweedfs/seaweedfs-csi-driver) so
that pods mount a SeaweedFS filer as ordinary `PersistentVolume`s (a POSIX FUSE
mount), including `ReadWriteMany` volumes shared across nodes. This is the
filesystem alternative to the S3 API above.

A CSI driver is node-global, so it is managed through its own opt-in
`SeaweedCSIDriver` resource rather than a field on the `Seaweed` CR, and the
controller is **off by default** — enable it with `ENABLE_CSI_DRIVER=true` on
the operator manager. The driver can mount an operator-managed cluster
(`seaweedRef`, grant-gated across namespaces) or any external filer
(`filerAddress`):

```yaml
apiVersion: seaweed.seaweedfs.com/v1
kind: SeaweedCSIDriver
metadata:
  name: seaweedfs
spec:
  seaweedRef:
    name: seaweed1
  storageClass:
    name: seaweedfs
    parameters:
      replication: "000"
```

Pods then request a PVC against the `seaweedfs` `StorageClass`. See
[CSI_SUPPORT.md](CSI_SUPPORT.md) for the full guide, API reference, and the
list of managed objects. Example:
`config/samples/seaweed_v1_seaweedcsidriver.yaml`.

### Scheduled admin scripts (AdminScript)

The `AdminScript` CRD (`seaweed.seaweedfs.com/v1`) runs a `weed shell` script
on a cron schedule against a cluster — for recurring maintenance such as
`volume.balance`, `volume.fix.replication`, `ec.encode`, or `volume.vacuum`.
The operator reconciles each `AdminScript` into a native Kubernetes `CronJob`
in the same namespace, owned by the CR (so deleting the `AdminScript` removes
the `CronJob`). Each run pipes the `script` into `weed shell -master=<cluster
masters>` (and `-filer=<cluster filer>` when the cluster runs a filer). The run
pod mirrors the cluster's admin/worker pods — it uses the cluster image and,
when the cluster has mTLS enabled, mounts the same `security.toml`/TLS material
so the shell authenticates to the masters over gRPC.

```yaml
apiVersion: seaweed.seaweedfs.com/v1
kind: AdminScript
metadata:
  name: nightly-balance
  namespace: default
spec:
  clusterRef:
    name: seaweed-sample        # a Seaweed CR in the same namespace
  schedule: "0 2 * * *"         # daily at 02:00
  script: |
    lock
    volume.balance -force
    volume.fix.replication
    unlock
```

- `concurrencyPolicy` defaults to `Forbid` so overlapping maintenance runs
  never stack up; `suspend: true` pauses scheduling without deleting the CR.
- `successfulJobsHistoryLimit` / `failedJobsHistoryLimit` / `backoffLimit` /
  `activeDeadlineSeconds` / `startingDeadlineSeconds` / `timeZone` are passed
  through to the generated `CronJob`/`Job`.
- `image` overrides the container image (defaults to the cluster's image), and
  `credentialsSecret` projects a Secret's keys into the run pod as environment
  variables for scripts that need them (defaults to the cluster's admin
  `credentialsSecret` when set).
- Status surfaces `phase` (`Pending`/`Active`/`Suspended`), the managed
  `cronJobName`, and the CronJob's `lastScheduleTime`/`lastSuccessfulTime`.

`kubectl get adminscripts` (short name `swas`) lists them. Example:
`config/samples/seaweed_v1_adminscript.yaml`.

## Maintenance and Uninstallation

- TBD

## Development

Follow the instructions in <https://sdk.operatorframework.io/docs/building-operators/golang/quickstart/>

```bash
# install and prepare kind-cluster for development
make kind-prepare

# build the operator image and load the image into Kind cluster
make kind-load

# deploy operator and CRDs
make deploy

# install example of CR
kubectl apply -f config/samples/seaweed_v1_seaweed.yaml

# or install example with S3 and embedded IAM
kubectl apply -f config/samples/seaweed_v1_seaweed_with_iam_embedded.yaml
```

### Testing IAM Functionality

To test the embedded IAM implementation:

```bash
# Run IAM-specific tests
go test -v -run "Filer.*IAM|IAM.*Filer" ./internal/controller

# Run all tests
make test
```

### Update the Operator

```bash
# rebuild and re-upload image to the kind
make kind-load

# redeploy operator and CRDs
make redeploy
```

### Develop outside of k8s

```bash
# register the CRD with the Kubernetes cluster
make install

# run the operator locally outside the Kubernetes cluster
make run ENABLE_WEBHOOKS=false

# From another terminal in the same directory
kubectl apply -f config/samples/seaweed_v1_seaweed.yaml
```
