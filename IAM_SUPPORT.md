# IAM Service Support in SeaweedFS Operator

This document describes the IAM (Identity and Access Management) service support in the SeaweedFS Operator.

## Overview

The IAM API is now **embedded in the S3 server by default**. This follows the pattern used by MinIO and Ceph RGW, providing a simpler deployment model where both S3 and IAM APIs are available on the same port (8333).

## Configuration

### Enabling S3 with Embedded IAM (Default)

When you enable S3, IAM is automatically enabled on the same port:

```yaml
apiVersion: seaweed.seaweedfs.com/v1
kind: Seaweed
metadata:
  name: seaweed-sample
spec:
  image: chrislusf/seaweedfs:latest

  master:
    replicas: 1
  volume:
    replicas: 1
  filer:
    replicas: 1
    s3:
      enabled: true
    # iam: true  # Default - IAM is enabled when S3 is enabled
```

The IAM API is accessible on the same port as S3 (8333).

### Disabling Embedded IAM

To run S3 without IAM:

```yaml
filer:
  replicas: 1
  s3:
    enabled: true
  iam: false  # Explicitly disable embedded IAM
```

## API Reference

### FilerSpec.IAM

```go
// IAM enables/disables IAM API embedded in S3 server.
// When S3 is enabled, IAM is enabled by default (on the same S3 port: 8333).
// Set to false to explicitly disable embedded IAM.
// +kubebuilder:default:=true
IAM bool `json:"iam,omitempty"`
```

## Service Discovery

The IAM API is accessible through the filer S3 service:
- **Internal**: `<seaweed-name>-filer.<namespace>.svc.cluster.local:8333`
- **Port**: 8333 (same as S3)

## Examples

Complete examples are available in the `config/samples/` directory:

- `seaweed_v1_seaweed_with_iam_embedded.yaml`: Embedded IAM configuration

### Quick Start

Deploy SeaweedFS with S3 and embedded IAM:

```bash
kubectl apply -f config/samples/seaweed_v1_seaweed_with_iam_embedded.yaml
```

Verify deployment:

```bash
# Check all resources
kubectl get seaweed,statefulset,service,pod

# Test S3/IAM endpoint (both on same port)
kubectl port-forward svc/seaweed-sample-filer 8333:8333

# S3 operations
aws --endpoint-url http://localhost:8333 s3 ls

# IAM operations (same endpoint)
aws --endpoint-url http://localhost:8333 iam list-users
```

## Architecture

```
┌─────────────────────────────────────────────┐
│                Filer Pod                     │
│  ┌────────────────────────────────────────┐ │
│  │           weed filer -s3               │ │
│  │  ┌──────────────────────────────────┐  │ │
│  │  │  S3 API Server (port 8333)       │  │ │
│  │  │  ├── S3 Operations (GET/PUT/...)  │  │ │
│  │  │  └── IAM Operations (POST /)      │  │ │
│  │  └──────────────────────────────────┘  │ │
│  └────────────────────────────────────────┘ │
└─────────────────────────────────────────────┘
```

## Benefits

1. **Simple deployment**: Single service, single port
2. **Reduced resource usage**: No separate IAM pods
3. **Industry standard**: Matches MinIO and Ceph RGW patterns
4. **Automatic scaling**: IAM scales with S3/filer instances

## Migration from Standalone IAM

If you were previously using standalone IAM, simply:

1. Remove the `iam:` section from your Seaweed CRD
2. Ensure `filer.s3.enabled: true`
3. Update clients to use the S3 port (8333) for IAM operations

## Further Reading

- [SeaweedFS IAM Documentation](https://github.com/seaweedfs/seaweedfs/wiki/IAM)
- [SeaweedFS S3 API Documentation](https://github.com/seaweedfs/seaweedfs/wiki/Amazon-S3-API)
