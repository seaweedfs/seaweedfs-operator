# IAM Service Support in SeaweedFS Operator

This document describes the IAM (Identity and Access Management) service support that has been added to the SeaweedFS Operator.

## Overview

The SeaweedFS Operator now supports deploying IAM services in two ways:

1. **Standalone IAM Service**: Deploy IAM as a separate service with its own pods
2. **Embedded IAM**: Run IAM service embedded within the filer pods

This addresses [Issue #137](https://github.com/seaweedfs/seaweedfs-operator/issues/137) by providing flexible IAM deployment options that match SeaweedFS's architecture.

## Configuration Options

### Standalone IAM Service

To deploy IAM as a standalone service, configure the `iam` section in your Seaweed CRD:

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
    s3: true

  # Standalone IAM service
  iam:
    replicas: 1
    port: 8111  # Optional: defaults to 8111
    resources:
      requests:
        memory: "64Mi"
        cpu: "50m"
      limits:
        memory: "128Mi"
        cpu: "100m"
    service:
      type: ClusterIP
```

### Embedded IAM Service

To run IAM embedded with the filer, enable the `iam` flag in the filer spec:

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

  # Filer with embedded IAM
  filer:
    replicas: 1
    s3: true
    iam: true  # Enable embedded IAM

  # Optional: Configure IAM port (applies to both standalone and embedded)
  iam:
    port: 8111
```

## API Reference

### IAMSpec

The `IAMSpec` defines the configuration for standalone IAM services:

```go
type IAMSpec struct {
    ComponentSpec               `json:",inline"`
    corev1.ResourceRequirements `json:",inline"`

    // The desired ready replicas
    Replicas int32        `json:"replicas"`
    Service  *ServiceSpec `json:"service,omitempty"`

    // Config in raw toml string
    Config *string `json:"config,omitempty"`

    // MetricsPort is the port that the prometheus metrics export listens on
    MetricsPort *int32 `json:"metricsPort,omitempty"`

    // Port for IAM service (default: 8111)
    Port *int32 `json:"port,omitempty"`
}
```

### FilerSpec Updates

The `FilerSpec` has been updated to support embedded IAM:

```go
type FilerSpec struct {
    // ... existing fields ...

    // Enable IAM service embedded with filer (alternative to standalone IAM)
    IAM bool `json:"iam,omitempty"`
}
```

## Deployment Scenarios

### Scenario 1: Standalone IAM for Multi-Service Architecture

Use standalone IAM when you want to:
- Scale IAM independently from filer
- Share IAM across multiple filer instances
- Have dedicated resources for IAM operations

```yaml
iam:
  replicas: 2  # Scale IAM independently
  port: 8111
  resources:
    requests:
      memory: "128Mi"
      cpu: "100m"
```

### Scenario 2: Embedded IAM for Simplified Deployment

Use embedded IAM when you want to:
- Minimize the number of pods
- Keep IAM close to the filer for performance
- Simplify network configuration

```yaml
filer:
  replicas: 1
  s3: true
  iam: true  # IAM runs in the same pod as filer
```

## Generated Resources

### Standalone IAM

When you configure a standalone IAM service, the operator creates:

- **StatefulSet**: `<seaweed-name>-iam`
- **Service**: `<seaweed-name>-iam`
- **ConfigMap**: `<seaweed-name>-iam` (if config is provided)

### Embedded IAM

When you enable embedded IAM:

- IAM runs within the existing filer pods
- IAM port is exposed through the filer service
- IAM arguments are added to the filer startup command

## Service Discovery

### Standalone IAM

The IAM service is accessible at:
- Internal: `<seaweed-name>-iam.<namespace>.svc.cluster.local:8111`
- Port: 8111 (default) or custom port specified in spec

### Embedded IAM

The IAM service is accessible through the filer service:
- Internal: `<seaweed-name>-filer.<namespace>.svc.cluster.local:8111`
- Port: 8111 (default) or custom port specified in spec

## Networking

### Default Ports

- **IAM HTTP**: 8111
- **Filer HTTP**: 8888
- **Filer S3**: 8333

### Port Configuration

You can customize the IAM port:

```yaml
iam:
  port: 9111  # Custom IAM port
```

This port setting applies to both standalone and embedded IAM services.

## Monitoring

IAM services support metrics export. Configure the metrics port:

```yaml
iam:
  metricsPort: 9090
```

## Migration Guide

### From No IAM to Standalone IAM

1. Add the `iam` section to your Seaweed CRD
2. Apply the updated configuration
3. The operator will create the IAM resources

### From No IAM to Embedded IAM

1. Add `iam: true` to your filer spec
2. Apply the updated configuration
3. The operator will update the filer statefulset to include IAM

### From Embedded to Standalone IAM

1. Set `filer.iam: false`
2. Add the `iam` section for standalone configuration
3. Apply the configuration
4. The operator will create standalone IAM and update filer

## Troubleshooting

### IAM Service Not Starting

Check the following:

1. **Filer Dependency**: IAM requires a running filer service
2. **Master Connection**: Ensure IAM can connect to master servers
3. **Resource Limits**: Verify sufficient CPU/memory allocation

```bash
# Check IAM pod logs
kubectl logs -f <seaweed-name>-iam-0

# Check filer connectivity
kubectl exec -it <seaweed-name>-iam-0 -- weed shell
```

### Port Conflicts

If you encounter port conflicts:

1. Check existing services in the namespace
2. Customize the IAM port in the spec
3. Ensure the port is not used by other services

### Configuration Issues

For configuration problems:

1. Verify the CRD syntax
2. Check operator logs for reconciliation errors
3. Validate resource requirements

## Testing

The IAM implementation includes comprehensive test coverage:

### Running Tests

```bash
# Run IAM-specific API tests
go test -v -run "IAM" ./api/v1

# Run IAM controller unit tests
go test -v -run "TestCreateIAM|TestBuildIAM|TestLabelsForIAM" ./internal/controller

# Run embedded IAM tests
go test -v -run "Filer.*IAM|IAM.*Filer" ./internal/controller

# Run all IAM tests
go test -v -run "IAM" ./...
```

### Test Coverage

The implementation includes:
- **42 test cases** covering all IAM functionality
- Unit tests for StatefulSet and Service creation
- Integration tests for embedded IAM with filer
- API validation tests for CRD specifications
- Error condition and edge case testing

## Examples

Complete examples are available in the `config/samples/` directory:

- `seaweed_v1_seaweed_with_iam_standalone.yaml`: Standalone IAM service
- `seaweed_v1_seaweed_with_iam_embedded.yaml`: Embedded IAM service

### Quick Start Example

Deploy SeaweedFS with standalone IAM:

```bash
kubectl apply -f config/samples/seaweed_v1_seaweed_with_iam_standalone.yaml
```

Verify deployment:

```bash
# Check all resources
kubectl get seaweed,statefulset,service,pod

# Test IAM endpoint
kubectl port-forward svc/seaweed-sample-iam 8111:8111
curl http://localhost:8111/
```

## Architecture Details

### Command Line Integration

#### Standalone IAM
The operator generates the following command for standalone IAM:

```bash
weed -logtostderr=true iam \
  -master=<master-peers> \
  -filer=<filer-service>:8888 \  # Default filer port, configurable via FilerHTTPPort
  -port=8111 \
  -metricsPort=9090
```

#### Embedded IAM
For embedded IAM, the filer command includes:

```bash
weed -logtostderr=true filer \
  -port=8888 \
  -s3 \
  -iam \
  -iam.port=8111 \
  -master=<master-peers>
```

### Resource Management

The operator creates the following Kubernetes resources:

#### Standalone IAM
- `StatefulSet/<name>-iam`: Manages IAM pods
- `Service/<name>-iam`: Exposes IAM HTTP endpoint
- `ConfigMap/<name>-iam`: Configuration (if specified)

#### Embedded IAM
- Modifies existing `StatefulSet/<name>-filer`: Adds IAM flags
- Modifies existing `Service/<name>-filer`: Exposes IAM port

## Security Considerations

1. **Network Policies**: Consider implementing network policies to restrict IAM access
2. **RBAC**: Configure appropriate RBAC for IAM operations
3. **TLS**: Enable TLS for production deployments
4. **Secrets**: Store IAM configuration securely using Kubernetes secrets

## Performance Tuning

### Resource Allocation

For production deployments, consider:

```yaml
iam:
  replicas: 2  # For high availability
  resources:
    requests:
      memory: "256Mi"
      cpu: "200m"
    limits:
      memory: "512Mi"
      cpu: "500m"
```

### Scaling Guidelines

- **Standalone IAM**: Scale based on IAM request volume
- **Embedded IAM**: Scale with filer instances
- **Resource ratio**: Typically 1:4 ratio of IAM to filer resources

## Further Reading

- [SeaweedFS IAM Documentation](https://github.com/seaweedfs/seaweedfs/wiki/IAM)
- [SeaweedFS S3 API Documentation](https://github.com/seaweedfs/seaweedfs/wiki/Amazon-S3-API)
- [Kubernetes Operator Pattern](https://kubernetes.io/docs/concepts/extend-kubernetes/operator/)
- [Original Issue #137](https://github.com/seaweedfs/seaweedfs-operator/issues/137)
