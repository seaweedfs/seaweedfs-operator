# Changelog

## Unreleased

### Security

- Harden the Helm manager pod with a `RuntimeDefault` seccomp profile and
  health probes, correct the declared webhook port, and restrict webhook
  certificate updates to release-owned configurations
  ([#405](https://github.com/seaweedfs/seaweedfs-operator/issues/405),
  [#406](https://github.com/seaweedfs/seaweedfs-operator/pull/406)).
