# CSI Driver Support in SeaweedFS Operator

This document describes how to deploy and use the [SeaweedFS CSI
driver](https://github.com/seaweedfs/seaweedfs-csi-driver) through the
SeaweedFS Operator, using the `SeaweedCSIDriver` custom resource.

## What is the CSI driver for?

The [Container Storage Interface](https://kubernetes-csi.github.io/docs/) (CSI)
is the standard way Kubernetes consumes external storage. The SeaweedFS CSI
driver lets a SeaweedFS **filer** back ordinary Kubernetes
`PersistentVolume`s: a pod requests a `PersistentVolumeClaim` against a
SeaweedFS `StorageClass`, and the driver mounts a path in the filer (over FUSE)
into the pod as a normal directory. The application reads and writes files;
the bytes land in SeaweedFS.

This is different from the operator's existing **S3** and **bucket** support.
Use S3/IAM when your application speaks the S3 API. Use the CSI driver when
your application wants a **POSIX-style filesystem mount** instead.

### Is it a sidecar?

No. The CSI driver is **cluster infrastructure**, not a per-application
sidecar. An application pod that wants SeaweedFS storage simply declares a PVC
and a volume — it does **not** embed any SeaweedFS container. The driver runs
as shared components:

| Component | Workload | Role |
|-----------|----------|------|
| Controller | `Deployment` (1+ replicas) | Provisions/deletes/expands volumes (control plane). Runs the standard `csi-provisioner`, `csi-resizer`, and (optionally) `csi-attacher` sidecars. |
| Node plugin | `DaemonSet` (every node) | Mounts/unmounts volumes on the node where a consuming pod is scheduled. Runs the `csi-node-driver-registrar`. |
| Mount service | `DaemonSet` (every node) | Performs the actual FUSE mounts that the node plugin delegates to over a shared host socket. |

The only containers commonly called "sidecars" here are the upstream
`sig-storage` helper containers (`csi-provisioner`, `csi-attacher`, etc.) that
sit next to the driver **inside the controller/node pods** — never inside your
application pods.

> If you specifically want a per-pod mount (for example a `weed mount`
> container co-located with a single app), that is the separate "sidecar mount"
> pattern and does not use this CRD. The CSI driver is the cluster-wide
> alternative that lets any pod use SeaweedFS through a plain PVC.

### What it supports

The SeaweedFS CSI driver advertises:

- **Dynamic provisioning** and **static provisioning**.
- **`ReadWriteMany` (RWX)** — because the filer is a shared filesystem, the
  same volume can be mounted read-write on many nodes/pods at once
  (`MULTI_NODE_MULTI_WRITER`). It also supports `ReadWriteOnce`
  (`SINGLE_NODE_WRITER`). A volume mounted with a read-only flag is honored as
  read-only by the node plugin, but `ReadOnlyMany` is not an advertised access
  mode.
- **Online volume expansion** (`allowVolumeExpansion`).

This RWX capability is the main reason to choose SeaweedFS CSI over a
block-device CSI driver, which is typically `ReadWriteOnce` only.

## Enabling the controller

The `SeaweedCSIDriver` controller is **off by default** — enabling it installs
a cluster-wide `CSIDriver` registration plus privileged node DaemonSets, which
not every cluster wants. Turn it on by setting the environment variable on the
operator's manager container:

```
ENABLE_CSI_DRIVER=true
```

A CSI driver is **node-global**: the kubelet registers exactly one driver per
`driverName` per node, regardless of how many SeaweedFS clusters run on it.
For that reason the driver is modeled as its own opt-in resource rather than a
field on the `Seaweed` CR.

## Quick start

Point a `SeaweedCSIDriver` at an operator-managed `Seaweed` cluster and let it
manage a `StorageClass`:

```yaml
apiVersion: seaweed.seaweedfs.com/v1
kind: SeaweedCSIDriver
metadata:
  name: seaweedfs
  namespace: default
spec:
  # Mount an operator-managed Seaweed cluster in the same namespace.
  seaweedRef:
    name: seaweed1
  driverName: seaweedfs-csi-driver
  storageClass:
    name: seaweedfs
    isDefaultClass: false
    reclaimPolicy: Delete
    volumeBindingMode: Immediate
    allowVolumeExpansion: true
    parameters:
      replication: "000"
```

Then consume it from any pod via a PVC:

```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: shared-data
spec:
  accessModes: ["ReadWriteMany"]   # SeaweedFS supports RWX
  storageClassName: seaweedfs
  resources:
    requests:
      storage: 5Gi
---
apiVersion: v1
kind: Pod
metadata:
  name: app
spec:
  containers:
    - name: app
      image: busybox
      command: ["sh", "-c", "echo hello > /data/hello && sleep 3600"]
      volumeMounts:
        - name: data
          mountPath: /data
  volumes:
    - name: data
      persistentVolumeClaim:
        claimName: shared-data
```

## Mounting an external filer

To mount a filer **not** managed by this operator, give an explicit address
instead of `seaweedRef` (exactly one of the two must be set):

```yaml
apiVersion: seaweed.seaweedfs.com/v1
kind: SeaweedCSIDriver
metadata:
  name: external
  namespace: storage
spec:
  filerAddress: my-filer.storage.svc:8888   # filer HTTP host:port
  driverName: seaweedfs-csi-driver
```

## Cross-namespace references

When `seaweedRef` points at a `Seaweed` cluster in **another** namespace, the
reference is denied until a `ResourceReferenceGrant` in the target cluster's
namespace permits it — the same deny-by-default model used by the `Bucket` and
S3 IAM CRDs. Until granted, the `SeaweedCSIDriver` stays `Pending` with
`ReferenceGranted=False`. Same-namespace references are always allowed.

## What the operator creates

For a `SeaweedCSIDriver` named `X` in namespace `N`, the operator reconciles:

- `ServiceAccount` `seaweedfs-csi-X-controller` and `seaweedfs-csi-X-node`
- `ClusterRole`/`ClusterRoleBinding` `seaweedfs-csi-X-controller` and
  `seaweedfs-csi-X-node`, plus a namespaced leader-election `Role`/`RoleBinding`
- `Deployment` `seaweedfs-csi-X-controller`
- `DaemonSet` `seaweedfs-csi-X-node` and (unless disabled) `seaweedfs-csi-X-mount`
- the cluster-scoped `CSIDriver` object named after `driverName`
- an optional `StorageClass` (when `spec.storageClass` is set)

Namespaced objects are owned by the CR and garbage-collected with it. The
cluster-scoped objects (`CSIDriver`, `StorageClass`, `ClusterRole`,
`ClusterRoleBinding`) cannot carry an owner reference to a namespaced CR, so
they are cleaned up by the CR's finalizer on deletion.

## API reference

### `spec`

| Field | Default | Description |
|-------|---------|-------------|
| `seaweedRef.{name,namespace}` | — | Operator-managed `Seaweed` cluster to mount. Mutually exclusive with `filerAddress`. |
| `filerAddress` | — | Explicit filer HTTP `host:port` for an external filer. Mutually exclusive with `seaweedRef`. |
| `driverName` | `seaweedfs-csi-driver` | CSI driver name registered with kubelet and referenced by StorageClasses. **Node-global and immutable.** |
| `image` | `chrislusf/seaweedfs-csi-driver:v1.4.20` | Driver image for the controller and node pods. |
| `imagePullPolicy` | — | Applied to all rendered containers. |
| `imagePullSecrets` | — | Pull secrets in the deployment namespace. |
| `logVerbosity` | — | Driver glog `-v` level (0–10). |
| `cacheCapacityMB` | — | Per-node chunk cache size in MiB (`--cacheCapacityMB`). |
| `concurrentWriters` / `concurrentReaders` | — | Per-mount concurrency caps. |
| `sidecars.{provisioner,attacher,resizer,nodeDriverRegistrar,livenessProbe}` | pinned upstream images | Override individual sig-storage sidecar images. |
| `controller.replicas` | `1` | Controller `Deployment` replicas (run 2+ for HA; leader election is enabled). |
| `controller.attacherEnabled` | `true` | Run the `csi-attacher` sidecar and set `CSIDriver.attachRequired`. |
| `controller.{resources,nodeSelector,tolerations,affinity}` | — | Controller pod scheduling. Affinity defaults to soft pod anti-affinity. |
| `node.kubeletPath` | `/var/lib/kubelet` | Host kubelet root directory. Override on k3s/MicroK8s/k0s. |
| `node.hostPID` | `true` | Let the node plugin enter container mount namespaces to repair stale mounts. |
| `node.{resources,nodeSelector,tolerations,updateStrategy}` | — | Node DaemonSet scheduling/strategy. |
| `mountService.enabled` | `true` | Deploy the mount DaemonSet. Required for the node component to mount volumes. |
| `mountService.image` | `chrislusf/seaweedfs-mount:v1.4.20` | Mount service image. |
| `mountService.socketDir` | `/var/lib/seaweedfs-mount` | Host directory shared between the node plugin and mount service. |
| `mountService.{resources,nodeSelector,tolerations,updateStrategy}` | `updateStrategy: OnDelete` | Mount DaemonSet scheduling/strategy. |
| `storageClass` | — | Manage a `StorageClass` for this driver (omit to manage it out-of-band). |

### `spec.storageClass`

| Field | Default | Description |
|-------|---------|-------------|
| `name` | `driverName` | Name of the managed `StorageClass`. |
| `isDefaultClass` | `false` | Mark it the cluster default class. |
| `reclaimPolicy` | `Delete` | `Delete` or `Retain`. |
| `volumeBindingMode` | `Immediate` | `Immediate` or `WaitForFirstConsumer`. |
| `allowVolumeExpansion` | `true` | Permit online expansion. |
| `parameters` | — | Passed verbatim to the driver at provision time (see below). |
| `mountOptions` | — | Added to every provisioned PV. |

Common `parameters` accepted by the driver include `collection`,
`replication`, `diskType`, `ttl`, `dataCenter`, `path`, `cacheCapacityMB`,
`map.uid`, and `map.gid`. See the
[seaweedfs-csi-driver](https://github.com/seaweedfs/seaweedfs-csi-driver)
documentation for the authoritative list.

### `status`

- `observedGeneration`: the `.metadata.generation` the controller last reconciled
- `phase`: `Pending` | `Ready` | `Degraded` | `Failed` | `Terminating`
- `conditions`: `Ready`, `ClusterReachable`, `ReferenceGranted`,
  `ControllerAvailable`, `NodeAvailable`, `DriverNameConflict`
- `driverName`, `resolvedFilerAddress`
- `controller.{desired,ready}`, `node.{desired,ready}` — replica readiness

```sh
kubectl get seaweedcsidriver          # short name: swcsi
kubectl describe swcsi seaweedfs
```

## `driverName` is node-global

Because the kubelet allows only one registration per `driverName` per node, two
`SeaweedCSIDriver` objects must not claim the same `driverName`. If they do, the
**oldest** object owns it and the others report
`DriverNameConflict=True`, move to `Failed`, and reconcile no workloads. Give
each driver a distinct `driverName` (and a distinct `storageClass.name`) to run
more than one in a cluster.

## Uninstall and cleanup

Deleting a `SeaweedCSIDriver` removes all of its objects, including the
cluster-scoped `CSIDriver`, `StorageClass`, and RBAC, via the finalizer.
Existing `PersistentVolume`s provisioned with `reclaimPolicy: Retain` are left
intact; PVs with `reclaimPolicy: Delete` follow normal CSI deletion. Disabling
the attacher (`controller.attacherEnabled: false`) on an existing driver
recreates the immutable `CSIDriver` object; any leftover `VolumeAttachment`
objects must be cleaned up by an administrator.
