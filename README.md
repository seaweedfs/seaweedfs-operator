
[![Build Status](https://travis-ci.com/seaweedfs/seaweedfs-operator.svg?branch=master)](https://travis-ci.com/github/seaweedfs/seaweedfs-operator)

# SeaweedFS Operator

This [Kubernetes Operator](https://kubernetes.io/docs/concepts/extend-kubernetes/operator/) is made to easily deploy SeaweedFS onto your Kubernetes cluster.

The operator manages the complete SeaweedFS infrastructure on Kubernetes, including Master servers, Volume servers, and Filer services with S3-compatible API and embedded IAM (Identity and Access Management). This provides a scalable, resilient distributed file system with built-in authentication.

The difference to [seaweedfs-csi-driver](https://github.com/seaweedfs/seaweedfs-csi-driver) is that the infrastructure (SeaweedFS) itself runs on Kubernetes as well (Master, Filer, Volume-Servers) and can as such easily scale with it as you need. It is also by far more resilent to failures then a simple systemD service in regards to handling crashing services or accidental deletes.

By using `make deploy` it will deploy a Resource of type 'Seaweed' onto your current kubectl $KUBECONFIG target (the operator itself) which by default will do nothing unless you configurate it (see examples in config/samples/).

Goals:

- [x] Automatically deploy and manage a SeaweedFS cluster
- [x] Ability to be managed by other Operators
- [ ] Compability with [seaweedfs-csi-driver](https://github.com/seaweedfs/seaweedfs-csi-driver)
- [x] Auto rolling upgrade and restart
- [x] Ingress for volume server, filer and S3, to support HDFS, REST filer, S3 API and cross-cluster replication
- [x] IAM (Identity and Access Management) service support for S3 API authentication and authorization
- [ ] Support all major cloud Kubernetes: AWS, Google, Azure
- [ ] Scheduled backup to cloud storage: S3, Google Cloud Storage , Azure
- [ ] Put warm data to cloud storage tier: S3, Google Cloud Storage , Azure
- [x] Grafana dashboard

## Installation

### Helm

```bash
helm repo add seaweedfs-operator https://seaweedfs.github.io/seaweedfs-operator/
helm template seaweedfs-operator seaweedfs-operator/seaweedfs-operator
```

> **Note**: For versions prior to 0.1.2, the legacy repository URL `https://seaweedfs.github.io/seaweedfs-operator/helm` can still be used, but new releases will only be published to the main repository URL above.

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
  values:
    webhook:
      enabled: false
```

NOTE: Due to an issue with the way the `seaweedfs-operator-webhook-server-cert` is created, `.Values.webhook.enabled` should be set to `false` initially, and then `true` later on. After the deployment is created, modify the `seaweedfs-operator-helmrelease.yaml` file to remove the `values` directive and everything underneath it.

### Manual

This operator uses `kustomize` for deployment. Please [install kustomize](https://kubectl.docs.kubernetes.io/installation/kustomize/) if you do not have it.

By default, the defaulting and validation webhooks are disabled. We strongly recommend to enable the webhooks.

First clone the repository:

```bash
git clone https://github.com/seaweedfs/seaweedfs-operator --depth=1
```

To deploy the operator with webhooks enabled, make sure you have installed the `cert-manager`(Installation docs: <https://cert-manager.io/docs/installation/>) in your cluster, then follow the instructions in the `config/default/kustomization.yaml` file to uncomment the components you need.
Lastly, change the value of `ENABLE_WEBHOOKS` to `"true"` in `config/manager/manager.yaml`

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
seaweedfs-operator-system   seaweedfs-operator-controller-manager-54cc768f4c-cwz2k   2/2     Running   0          34m
```

See the next section for example usage - **at this point you only deployed the Operator itself!**

### You need to also deploy a configuration to get it running (see next section)!

## Configuration Examples

### Basic SeaweedFS Deployment

For detailed configuration options and examples, see the sample configurations in the `config/samples/` directory.

### IAM Support

The operator supports IAM (Identity and Access Management) for S3 API authentication. IAM is **embedded in the S3 server by default** and runs on the same port (8333) as the S3 API. This follows the pattern used by MinIO and Ceph RGW.

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
    s3:
      enabled: true   # Enable S3 API (IAM is enabled by default)
    # iam: true       # Optional: IAM is enabled by default when S3 is enabled
    config: |
      [leveldb2]
      enabled = true
      dir = "/data/filerldb2"
```

For more examples, see the `config/samples/` directory:
- `seaweed_v1_seaweed_with_iam_embedded.yaml` - S3 with embedded IAM
- `seaweed_v1_seaweed.yaml` - Basic deployment

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
