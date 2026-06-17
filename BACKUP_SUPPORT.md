# Backup & Restore Support

The operator can back up and restore a SeaweedFS cluster's **filer metadata**
(point-in-time snapshots) and continuously **mirror file data** to cloud object
storage or a PersistentVolumeClaim. Configuration is declared on the `Seaweed`
CR (`spec.backup`), and individual backups/restores are driven by the
`SeaweedBackup` and `SeaweedRestore` CRDs.

## How it maps to SeaweedFS

SeaweedFS provides two complementary primitives, and the operator models each
the way it actually behaves:

| Concern | `weed` primitive | Shape | CRD / field |
|---|---|---|---|
| **Metadata** (filer namespace + file→chunk mappings) | `fs.meta.save` / `fs.meta.load` | one-shot snapshot | `SeaweedBackup` / `SeaweedRestore` |
| **Data** (file content) | `weed filer.backup` | continuous daemon | `spec.backup.dataMirror` Deployment |

A metadata snapshot is small and point-in-time, so it is schedulable and
retained. `weed filer.backup` continuously replicates file content to a
replication sink and never exits, so it is run as an always-on Deployment
rather than a one-shot Job.

> **Restore is metadata-level.** `fs.meta.load` rebuilds the filer namespace
> and chunk references. The underlying volume data must still exist (same-cluster
> recovery) or be re-seeded from a data mirror (cross-cluster DR). Automated
> cross-cluster data reseed is a planned follow-up.

## Configuring backup on the cluster

```yaml
apiVersion: seaweed.seaweedfs.com/v1
kind: Seaweed
metadata:
  name: seaweed-sample
spec:
  image: chrislusf/seaweedfs:latest
  master: { replicas: 1 }
  volume: { replicas: 1, requests: { storage: 10Gi } }
  filer:  { replicas: 1 }
  backup:
    # Optional weed image override for backup/restore/mirror pods.
    # image: chrislusf/seaweedfs:latest

    storages:
      # A PersistentVolumeClaim destination (self-contained metadata snapshots).
      pvc:
        type: filesystem
        filesystem:
          existingClaim: seaweed-backups
          mountPath: /backup

      # An S3-compatible destination (AWS, MinIO, or a SeaweedFS S3 gateway).
      s3-main:
        type: s3
        s3:
          bucket: my-seaweedfs-backups
          region: us-east-1
          endpoint: ""            # leave empty for AWS
          directory: /
          forcePathStyle: true
        credentialsSecret: s3-backup-creds

    # Cron-driven metadata snapshots (creates SeaweedBackup objects).
    schedule:
      - name: nightly
        schedule: "0 2 * * *"     # standard 5-field cron
        storageName: pvc
        keep: 7                    # retain the 7 most recent completed snapshots
        filerPath: /

    # Continuous data mirrors (one `weed filer.backup` Deployment each).
    dataMirror:
      - storageName: s3-main
        filerPath: /
```

### Storage types & credentials

Each storage maps to a `weed` replication sink. Credentials come from the
storage's `credentialsSecret` (in the cluster's namespace) using these keys:

| Type | Spec fields | Secret keys |
|---|---|---|
| `s3` | `bucket`, `region`, `endpoint`, `directory`, `forcePathStyle` | `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY` (optional — falls back to the ambient AWS credential chain) |
| `gcs` | `bucket`, `directory` | `GOOGLE_APPLICATION_CREDENTIALS_JSON` (service-account JSON) |
| `azure` | `accountName`, `container`, `directory` | `AZURE_STORAGE_ACCOUNT_KEY` (**required**) |
| `b2` | `bucket`, `region`, `directory` | `B2_ACCOUNT_ID`, `B2_MASTER_APPLICATION_KEY` (**required**) |
| `filesystem` | `existingClaim`, `mountPath`, `subPath` | none |

Example S3 credentials Secret:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: s3-backup-creds
type: Opaque
stringData:
  AWS_ACCESS_KEY_ID: AKIA...
  AWS_SECRET_ACCESS_KEY: ...
```

## On-demand metadata snapshot

```yaml
apiVersion: seaweed.seaweedfs.com/v1
kind: SeaweedBackup
metadata:
  name: adhoc-1
spec:
  clusterName: seaweed-sample
  storageName: pvc
  filerPath: /
```

The controller creates a one-shot Job that runs `fs.meta.save` and stores
`<cluster>/<backup>/filer.meta.gz` on the storage. Track it with:

```bash
kubectl get seaweedbackups
# NAME      CLUSTER          STORAGE   PHASE       COMPLETED   AGE
# adhoc-1   seaweed-sample   pvc       Completed   12s         30s
```

For **object-store** storages the snapshot is staged into a reserved hidden
filer path (`/.seaweedfs-operator/backups/<backup>/filer.meta.gz`); the storage's
data mirror then replicates it off-cluster. Configure a `dataMirror` to the
same storage when you want object-store metadata snapshots to leave the cluster.

## Scheduled snapshots

`spec.backup.schedule` is evaluated by a leader-elected scheduler. When a cron
fires it creates a `SeaweedBackup` named `<cluster>-<schedule>-<timestamp>`,
then prunes completed snapshots beyond `keep` (oldest first; running snapshots
are never pruned). Set `suspend: true` to pause a schedule.

> Retention prunes the `SeaweedBackup` objects (and their Jobs). The snapshot
> artifacts on the storage are **retained** — manage their lifecycle with the
> object store's lifecycle policies or by pruning the PVC.

## Continuous data mirror

Each `spec.backup.dataMirror` entry produces a Deployment
`<cluster>-backup-mirror-<storage>` running `weed filer.backup -initialSnapshot`
into the storage sink, plus a rendered `replication.toml` Secret. Removing an
entry deletes its Deployment and Secret. Status is surfaced on the cluster:

```bash
kubectl get seaweed seaweed-sample -o jsonpath='{.status.backupMirrors}'
```

> `-initialSnapshot` re-walks the full tree on each pod (re)start, which seeds a
> fresh sink but is expensive on large trees. Mirrors use the `Recreate`
> strategy so two never run against the same checkpoint at once.

## Restore

```yaml
apiVersion: seaweed.seaweedfs.com/v1
kind: SeaweedRestore
metadata:
  name: restore-1
spec:
  clusterName: seaweed-sample
  backupName: adhoc-1        # restore a SeaweedBackup in this namespace
  # --- or, restore an explicit snapshot location (mutually exclusive):
  # backupSource:
  #   storageName: pvc
  #   metaPath: seaweed-sample/adhoc-1/filer.meta.gz   # relative to the storage root (filesystem)
  filerPath: /
```

The controller creates a one-shot Job that fetches the snapshot (off the PVC, or
back from the reserved filer path for object stores via `weed filer.cat`) and
runs `fs.meta.load` into the target filer. When `filerPath` is not `/`, the load
is scoped with `-dirPrefix`.

## TLS clusters

Snapshot/restore Jobs and mirror Deployments mount the cluster's `security.toml`
and certificates the same way the core components do, so they work on clusters
with `spec.tls.enabled: true`.

## RBAC

The operator's manager role gains `batch/jobs` (create/manage backup Jobs) and
the new `seaweedbackups` / `seaweedrestores` resources. Both the kustomize
(`config/rbac/role.yaml`) and Helm (`deploy/helm/templates/rbac/role.yaml`)
roles are updated; the `test/helm` RBAC parity test guards against drift.
