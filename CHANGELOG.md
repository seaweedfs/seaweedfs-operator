# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Fixed

- Restore `coordination.k8s.io` Leases permissions for the default RBAC role via `internal/controller/doc.go`, so the leases marker is included by the default-controller pass and is not gated behind CSI RBAC.
- Set `ENABLE_CSI_DRIVER=true` on the `controller-manager` container in the CSI overlay (`config/csi/kustomization.yaml`), so the CSI RBAC path builds an enabled operator image by default.
- Align the CSI `ClusterRoleBinding` subject in `config/csi/role_binding.yaml` with the ServiceAccount the manager pod actually runs as (`ServiceAccount: default`, namespace `system`), matching `config/rbac/role_binding.yaml` and the manager Deployment.
- Skip `kustomizeBuild` in `test/helm/rbac_drift_test.go` when no kustomize binary is found, including the `os.ErrNotExist` case, instead of failing the test.

### Documentation

- Corrected the upgrade/CSI wording in `README.md`: the default Kustomize install no longer grants `bind` and `escalate`; the cluster-wide CSI/RBAC write grants now live under `config/csi`.
- `README.md` no longer claims that the `secrets` write grant was removed; the default role still grants `secrets` read/write (create/get/list/watch/update/patch/delete). The accurate note is kept.
- Added `CHANGELOG.md`.
