# Integration Testing

This document describes how to run integration tests for the SeaweedFS Operator, particularly the resource requirements testing that ensures the fixes for [Issue #131](https://github.com/seaweedfs/seaweedfs-operator/issues/131) and [Issue #132](https://github.com/seaweedfs/seaweedfs-operator/issues/132) work correctly.

## Test Overview

The integration tests verify that:

1. **Resource requirements are properly applied** to all container specs (master, volume, filer)
2. **Storage resources are correctly filtered** out of container specs but used for PVC sizing
3. **The operator deploys successfully** in a real Kubernetes environment
4. **Resource requests/limits work in constrained environments** like GKE Autopilot

## Running Tests Locally

### Prerequisites

- Go 1.22+
- Docker
- kubectl
- Make

### Quick Test Run

```bash
# Run just the unit tests for resource filtering
make test

# Run the specific resource filtering tests
go test ./internal/controller/ -run TestFilterContainerResources -v
```

### Full Integration Test with Kind

```bash
# Create a Kind cluster and run all e2e tests
make test-e2e

# Or run manually with specific Kubernetes version
K8S_VERSION=v1.30.0 make kind-prepare
make docker-build kind-load deploy
go test ./test/e2e/ -v -ginkgo.v -timeout 20m
```

### Step-by-Step Manual Testing

1. **Set up Kind cluster:**
   ```bash
   make kind-prepare
   ```

2. **Build and load operator image:**
   ```bash
   make docker-build kind-load
   ```

3. **Deploy the operator:**
   ```bash
   make deploy
   kubectl wait deployment.apps/seaweedfs-operator-controller-manager \
     --for condition=Available \
     --namespace seaweedfs-operator-system \
     --timeout 5m
   ```

4. **Run integration tests:**
   ```bash
   go test ./test/e2e/ -v -ginkgo.v -ginkgo.progress
   ```

5. **Clean up:**
   ```bash
   make undeploy kind-delete
   ```

## Test Structure

### Unit Tests (`internal/controller/helper_test.go`)

- `TestFilterContainerResources`: Verifies the `filterContainerResources()` function correctly removes storage resources while preserving other resources
- `TestFilterContainerResourcesEmpty`: Tests edge cases with empty resource specifications

### Integration Tests (`test/e2e/resource_integration_test.go`)

The integration tests create actual Seaweed resources with comprehensive resource specifications and verify:

#### Master Container Resources
- CPU requests/limits are applied correctly
- Memory requests/limits are applied correctly
- No storage resources leak into container specs

#### Volume Container Resources
- CPU, memory, and ephemeral-storage resources are applied
- **Critical**: `storage` resources are filtered out of container specs
- Storage resources are used for PVC templates in StatefulSets

#### Filer Container Resources
- CPU and memory resources are applied correctly
- No unintended resource types are included

### GitHub Actions Workflow

The `.github/workflows/integration-test.yml` workflow runs automatically on:

- Pull requests (with proper labels or from maintainers)
- Pushes to main/master branches

The workflow includes:
- **Multi-version testing**: Kubernetes v1.29, v1.30, v1.31
- **Resource validation**: Specific tests for storage filtering
- **Build verification**: Ensures code compiles and Docker images build
- **Comprehensive logging**: Collects operator logs, pod status, and events on failure

## Key Test Scenarios

### Resource Filtering Test
```yaml
volume:
  requests:
    cpu: "250m"
    memory: "512Mi"
    storage: "10Gi"           # Should NOT appear in container
    ephemeral-storage: "1Gi"  # Should appear in container
```

**Expected Result:**
- Container spec contains: `cpu`, `memory`, `ephemeral-storage`
- Container spec does NOT contain: `storage`
- PVC template contains: `storage: "10Gi"`

### GKE Autopilot Compatibility
The tests ensure that resource specifications work correctly in constrained environments like GKE Autopilot, where:
- Missing resource requests cause pod failures
- Invalid resource types (like `storage` in containers) are rejected

## Troubleshooting

### Test Failures

1. **Check operator logs:**
   ```bash
   kubectl logs -n seaweedfs-operator-system deployment/seaweedfs-operator-controller-manager
   ```

2. **Verify StatefulSet creation:**
   ```bash
   kubectl get statefulsets --all-namespaces
   kubectl describe statefulset -n test-resources test-seaweed-resources-volume
   ```

3. **Check resource specifications:**
   ```bash
   kubectl get statefulset test-seaweed-resources-volume -o yaml | grep -A 20 resources:
   ```

### Common Issues

- **Kind cluster not starting**: Check Docker is running and has sufficient resources
- **Image pull failures**: Ensure `make kind-load` completed successfully
- **Timeout errors**: Increase test timeouts or check cluster resources

## Contributing

When modifying resource handling:

1. **Update unit tests** in `helper_test.go` for new resource types
2. **Extend integration tests** in `resource_integration_test.go` for new scenarios
3. **Test locally** with `make test-e2e` before submitting PRs
4. **Verify multi-version compatibility** by testing with different Kubernetes versions

The integration tests serve as both verification of fixes and regression prevention for future changes.
