# Changelog

All notable changes to this Operator are documented here.
This file was added with PR #471; earlier releases were published without a changelog.

## [unreleased]

### Changed

- **Security:** narrowed the default kustomize install (`make deploy`, `config/default`).
  The generated default ClusterRole no longer grants `rbac.authorization.k8s.io` `bind`/`escalate`,
  and no longer has cluster-wide `create`/`delete`/`update`/`patch` on `secrets`, `serviceaccounts`,
  `pods`, `nodes`, `persistentvolumes`, `csidrivers` or `storageclasses`.
  The default kustomize role set now matches the Helm chart default render.
  CVE tracking and CVSS are in `SECURITY_AUDIT.md`. ([#470](https://github.com/seaweedfs/seaweedfs-operator/issues/470), [#471](https://github.com/seaweedfs/seaweedfs-operator/pull/471))

### Added

- **`make deploy-csi` / `make undeploy-csi`** for kustomize installs that use the
  [seaweedfs-csi-driver](https://github.com/seaweedfs/seaweedfs-csi-driver):
  `config/csi` composes `config/default` plus a separate CSI ClusterRole and binding.
  Existing CSI kustomize installs must switch to `make deploy-csi` after upgrading,
  because `make deploy` no longer creates the CSI controller's required permissions.
  ([#471](https://github.com/seaweedfs/seaweedfs-operator/pull/471))
