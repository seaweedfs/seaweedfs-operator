module github.com/seaweedfs/seaweedfs-operator

go 1.13

require (
	github.com/chrislusf/seaweedfs v0.0.0-20210110065522-f0d3b3bf9397
	github.com/go-logr/logr v0.1.0
	github.com/go-redis/redis v6.15.7+incompatible // indirect
	github.com/onsi/ginkgo v1.14.2
	github.com/onsi/gomega v1.10.4
	google.golang.org/grpc v1.29.1
	k8s.io/api v0.18.2
	k8s.io/apimachinery v0.18.2
	k8s.io/client-go v0.18.2
	k8s.io/klog v1.0.0
	sigs.k8s.io/controller-runtime v0.6.0
)
