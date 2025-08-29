# Topology Support in SeaweedFS Operator

This document describes how to configure topology information (rack and datacenter) for SeaweedFS volume servers using the SeaweedFS Operator.

## Overview

SeaweedFS supports topology-aware placement of data across different racks and datacenters to ensure high availability and fault tolerance. This is especially important for replication strategies like "210" (2 copies in different datacenters, 1 copy in different rack, 0 copies in same rack).

The SeaweedFS Operator provides two approaches for topology configuration:

1. **Simple Topology**: Basic rack/datacenter configuration for a single volume server group
2. **Tree Topology**: Hierarchical configuration allowing multiple volume server groups across different topology zones

## Configuration

### Volume Server Topology

You can specify topology information for volume servers by adding `rack` and `dataCenter` fields to the volume specification:

```yaml
apiVersion: seaweed.seaweedfs.com/v1
kind: Seaweed
metadata:
  name: seaweed-with-topology
spec:
  image: chrislusf/seaweedfs:latest
  volume:
    replicas: 3
    requests:
      storage: 10Gi
    # Topology configuration
    rack: "rack1"
    dataCenter: "dc1"
```

### Tree Topology Configuration (Recommended)

The tree topology approach allows you to define multiple volume server groups, each with their own topology configuration. This provides more flexibility and better organization for complex multi-datacenter deployments.

```yaml
apiVersion: seaweed.seaweedfs.com/v1
kind: Seaweed
metadata:
  name: seaweed-tree-topology
spec:
  image: chrislusf/seaweedfs:latest
  master:
    replicas: 3
    defaultReplication: "210"

  # Define multiple topology groups
  volumeTopology:
    # Datacenter 1, Rack 1
    dc1-rack1:
      replicas: 3
      rack: "rack1"
      dataCenter: "dc1"
      nodeSelector:
        topology.kubernetes.io/zone: "us-west-2a"
        seaweedfs/datacenter: "dc1"
        seaweedfs/rack: "rack1"
      requests:
        storage: 10Gi
        cpu: "1"
        memory: "2Gi"
      limits:
        cpu: "2"
        memory: "4Gi"

    # Datacenter 1, Rack 2
    dc1-rack2:
      replicas: 2
      rack: "rack2"
      dataCenter: "dc1"
      nodeSelector:
        topology.kubernetes.io/zone: "us-west-2b"
        seaweedfs/datacenter: "dc1"
        seaweedfs/rack: "rack2"
      requests:
        storage: 10Gi

    # Datacenter 2, Rack 1
    dc2-rack1:
      replicas: 3
      rack: "rack1"
      dataCenter: "dc2"
      nodeSelector:
        topology.kubernetes.io/zone: "us-east-1a"
        seaweedfs/datacenter: "dc2"
        seaweedfs/rack: "rack1"
      requests:
        storage: 15Gi
      storageClassName: "fast-ssd"
      compactionMBps: 100

    # Datacenter 2, Rack 2
    dc2-rack2:
      replicas: 2
      rack: "rack2"
      dataCenter: "dc2"
      nodeSelector:
        topology.kubernetes.io/zone: "us-east-1b"
        seaweedfs/datacenter: "dc2"
        seaweedfs/rack: "rack2"
      requests:
        storage: 15Gi
      storageClassName: "fast-ssd"
```

### Complete Example for 210 Replication (Legacy Approach)

For 210 replication using the legacy single-instance approach (2 copies in different datacenters, 1 copy in different rack, 0 copies in same rack), you need volume servers in multiple racks and datacenters:

```yaml
apiVersion: seaweed.seaweedfs.com/v1
kind: Seaweed
metadata:
  name: seaweed-210-replication
spec:
  image: chrislusf/seaweedfs:latest
  master:
    replicas: 3
    # Enable 210 replication
    defaultReplication: "210"
  volume:
    replicas: 6
    requests:
      storage: 10Gi
    rack: "rack1"
    dataCenter: "dc1"
    # Use node selectors to ensure proper placement
    nodeSelector:
      topology.kubernetes.io/zone: "dc1-rack1"
---
# Additional volume servers in different rack/datacenter
apiVersion: seaweed.seaweedfs.com/v1
kind: Seaweed
metadata:
  name: seaweed-dc1-rack2
spec:
  image: chrislusf/seaweedfs:latest
  # Only deploy volume servers
  master:
    replicas: 0
  volume:
    replicas: 3
    requests:
      storage: 10Gi
    rack: "rack2"
    dataCenter: "dc1"
    nodeSelector:
      topology.kubernetes.io/zone: "dc1-rack2"
  filer:
    replicas: 0
---
apiVersion: seaweed.seaweedfs.com/v1
kind: Seaweed
metadata:
  name: seaweed-dc2-rack1
spec:
  image: chrislusf/seaweedfs:latest
  # Only deploy volume servers
  master:
    replicas: 0
  volume:
    replicas: 3
    requests:
      storage: 10Gi
    rack: "rack1"
    dataCenter: "dc2"
    nodeSelector:
      topology.kubernetes.io/zone: "dc2-rack1"
  filer:
    replicas: 0
```

