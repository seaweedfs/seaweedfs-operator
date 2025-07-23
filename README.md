# SeaweedFS Operator

This [Kubernetes Operator](https://kubernetes.io/docs/concepts/extend-kubernetes/operator/) is made to easily deploy SeaweedFS onto your Kubernetes cluster.

The difference to [seaweedfs-csi-driver](https://github.com/seaweedfs/seaweedfs-csi-driver) is that the infrastructure (SeaweedFS) itself runs on Kubernetes as well (Master, Filer, Volume-Servers) and can as such easily scale with it as you need. It is also by far more resilent to failures then a simple systemD service in regards to handling crashing services or accidental deletes.

By using `make deploy` it will deploy a Resource of type 'Seaweed' onto your current kubectl $KUBECONFIG target (the operator itself) which by default will do nothing unless you configurate it (see examples in config/samples/).

Goals:

- [x] Automatically deploy and manage a SeaweedFS cluster
- [x] Ability to be managed by other Operators
- [ ] Compability with [seaweedfs-csi-driver](https://github.com/seaweedfs/seaweedfs-csi-driver)
- [x] Auto rolling upgrade and restart
- [x] Ingress for volume server, filer and S3, to support HDFS, REST filer, S3 API and cross-cluster replication
- [ ] Support all major cloud Kubernetes: AWS, Google, Azure
- [x] Scheduled backup to cloud storage: S3, Google Cloud Storage, Azure
- [ ] Put warm data to cloud storage tier: S3, Google Cloud Storage, Azure
- [x] Grafana dashboard
- [x] Admin UI for cluster management and monitoring

## Installation

### Helm

```bash
helm repo add seaweedfs-operator https://nnstd.github.io/seaweedfs-operator
helm template seaweedfs-operator seaweedfs-operator/seaweedfs-operator
```

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
  url: https://seaweedfs.github.io/seaweedfs-operator/helm
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

## Admin UI

The SeaweedFS Operator supports deploying the SeaweedFS Admin UI, which provides a comprehensive web-based administration interface for managing SeaweedFS clusters. The Admin UI includes:

- **Cluster topology visualization and monitoring**
- **Volume management and operations**
- **File browser and management**
- **System metrics and performance monitoring**
- **Configuration management**

### Basic Admin UI Configuration

```yaml
apiVersion: seaweed.seaweedfs.com/v1
kind: Seaweed
metadata:
  name: seaweed-with-admin
spec:
  image: chrislusf/seaweedfs:latest
  
  master:
    replicas: 3
  
  volume:
    replicas: 3
  
  filer:
    replicas: 1
    s3: true
  
  # Admin UI configuration
  admin:
    replicas: 1
    port: 23646  # Default admin port
    adminUser: "admin"
    adminPassword: "admin123"
    service:
      type: LoadBalancer  # Or ClusterIP for internal access
    persistence:
      enabled: true
      mountPath: "/data"
      resources:
        requests:
          storage: 1Gi
```

### Secure Admin UI Configuration

For production deployments, use secret references for passwords and TLS certificates:

```yaml
apiVersion: seaweed.seaweedfs.com/v1
kind: Seaweed
metadata:
  name: seaweed-secure-admin
spec:
  # ... other components ...
  
  admin:
    replicas: 1
    adminUser: "admin"
    # Use secret reference for password
    adminPasswordSecretRef:
      name: "admin-password-secret"
      key: "password"
    
    # TLS configuration
    tls:
      enabled: true
      certificateSecretRef:
        name: "admin-tls-secret"
        mapping:
          cert: "tls.crt"
          key: "tls.key"
    
    service:
      type: LoadBalancer
      annotations:
        service.beta.kubernetes.io/aws-load-balancer-ssl-cert: "arn:aws:acm:region:account:certificate/certificate-id"
```

### Admin UI Features

- **Authentication**: Basic authentication with username/password
- **TLS/HTTPS**: Secure communication with TLS certificates
- **Persistence**: Configurable data persistence for admin configuration
- **Auto-discovery**: Automatically discovers masters and filers from the cluster
- **gRPC Support**: Runs both HTTP and gRPC servers for worker connections
- **Resource Management**: Configurable resource limits and requests
- **Service Types**: Support for ClusterIP, LoadBalancer, and NodePort services

### Accessing the Admin UI

Once deployed, you can access the Admin UI through:

1. **LoadBalancer**: If using LoadBalancer service type, get the external IP:
   ```bash
   kubectl get svc seaweed-with-admin-admin
   ```

2. **Port Forward**: For internal access:
   ```bash
   kubectl port-forward svc/seaweed-with-admin-admin 23646:23646
   ```

3. **Ingress**: Configure ingress rules for custom domain access

The Admin UI will be available at `http://<service-ip>:23646` (or `https://` if TLS is enabled).

## Configuration Examples

- Please send us your use-cases / example configs ... this is currently empty (needs to be written)
- For now see: <https://github.com/seaweedfs/seaweedfs-operator/blob/master/config/samples/seaweed_v1_seaweed.yaml>

```yaml
apiVersion: seaweed.seaweedfs.com/v1
kind: Seaweed
metadata:
  name: seaweed1
  namespace: default
spec:
  # Add fields here
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
