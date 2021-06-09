[![Build Status](https://travis-ci.org/seaweedfs/seaweedfs-operator.svg?branch=master)](https://travis-ci.com/github/seaweedfs/seaweedfs-operator)

# SeaweedFS Operator

Goals: 
* Automatically deploy a SeaweedFS cluster with 3 masters, N volume servers, and M filers with customizable filer store managed by other operators.
* Auto rolling upgrade and restart.
* Ingress for volume server, filer and S3, to support HDFS, REST filer, S3 API and cross-cluster replication.
* Support all major cloud Kubernetes: AWS, Google, Azure.
* Scheduled backup to cloud storage: S3, Google Cloud Storage , Azure.
* Put warm data to cloud storage tier: S3, Google Cloud Storage , Azure.
* Grafana dashboard.

## Installation

This operator uses `kustomize` to deploy. The installation process will install one for you if you do not have one.

By default, the defaulting and validation webhooks are disabled. We strongly recommend that the webhooks be enabled.

First clone the repository:

```bash
$ git clone https://github.com/seaweedfs/seaweedfs-operator --depth=1
```

To deploy the operator with webhooks enabled, make sure you have installed the `cert-manager`(Installation docs: https://cert-manager.io/docs/installation/) in your cluster, then follow the instructions in the `config/default/kustomization.yaml` file to uncomment the components you need.

Lastly, change the value of `ENABLE_WEBHOOKS` to `"true"` in `config/manager/manager.yaml`

Then run the command to deploy the operator into your cluster:

```
$ make deploy
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