## Fields

### Volume Spec Fields

#### Simple Topology Fields

Since `VolumeSpec` embeds `VolumeServerConfig`, the simple topology supports all the same configuration options as individual topology groups, plus topology-specific fields:

**Required Fields:**
- `replicas` (int32, required): Number of volume server replicas

**Topology Placement (Simple Topology Specific):**
- `rack` (string, optional): The rack name for volume servers
- `dataCenter` (string, optional): The datacenter name for volume servers

**Resource Configuration:**
- `requests` (ResourceList): Resource requests (CPU, memory, storage) for volume servers
- `limits` (ResourceList): Resource limits (CPU, memory) for containers
- `storageClassName` (string, optional): Storage class for PVCs

**Kubernetes Pod Placement:**
- `nodeSelector` (map[string]string): Node selector to ensure pods are scheduled on appropriate nodes
- `tolerations` ([]corev1.Toleration): Tolerations for pod scheduling
- `affinity` (corev1.Affinity): Affinity rules for pod placement

**Volume Server Configuration:**
- `compactionMBps` (int32, optional): Background compaction speed limit in MB/s
- `maxVolumeCounts` (int32, optional): Maximum number of volumes per volume server
- `fileSizeLimitMB` (int32, optional): File size limit in MB
- `fixJpgOrientation` (bool, optional): Fix JPEG orientation on upload
- `idleTimeout` (int32, optional): Idle connection timeout in seconds
- `minFreeSpacePercent` (int32, optional): Minimum free space percentage before volume becomes read-only
- `metricsPort` (int32, optional): Port for Prometheus metrics
- `service` (ServiceSpec, optional): Service configuration for volume servers

**Pod Configuration:**
- `env` ([]corev1.EnvVar): Environment variables for volume server containers
- `imagePullPolicy` (corev1.PullPolicy): Image pull policy override
- `imagePullSecrets` ([]corev1.LocalObjectReference): Image pull secrets
- `priorityClassName` (string, optional): Priority class for pod scheduling
- `schedulerName` (string, optional): Custom scheduler name
- `terminationGracePeriodSeconds` (int64, optional): Pod termination grace period
- `hostNetwork` (bool, optional): Enable host networking
- `annotations` (map[string]string): Annotations for pods and services

#### Tree Topology Fields

- `volumeTopology` (map[string]*VolumeTopologySpec, optional): Map of named volume server groups for topology-aware deployment. Each key represents a topology group name.

#### VolumeTopologySpec Fields

Each topology group supports the following fields:

**Required Fields:**
- `replicas` (int32, required): Number of volume server replicas for this topology group
- `rack` (string, required): The rack name for this topology group
- `dataCenter` (string, required): The datacenter name for this topology group

**Resource Configuration:**
- `requests` (ResourceList): Resource requests (CPU, memory, storage) for this topology group
- `limits` (ResourceList): Resource limits (CPU, memory) for containers
- `storageClassName` (string, optional): Storage class for PVCs in this topology group

**Topology-Specific Settings:**
- `nodeSelector` (map[string]string): Node selector to ensure pods are scheduled on appropriate nodes
- `tolerations` ([]corev1.Toleration): Tolerations for pod scheduling
- `affinity` (corev1.Affinity): Affinity rules for pod placement

**Volume Server Configuration:**
- `compactionMBps` (int32, optional): Background compaction speed limit in MB/s
- `maxVolumeCounts` (int32, optional): Maximum number of volumes per volume server
- `fileSizeLimitMB` (int32, optional): File size limit in MB
- `metricsPort` (int32, optional): Port for Prometheus metrics
- `service` (ServiceSpec, optional): Service configuration for this topology group

