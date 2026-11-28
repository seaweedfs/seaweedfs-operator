# Changelog

Release notes for the `seaweedfs-operator` chart. Operator image releases, and
the chart releases before 0.2.0, are listed on the
[releases page](https://github.com/seaweedfs/seaweedfs-operator/releases).

## 0.2.0 — scoped RBAC by default

**Breaking.** A default install no longer runs with cluster-wide access to
Secrets. Issue: [#477](https://github.com/seaweedfs/seaweedfs-operator/issues/477).
Change: [#478](https://github.com/seaweedfs/seaweedfs-operator/pull/478).

- The manager is scoped to the release namespace by default: one
  `Role`/`RoleBinding` per watched namespace, a `ClusterRole` limited to the
  cluster-scoped reads the controllers still need (`namespaces`,
  `storageclasses`), and `--watch-namespaces` on the manager. Previously a
  default install rendered a single `ClusterRole` that could read and write
  every Secret in the cluster.
- New value `rbac.clusterWide` (default `false`) restores that previous single
  `ClusterRole`. Setting it together with `watchNamespaces` fails the render.
- `Seaweed` CRs outside the release namespace are no longer watched or
  reconciled. List their namespaces in `watchNamespaces`, or set
  `rbac.clusterWide: true` to keep the old behaviour.
- `helm upgrade` replaces the release's `ClusterRoleBinding` with the namespaced
  bindings; no manual cleanup is needed.
- Kustomize installs (`make deploy`) keep their cluster-wide `ClusterRole`, but
  the manager now runs as its own `manager` ServiceAccount instead of the
  namespace's `default`.
- The values table documents `rbac.clusterWide` and the new `watchNamespaces`
  default; the repository README has a migration note under "Scoped RBAC".
