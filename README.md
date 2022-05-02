[![Build Status](https://travis-ci.com/seaweedfs/seaweedfs-operator.svg?branch=master)](https://travis-ci.com/github/seaweedfs/seaweedfs-operator)

# SeaweedFS Operator

This [Kubernetes Operator](https://kubernetes.io/docs/concepts/extend-kubernetes/operator/) is made to easily deploy SeaweedFS onto your Kubernetes-Cluster.

The difference to [seaweedfs-csi-driver](https://github.com/seaweedfs/seaweedfs-csi-driver) is that the infrastructure (SeaweedFS) itself runs on Kubernetes as well (Master, Filer, Volume-Servers) and can as such easily scale with it as you need. It is also by far more resilent to failures then a simple systemD service in regards to handling crashing services or accidental deletes.

By using `make deploy` it will deploy a Resource of type 'Seaweed' onto your current kubectl $KUBECONFIG target (the operator itself) which by default will do nothing unless you configurate it (see examples in config/samples/).

Goals: 
- [x] Automatically deploy and manage a SeaweedFS cluster.
- [x] Ability to be managed by other Operators.
- [ ] Compability with [seaweedfs-csi-driver](https://github.com/seaweedfs/seaweedfs-csi-driver)
- [x] Auto rolling upgrade and restart.
- [x] Ingress for volume server, filer and S3, to support HDFS, REST filer, S3 API and cross-cluster replication.
- [ ] Support all major cloud Kubernetes: AWS, Google, Azure.
- [ ] Scheduled backup to cloud storage: S3, Google Cloud Storage , Azure.
- [ ] Put warm data to cloud storage tier: S3, Google Cloud Storage , Azure.
- [ ] Grafana dashboard.

## Installation

This operator uses `kustomize` to deploy. The installation process will install one for you if you do not have one.

By default, the defaulting and validation webhooks are disabled. We strongly recommend that the webhooks be enabled.

First clone the repository:

```bash
$ git clone https://github.com/seaweedfs/seaweedfs-operator --depth=1
```

To deploy the operator with webhooks enabled, make sure you have installed the `cert-manager`(Installation docs: https://cert-manager.io/docs/installation/) in your cluster, then follow the instructions in the `config/default/kustomization.yaml` file to uncomment the components you need.

Lastly, change the value of `ENABLE_WEBHOOKS` to `"true"` in `config/manager/manager.yaml`

Afterwards fire up:
```bash
$ make install
```

Then run the command to deploy the operator into your cluster:

```bash
$ make deploy
```

Verify if it was correctly deployed with:
```bash
$ kubectl get pods --all-namespaces
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

See the next section for example usage - **__at this point you only deployed the Operator itself!__**

### You need to also deploy an configuration to get it running (see next section)!


## Configuration Examples 

- Please send us your use-cases / example configs ... this is currently empty (needs to be written)
- For now see: https://github.com/seaweedfs/seaweedfs-operator/blob/master/config/samples/seaweed_v1_seaweed.yaml
````
apiVersion: seaweed.seaweedfs.com/v1
kind: Seaweed
metadata:
  name: seaweed1
  namespace: default
spec:
  # Add fields here
  image: chrislusf/seaweedfs:3.01
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
  ````


## Maintenance and Uninstallation
- TBD

## Development

Follow the instructions in https://sdk.operatorframework.io/docs/building-operators/golang/quickstart/

```
$ git clone https://github.com/seaweedfs/seaweedfs-operator
$ cd seaweedfs-operator

# register the CRD with the Kubernetes
$ make deploy

# build the operator image
$ make docker-build

# load the image into Kind cluster
$ kind load docker-image chrislusf/seaweedfs-operator:v0.0.1

# From another terminal in the same directory
$ kubectl apply -f config/samples/seaweed_v1_seaweed.yaml

```

### Update the operator
```
# delete the existing operator
$ kubectl delete namespace seaweedfs-operator-system

# rebuild the operator image
$ make docker-build

# load the image into Kind cluster
$ kind load docker-image chrislusf/seaweedfs-operator:v0.0.1

# register the CRD with the Kubernetes
$ make deploy

```

### develop outside of k8s

```
$ git clone https://github.com/seaweedfs/seaweedfs-operator
$ cd seaweedfs-operator

# register the CRD with the Kubernetes
$ make install

# run the operator locally outside the Kubernetes cluster
$ make run ENABLE_WEBHOOKS=false 

# From another terminal in the same directory
$ kubectl apply -f config/samples/seaweed_v1_seaweed.yaml
```