**Pod Configuration:**
- `env` ([]corev1.EnvVar): Environment variables for volume server containers
- `imagePullPolicy` (corev1.PullPolicy): Image pull policy override
- `imagePullSecrets` ([]corev1.LocalObjectReference): Image pull secrets
- `priorityClassName` (string, optional): Priority class for pod scheduling
- `schedulerName` (string, optional): Custom scheduler name
- `terminationGracePeriodSeconds` (int64, optional): Pod termination grace period
- `hostNetwork` (bool, optional): Enable host networking

### Field Inheritance Behavior

When using `volumeTopology`, it's important to understand which fields inherit from global settings and which are topology-specific only:

#### Fields with Global Fallback

These fields use topology-specific values when provided, otherwise fall back to global `spec` settings:

- **`affinity`**: `volumeTopology.GROUP.affinity` → `spec.affinity`
- **`tolerations`**: `volumeTopology.GROUP.tolerations` → `spec.tolerations`
- **`schedulerName`**: `volumeTopology.GROUP.schedulerName` → `spec.schedulerName`
- **`imagePullSecrets`**: `volumeTopology.GROUP.imagePullSecrets` → `spec.imagePullSecrets`
- **`hostNetwork`**: `volumeTopology.GROUP.hostNetwork` → `spec.hostNetwork`
- **`imagePullPolicy`**: `volumeTopology.GROUP.imagePullPolicy` → `spec.imagePullPolicy`

#### Fields with Volume-Specific Fallback

These fields use topology-specific values when provided, otherwise fall back to `spec.volume` settings:

- **`env`**: `volumeTopology.GROUP.env` → `spec.volume.env`
- **`requests`** and **`limits`**: `volumeTopology.GROUP.requests/limits` → `spec.volume.requests/limits`
- **`storageClassName`**: `volumeTopology.GROUP.storageClassName` → `spec.volume.storageClassName`
- **`metricsPort`**: `volumeTopology.GROUP.metricsPort` → `spec.volume.metricsPort`
- **`service`**: `volumeTopology.GROUP.service` → `spec.volume.service`
- **Volume server settings**: All fields (`compactionMBps`, `maxVolumeCounts`, `fileSizeLimitMB`, etc.) fall back to `spec.volume` equivalents

#### Fields with Merge Behavior

These fields merge global and topology-specific values:

- **`nodeSelector`**: Merges `spec.nodeSelector` and `volumeTopology.GROUP.nodeSelector` (topology takes precedence for conflicting keys)
- **`annotations`**: Merges `spec.annotations` and `volumeTopology.GROUP.annotations` (topology takes precedence for conflicting keys)

#### Fields with No Inheritance

These fields are topology-specific only and do not inherit from global settings:

- **`priorityClassName`**: Priority class is topology-specific only
- **`terminationGracePeriodSeconds`**: Termination grace period is topology-specific only

#### Migration Considerations

When migrating from simple to tree topology:
- Most pod-level settings (scheduling, networking) will inherit from global spec
- **Volume server settings now inherit automatically**: Resource requirements, storage settings, and volume server tuning parameters defined in `spec.volume` will automatically be used as defaults for all topology groups
- **Override only as needed**: You only need to specify settings in topology groups that differ from the `spec.volume` defaults
- **Consistent behavior**: The same fallback logic applies to all VolumeServerConfig fields, making the API predictable and user-friendly

## Kubernetes Node Labels

For proper topology-aware placement, ensure your Kubernetes nodes are labeled appropriately:

```bash
# Label nodes with topology information
kubectl label node node1 topology.kubernetes.io/zone=dc1-rack1
kubectl label node node2 topology.kubernetes.io/zone=dc1-rack2
kubectl label node node3 topology.kubernetes.io/zone=dc2-rack1

# Or use custom labels
kubectl label node node1 seaweedfs/datacenter=dc1 seaweedfs/rack=rack1
kubectl label node node2 seaweedfs/datacenter=dc1 seaweedfs/rack=rack2
kubectl label node node3 seaweedfs/datacenter=dc2 seaweedfs/rack=rack1
```

## Verification

Once deployed, you can verify that volume servers are reporting the correct topology by:

