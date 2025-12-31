# IAM Service Support in SeaweedFS Operator

This document describes the IAM (Identity and Access Management) service support in the SeaweedFS Operator.

## Overview

Starting from SeaweedFS version **4.03**, the IAM API is now **embedded in the S3 server by default**. This follows the pattern used by MinIO and Ceph RGW, providing a simpler deployment model where both S3 and IAM APIs are available on the same port (8333).

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

## OIDC Configuration

The IAM service supports OIDC (OpenID Connect) authentication. Configuration is provided via ConfigMap or Secret.

### Creating IAM Configuration

Create a ConfigMap with your IAM configuration:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: seaweed-iam-config
data:
  iam.json: |
    {
      "sts": {
        "tokenDuration": "1h",
        "maxSessionLength": "12h",
        "issuer": "seaweedfs-sts",
        "signingKey": "your-base64-encoded-signing-key"
      },
      "providers": [
        {
          "name": "keycloak",
          "type": "oidc",
          "enabled": true,
          "config": {
            "issuer": "https://keycloak.example.com/realms/seaweedfs",
            "clientId": "seaweedfs-s3",
            "clientSecret": "optional-secret",
            "jwksUri": "https://keycloak.example.com/realms/seaweedfs/protocol/openid-connect/certs",
            "tlsCaCert": "/etc/seaweedfs/certs/ca.pem",
            "tlsInsecureSkipVerify": false,
            "roleMapping": {
              "rules": [
                { "claim": "groups", "value": "admins", "role": "arn:aws:iam::role/S3AdminRole" }
              ],
              "defaultRole": "arn:aws:iam::role/S3ReadOnlyRole"
            }
          }
        }
      ],
      "policies": [...],
      "roles": [...]
    }
```

### TLS Configuration Options

| Field | Description |
|-------|-------------|
| `tlsCaCert` | Path to CA certificate file for custom/self-signed certificates |
| `tlsInsecureSkipVerify` | Skip TLS verification (development only, never use in production) |

### Mounting CA Certificates

If using custom CA certificates, create a Secret and mount it in the filer pods:

```yaml
# Create secret with CA certificate
apiVersion: v1
kind: Secret
metadata:
  name: oidc-ca-cert
type: Opaque
data:
  ca.pem: <base64-encoded-ca-cert>
---
# Mount in filer pod (add to Seaweed CRD)
apiVersion: seaweed.seaweedfs.com/v1
kind: Seaweed
metadata:
  name: seaweed-sample
spec:
  filer:
    s3:
      enabled: true
    volumeMounts:
      - name: oidc-ca
        mountPath: /etc/seaweedfs/certs
    volumes:
      - name: oidc-ca
        secret:
          secretName: oidc-ca-cert
```

### Applying IAM Configuration

Mount the ConfigMap to provide IAM configuration to the filer:

```yaml
apiVersion: seaweed.seaweedfs.com/v1
kind: Seaweed
metadata:
  name: seaweed-sample
spec:
  filer:
    s3:
      enabled: true
    volumeMounts:
      - name: iam-config
        mountPath: /etc/seaweedfs/iam
    volumes:
      - name: iam-config
        configMap:
          name: seaweed-iam-config
    # Add -iam.config flag to S3 command
    s3Args:
      - "-iam.config=/etc/seaweedfs/iam/iam.json"
```

For more details on OIDC configuration, see the [SeaweedFS OIDC Integration Wiki](https://github.com/seaweedfs/seaweedfs/wiki/OIDC-Integration).

## Further Reading

- [SeaweedFS IAM Documentation](https://github.com/seaweedfs/seaweedfs/wiki/IAM)
- [SeaweedFS S3 API Documentation](https://github.com/seaweedfs/seaweedfs/wiki/Amazon-S3-API)
