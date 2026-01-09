# IAM Service Support in SeaweedFS Operator

This document describes the IAM (Identity and Access Management) service support in the SeaweedFS Operator.

## Overview

The IAM API is **embedded in the S3 server** (which runs within the filer pod) and is available on the same port (8333) as the S3 API. This provides a simplified deployment model where both APIs are served together.

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

### Disabling IAM

To run S3 without IAM, set `iam: false` in the filer spec:

```yaml
filer:
  replicas: 1
  s3:
    enabled: true
  iam: false  # Disable IAM (IAM is enabled by default when S3 is enabled)
```

## API Reference

### FilerSpec.IAM

```go
// IAM enables/disables IAM API embedded in S3 server.
// When S3 is enabled, IAM is enabled by default (on the same S3 port: 8333).
// Set to false to explicitly disable IAM.
// +kubebuilder:default:=true
IAM bool `json:"iam,omitempty"`
```

## Service Discovery

The IAM API is accessible through the filer's S3 service:
- **Internal**: `<seaweed-name>-filer.<namespace>.svc.cluster.local:8333`
- **Port**: 8333 (S3 port - IAM API is embedded in the S3 server)

## Networking

### Default Ports

- **Filer HTTP**: 8888
- **Filer S3 + IAM**: 8333 (both APIs on same port)
- **Master HTTP**: 9333
- **Volume HTTP**: 8444

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

### Embedded IAM Architecture

```
┌─────────────────────────────────────────────┐
│            Filer Pod                         │
│  ┌────────────────────────────────────────┐ │
│  │     weed filer -s3 (default: -iam)     │ │
│  │  ┌──────────────────────────────────┐  │ │
│  │  │  S3 Server (port 8333)           │  │ │
│  │  │  ├── S3 API (GET/PUT/DELETE)      │  │ │
│  │  │  └── IAM API (embedded)           │  │ │
│  │  └──────────────────────────────────┘  │ │
│  └────────────────────────────────────────┘ │
└─────────────────────────────────────────────┘
```

### Command Line Integration

For embedded IAM, the filer command includes the `-s3` flag, which enables both S3 and IAM by default:

```bash
weed -logtostderr=true filer \
  -port=8888 \
  -s3 \
  -s3.port=8333 \
  -master=<master-peers>
# IAM is enabled by default when -s3 is present
# To disable: add -iam=false
```

## Benefits

1. **Simple deployment**: Single service, single port
2. **Reduced resource usage**: No separate IAM pods
3. **Automatic scaling**: IAM scales with S3/filer instances
4. **Simplified networking**: One endpoint for both S3 and IAM operations

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

If using custom CA certificates, create a Secret and mount it:

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
# Mount in Filer pod (add to Seaweed CRD)
apiVersion: seaweed.seaweedfs.com/v1
kind: Seaweed
metadata:
  name: seaweed-sample
spec:
  filer:
    replicas: 1
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

Mount the ConfigMap to provide IAM configuration:

```yaml
apiVersion: seaweed.seaweedfs.com/v1
kind: Seaweed
metadata:
  name: seaweed-sample
spec:
  filer:
    replicas: 1
    s3:
      enabled: true
    volumeMounts:
      - name: iam-config
        mountPath: /etc/seaweedfs/iam
    volumes:
      - name: iam-config
        configMap:
          name: seaweed-iam-config
    # Add -iam.config flag to command
    extraArgs:
      - "-iam.config=/etc/seaweedfs/iam/iam.json"
```

For more details on OIDC configuration, see the [SeaweedFS OIDC Integration Wiki](https://github.com/seaweedfs/seaweedfs/wiki/OIDC-Integration).

## Security Considerations

1. **Network Policies**: Consider implementing network policies to restrict IAM/S3 access
2. **RBAC**: Configure appropriate RBAC for IAM operations
3. **TLS**: Enable TLS for production deployments
4. **Secrets**: Store IAM configuration securely using Kubernetes secrets
5. **Authentication**: Always use proper authentication mechanisms in production

## Troubleshooting

### IAM API Not Responding

Check the following:

1. **S3 Enabled**: IAM requires S3 to be enabled (`filer.s3.enabled: true`)
2. **IAM Not Disabled**: Ensure `filer.iam` is not explicitly set to `false`
3. **Port Access**: Verify you're accessing port 8333 (S3 port, not filer HTTP port 8888)

```bash
# Check filer pod logs
kubectl logs -f <seaweed-name>-filer-0

# Verify S3 service is running
kubectl get svc <seaweed-name>-filer -o yaml

# Test IAM endpoint
kubectl port-forward svc/<seaweed-name>-filer 8333:8333
curl http://localhost:8333/
```

### Configuration Issues

For configuration problems:

1. Verify the CRD syntax
2. Check operator logs for reconciliation errors
3. Validate S3 configuration is correct

```bash
# Check operator logs
kubectl logs -n <operator-namespace> deployment/seaweedfs-operator-controller-manager

# Verify Seaweed resource status
kubectl describe seaweed <seaweed-name>
```

## Performance Tuning

### Resource Allocation

For production deployments with IAM, consider:

```yaml
filer:
  replicas: 2  # For high availability
  s3:
    enabled: true
  resources:
    requests:
      memory: "512Mi"
      cpu: "500m"
    limits:
      memory: "1Gi"
      cpu: "1000m"
```

### Scaling Guidelines

- **Embedded IAM**: Scales automatically with filer instances
- **High availability**: Deploy at least 2 filer replicas
- **Resource allocation**: IAM adds minimal overhead to S3 server



## Testing

### Manual Testing

```bash
# Port forward to filer S3 service
kubectl port-forward svc/seaweed-sample-filer 8333:8333

# Test S3 operations
aws --endpoint-url http://localhost:8333 s3 mb s3://test-bucket
aws --endpoint-url http://localhost:8333 s3 ls

# Test IAM operations (same endpoint)
aws --endpoint-url http://localhost:8333 iam list-users
aws --endpoint-url http://localhost:8333 iam create-user --user-name testuser
```

### Integration Testing

The operator includes tests for embedded IAM functionality. To run them:

```bash
# Run all tests
make test

# Run specific IAM-related tests
go test -v -run "Filer.*IAM|IAM.*Filer" ./internal/controller
```

## Further Reading

- [SeaweedFS IAM Documentation](https://github.com/seaweedfs/seaweedfs/wiki/IAM)
- [SeaweedFS S3 API Documentation](https://github.com/seaweedfs/seaweedfs/wiki/Amazon-S3-API)
- [Kubernetes Operator Pattern](https://kubernetes.io/docs/concepts/extend-kubernetes/operator/)
- [Original Issue #137](https://github.com/seaweedfs/seaweedfs-operator/issues/137)
- [SeaweedFS OIDC Integration](https://github.com/seaweedfs/seaweedfs/wiki/OIDC-Integration)