1. Checking the SeaweedFS master UI at `http://<master-service>:9333`
2. Looking at the volume server list to confirm datacenter and rack information
3. Using the SeaweedFS shell: `echo "cluster.status" | weed shell -master=<master-service>:9333`

## Tree Topology Advantages

The tree topology approach (`volumeTopology`) offers several advantages over the simple topology approach:

### 1. **Granular Control**
- Configure different resource requirements per topology group
- Use different storage classes for different locations
- Apply different volume server settings per topology group

### 2. **Better Organization**
- Logical grouping by datacenter and rack
- Clear naming convention for topology groups
- Individual service and monitoring per group

### 3. **Flexible Scaling**
- Scale each topology group independently
- Add new topology groups without affecting existing ones
- Different replica counts per topology location

### 4. **Advanced Kubernetes Integration**
- Per-group node selectors, tolerations, and affinity rules
- Individual service configurations
- Separate metrics endpoints for better monitoring

## Best Practices

### 1. **Naming Convention**
Use descriptive names for topology groups:
```yaml
volumeTopology:
  dc1-rack1:     # Clear datacenter and rack identification
  dc1-rack2:
  dc2-rack1:
  dc2-rack2:
```

### 2. **Resource Planning**
- Assign more resources to primary datacenters
- Use faster storage classes for critical locations
- Consider network bandwidth between datacenters

### 3. **Node Labeling Strategy**
Label nodes consistently across your cluster:
```bash
kubectl label node <node-name> \
  topology.kubernetes.io/zone=us-west-2a \
  seaweedfs/datacenter=dc1 \
  seaweedfs/rack=rack1
```

### 4. **Monitoring**
- Enable metrics per topology group
- Set up separate ServiceMonitors for detailed monitoring
- Monitor cross-datacenter replication latency

## Migration

### From Older Versions to Topology Support

If you're upgrading from a version without topology support, existing volume servers will continue to work without any rack or datacenter information. You can gradually add topology information to new volume servers as needed.

### From Simple to Tree Topology

When transitioning from the simple topology approach to the more flexible tree topology approach:

1. **Document Current Configuration**: Note your current volume server configuration
2. **Create Equivalent Topology Groups**: Create equivalent topology groups in `volumeTopology`
3. **Disable Simple Topology**: Set original `volume.replicas: 0` to disable simple topology
4. **Apply Configuration**: Apply the updated configuration
5. **Verify Topology**: Verify all volume servers report correct topology

**Migration Example:**
```yaml
apiVersion: seaweed.seaweedfs.com/v1
kind: Seaweed
metadata:
  name: seaweed-migration
spec:
  # Disable legacy volume section
  volume:
    replicas: 0

  # Use new topology structure
  volumeTopology:
    dc1-rack1:
      replicas: 3
      rack: "rack1"
      dataCenter: "dc1"
      # ... other settings
```

## Troubleshooting

### Simple Topology Issues
- **Volume servers not showing topology information**: Ensure the `rack` and `dataCenter` fields are properly set in the SeaweedFS resource.
- **Pod scheduling issues**: Check that your node selectors match the actual node labels in your cluster.

### Tree Topology Issues
- **Topology groups not deploying**: Check that all required fields (`replicas`, `rack`, `dataCenter`) are specified
- **Services not created**: Verify topology group names don't conflict with existing resources
- **Wrong node placement**: Ensure `nodeSelector` matches actual node labels
- **Resource conflicts**: Check that different topology groups don't compete for the same nodes
- **210 replication not working**: Verify that you have volume servers in at least 2 different datacenters and multiple racks across all topology groups

### General Debugging
1. Check operator logs: `kubectl logs -n seaweedfs-system deploy/seaweedfs-operator`
2. Verify CRD is updated: `kubectl get crd seaweeds.seaweed.seaweedfs.com -o yaml`
3. Check StatefulSet creation: `kubectl get statefulsets -l seaweedfs/topology`
4. Verify services: `kubectl get services -l seaweedfs/topology`
5. Check volume server logs: `kubectl logs -l app=seaweed,component=volume`

## References

- [SeaweedFS Replication Documentation](https://github.com/seaweedfs/seaweedfs/wiki/Replication)
- [Kubernetes Node Labels](https://kubernetes.io/docs/concepts/overview/working-with-objects/labels/)
- [SeaweedFS Architecture](https://github.com/seaweedfs/seaweedfs/wiki/Architecture)
