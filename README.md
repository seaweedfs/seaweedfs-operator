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
$ kind load docker-image seaweedfs/operator:v0.0.1

# From another terminal in the same directory
$ kubectl apply -f config/samples/seaweed_v1_seaweed.yaml

```
