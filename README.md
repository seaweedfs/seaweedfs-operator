# SeaweedFS Operator

This [Kubernetes Operator](https://kubernetes.io/docs/concepts/extend-kubernetes/operator/) is made to easily deploy SeaweedFS onto your Kubernetes cluster.

The difference to [seaweedfs-csi-driver](https://github.com/seaweedfs/seaweedfs-csi-driver) is that the infrastructure (SeaweedFS) itself runs on Kubernetes as well (Master, Filer, Volume-Servers) and can as such easily scale with it as you need. It is also by far more resilient to failures than a simple systemD service in regards to handling crashing services or accidental deletes.

## Features

- [x] Automatically deploy and manage a SeaweedFS cluster
- [x] Ability to be managed by other Operators
- [ ] Compatibility with [seaweedfs-csi-driver](https://github.com/seaweedfs/seaweedfs-csi-driver)
- [x] Auto rolling upgrade and restart
- [x] Ingress for volume server, filer and S3, to support HDFS, REST filer, S3 API and cross-cluster replication
- [ ] Support all major cloud Kubernetes: AWS, Google, Azure
- [x] [Async backup](https://github.com/seaweedfs/seaweedfs/wiki/Async-Backup) to cloud storage: S3, Google Cloud Storage, Azure
- [ ] Put warm data to cloud storage tier: S3, Google Cloud Storage, Azure
- [x] Grafana dashboard
- [x] Admin UI for cluster management and monitoring
- [x] BucketClaim for bucket and credentials management

## Quick Start

For a quick start guide, see [Quick Start Guide](https://github.com/nnstd/seaweedfs-operator/wiki/Quick-Start).

## Installation

For detailed installation instructions, see [Installation Guide](https://github.com/nnstd/seaweedfs-operator/wiki/Installation-Guide).

### Quick Installation with Helm

```bash
helm repo add seaweedfs-operator https://nnstd.github.io/seaweedfs-operator
helm install seaweedfs-operator seaweedfs-operator/seaweedfs-operator
```

## Documentation

- **[Home](https://github.com/nnstd/seaweedfs-operator/wiki/Home)** - Overview and getting started
- **[Quick Start Guide](https://github.com/nnstd/seaweedfs-operator/wiki/Quick-Start)** - Get up and running quickly
- **[Installation Guide](https://github.com/nnstd/seaweedfs-operator/wiki/Installation-Guide)** - Detailed installation instructions
- **[Configuration Reference](https://github.com/nnstd/seaweedfs-operator/wiki/Configuration-Reference)** - Complete configuration options
- **[Seaweed Resource](https://github.com/nnstd/seaweedfs-operator/wiki/Seaweed-Resource)** - SeaweedFS cluster configuration
- **[BucketClaim Resource](https://github.com/nnstd/seaweedfs-operator/wiki/BucketClaim-Resource)** - Bucket management

### Examples

- **[Basic Cluster](https://github.com/nnstd/seaweedfs-operator/wiki/Basic-Cluster)** - Simple SeaweedFS cluster setup
- **[Admin UI](https://github.com/nnstd/seaweedfs-operator/wiki/Admin-UI)** - Deploy with Admin UI for management
- **[Backup Configuration](https://github.com/nnstd/seaweedfs-operator/wiki/Backup-Configuration)** - Configure cloud backups
- **[Bucket Management](https://github.com/nnstd/seaweedfs-operator/wiki/Bucket-Management)** - Manage buckets and credentials

## Basic Example

```yaml
apiVersion: seaweed.seaweedfs.com/v1
kind: Seaweed
metadata:
  name: seaweed1
  namespace: default
spec:
  image: chrislusf/seaweedfs:latest
  storage:
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
```

For more examples, see the [wiki](https://github.com/nnstd/seaweedfs-operator/wiki).

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
