# IAM Service Support in SeaweedFS Operator

This document describes the IAM (Identity and Access Management) service support in the SeaweedFS Operator.

## Overview

Starting from SeaweedFS version X.X.X, the IAM API is now **embedded in the S3 server by default**. This follows the pattern used by MinIO and Ceph RGW, providing a simpler deployment model where both S3 and IAM APIs are available on the same port.

### Key Changes

- **IAM is embedded in S3 by default**: When S3 is enabled, IAM API is automatically available on the same port (8333)
- **Standalone IAM is deprecated**: The separate `weed iam` command and standalone IAM StatefulSet are deprecated
- **Simplified deployment**: Single port for both S3 and IAM operations

## Configuration Options

### Embedded IAM (Recommended)

IAM is enabled by default when S3 is enabled. No additional configuration needed:

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
    # iam: true  # Optional - IAM is enabled by default when S3 is enabled
```

The IAM API is accessible on the same port as S3 (8333).

### Disabling Embedded IAM

To disable embedded IAM (if you want S3-only without IAM):

```yaml
filer:
  replicas: 1
  s3:
    enabled: true
  iam: false  # Explicitly disable embedded IAM
```

### Standalone IAM (Deprecated)

> ⚠️ **DEPRECATED**: Standalone IAM is deprecated and will be removed in a future release.
> Please migrate to embedded IAM (see above).

For backward compatibility, you can still deploy standalone IAM:

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
    iam: false  # Disable embedded IAM when using standalone

  # DEPRECATED: Standalone IAM service
  iam:
    replicas: 1
    port: 8111
```

## API Reference

### FilerSpec.IAM

```go
// IAM enables/disables IAM API embedded in S3 server.
// When S3 is enabled, IAM is enabled by default (on the same S3 port).
// Set to false to explicitly disable embedded IAM.
// +kubebuilder:default:=true
IAM bool `json:"iam,omitempty"`
```

### IAMSpec (Deprecated)

The standalone IAM configuration is deprecated. Use embedded IAM instead.

## Service Discovery

### Embedded IAM (Recommended)

The IAM API is accessible through the filer S3 service:
- **Internal**: `<seaweed-name>-filer.<namespace>.svc.cluster.local:8333`
- **Port**: 8333 (same as S3)

### Standalone IAM (Deprecated)

- **Internal**: `<seaweed-name>-iam.<namespace>.svc.cluster.local:8111`
- **Port**: 8111

## Migration Guide

### From Standalone IAM to Embedded IAM

1. Remove the `iam:` section from your Seaweed CRD
2. Ensure `filer.s3.enabled: true`
3. Optionally set `filer.iam: true` (this is the default when S3 is enabled)
4. Apply the updated configuration
5. Update your clients to use the S3 port (8333) for IAM operations

**Before (deprecated):**
```yaml
filer:
  replicas: 1
  s3:
    enabled: true
iam:
  replicas: 1
  port: 8111
```

**After (recommended):**
```yaml
filer:
  replicas: 1
  s3:
    enabled: true
  # iam: true  # Default when S3 is enabled
```

## Examples

Complete examples are available in the `config/samples/` directory:

- `seaweed_v1_seaweed_with_iam_embedded.yaml`: Embedded IAM (recommended)
- `seaweed_v1_seaweed_with_iam_standalone.yaml`: Standalone IAM (deprecated)

### Quick Start

Deploy SeaweedFS with embedded IAM (recommended):

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

### Embedded IAM

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

### Standalone IAM (Deprecated)

```
┌──────────────────────┐    ┌──────────────────────┐
│     Filer Pod        │    │      IAM Pod         │
│  ┌────────────────┐  │    │  ┌────────────────┐  │
│  │ weed filer -s3 │  │    │  │   weed iam     │  │
│  │   port 8333    │  │    │  │   port 8111    │  │
│  └────────────────┘  │    │  └────────────────┘  │
└──────────────────────┘    └──────────────────────┘
```

## Benefits of Embedded IAM

1. **Simpler deployment**: Single service, single port
2. **Reduced resource usage**: No separate IAM pods
3. **Consistent with industry standards**: Matches MinIO and Ceph RGW patterns
4. **Automatic scaling**: IAM scales with S3/filer instances

## Troubleshooting

### IAM Requests Returning 404

Ensure S3 is enabled:
```yaml
filer:
  s3:
    enabled: true
```

### IAM Disabled When Expected

Check if `filer.iam` is explicitly set to `false`. Remove or set to `true`.

### Standalone IAM Deprecation Warning

If you see deprecation warnings in operator logs, migrate to embedded IAM by:
1. Removing the `iam:` section
2. Ensuring S3 is enabled

## Further Reading

- [SeaweedFS IAM Documentation](https://github.com/seaweedfs/seaweedfs/wiki/IAM)
- [SeaweedFS S3 API Documentation](https://github.com/seaweedfs/seaweedfs/wiki/Amazon-S3-API)
- [MinIO IAM](https://min.io/product/identity-and-access-management)
- [Ceph RGW IAM](https://docs.ceph.com/en/latest/radosgw/)
