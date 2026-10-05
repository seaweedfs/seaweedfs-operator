# Changelog

## Unreleased

### Added

- Harden the Kustomize manager deployment with non-root execution, the
  `RuntimeDefault` seccomp profile, disabled privilege escalation, dropped
  Linux capabilities, and a read-only root filesystem
  ([#412](https://github.com/seaweedfs/seaweedfs-operator/issues/412),
  [#413](https://github.com/seaweedfs/seaweedfs-operator/pull/413)).
- Add opt-in pod and container security contexts for `SeaweedBackup`,
  `SeaweedRestore`, and `AdminScript` workloads
  ([#409](https://github.com/seaweedfs/seaweedfs-operator/issues/409),
  [#408](https://github.com/seaweedfs/seaweedfs-operator/pull/408)).
