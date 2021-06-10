[![Build Status](https://travis-ci.com/seaweedfs/seaweedfs-operator.svg?branch=master)](https://travis-ci.com/github/seaweedfs/seaweedfs-operator)

# SeaweedFS Operator

This [Kubernetes Operator](https://kubernetes.io/docs/concepts/extend-kubernetes/operator/) is made to easily deploy SeaweedFS onto your Kubernetes-Cluster.

The difference to [seaweedfs-csi-driver](https://github.com/seaweedfs/seaweedfs-csi-driver) is that the infrastructure (SeaweedFS) itself runs on Kubernetes as well (Master, Filer, Volume-Servers) and can as such easily scale with it as you need. It is also by far more resilent to failures then a simple systemD service in regards to handling crashing services or accidental deletes.

By using `make deploy` it will deploy a Resource of type 'Seaweed' onto your current kubectl $KUBECONFIG target (the operator itself) which by default will do nothing unless you configurate it (see examples in config/samples/).

Goals: 
- [x] Automatically deploy a SeaweedFS cluster with 3 masters, N volume servers, and M filers with customizable filer store managed by other operators.
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
$ kubectl get Seaweed --all-namespaces
```

Which should return:
```bash
NAMESPACE   NAME      AGE
seaweed     seaweed   1h
```

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
