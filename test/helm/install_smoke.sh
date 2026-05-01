#!/usr/bin/env bash
#
# Helm-install smoke test: deploys the operator from the chart at
# deploy/helm/ with **default values** (i.e., what a real user gets from
# `helm install seaweedfs/seaweedfs-operator`), applies a Seaweed CR
# and a Bucket CR to exercise both controllers, then asserts the
# operator emits no RBAC permission errors during reconcile.
#
# Why this exists: the existing test-e2e suite deploys via `make deploy`
# (kustomize-based, uses config/rbac/role.yaml — the kubebuilder-generated
# unconditional ClusterRole), so it never observes the chart's
# hand-maintained RBAC. Issue #223 (missing bucket RBAC) and the
# follow-up servicemonitor RBAC gate were both invisible to the
# existing e2e tests for that reason — both manifested only at
# `helm install` time. The static parity test in test/helm/rbac_drift_test.go
# catches obvious drift at compile-time, but a full install lap also
# catches packaging bugs (missing CRD include, broken ServiceAccount
# binding, helm template syntax errors that only manifest at apply
# time, conditional gates the static parity test doesn't render).
#
# Pre-conditions assumed:
#   - kubectl points at a Kind cluster created via `make kind-prepare`
#     (which installs Prometheus Operator and cert-manager already).
#   - Operator image is present in the cluster as ghcr.io/seaweedfs/seaweedfs-operator:v0.0.1
#     (loaded via `make kind-load`).
#
# Override points:
#   NAMESPACE          (default: seaweedfs-operator-system)
#   RELEASE            (default: seaweedfs-operator)
#   CHART_DIR          (default: deploy/helm relative to repo root)
#   IMAGE_REPOSITORY   (default: ghcr.io/seaweedfs/seaweedfs-operator)
#   IMAGE_TAG          (default: v0.0.1)
#   RECONCILE_WAIT     (default: 30 — seconds to wait between applying
#                      sample CRs and inspecting operator logs)

set -euo pipefail

NAMESPACE="${NAMESPACE:-seaweedfs-operator-system}"
RELEASE="${RELEASE:-seaweedfs-operator}"
SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" >/dev/null 2>&1 && pwd -P)"
REPO_ROOT="$(cd -- "$SCRIPT_DIR/../.." >/dev/null 2>&1 && pwd -P)"
CHART_DIR="${CHART_DIR:-$REPO_ROOT/deploy/helm}"
IMAGE_REPOSITORY="${IMAGE_REPOSITORY:-ghcr.io/seaweedfs/seaweedfs-operator}"
IMAGE_TAG="${IMAGE_TAG:-v0.0.1}"
RECONCILE_WAIT="${RECONCILE_WAIT:-30}"

log() { printf '[smoke] %s\n' "$*"; }
fail() { printf '[smoke] FAIL: %s\n' "$*" >&2; exit 1; }

require() {
  command -v "$1" >/dev/null 2>&1 || fail "$1 not found in PATH"
}

require helm
require kubectl

log "deploying operator via Helm with chart defaults"
# Override only the image registry/tag so the locally-loaded image is
# used; everything else stays at chart defaults so the test reflects
# what `helm install seaweedfs/seaweedfs-operator` produces for a real
# user.
helm upgrade --install "$RELEASE" "$CHART_DIR" \
  --namespace "$NAMESPACE" --create-namespace \
  --set "image.registry=$(dirname "$IMAGE_REPOSITORY")" \
  --set "image.repository=$(basename "$IMAGE_REPOSITORY")" \
  --set "image.tag=$IMAGE_TAG" \
  --wait --timeout 5m

log "waiting for operator deployment Available"
kubectl wait deployment.apps/"${RELEASE}-seaweedfs-operator" \
  --for=condition=Available --namespace "$NAMESPACE" --timeout 5m

# Apply a basic Seaweed CR. This drives the SeaweedReconciler which,
# among other things, attempts to upsert ServiceMonitors for the
# master/volume/filer/admin/worker components — that's the path that
# trips the missing servicemonitor RBAC reported in TsengSR's follow-up
# on issue #223.
log "applying sample Seaweed CR"
kubectl apply -f "$REPO_ROOT/config/samples/seaweed_v1_seaweed.yaml"

# Apply a basic Bucket CR. This drives the BucketReconciler all the
# way through its full reconcile sequence (List, Get, finalizer
# Update, Status Patch), not just the initial List. The bucket
# sample is in the `media` namespace, so create it first — without
# the namespace the apply errors before any RBAC verb beyond List
# gets exercised.
log "applying sample Bucket CR"
kubectl create namespace media --dry-run=client -o yaml | kubectl apply -f -
kubectl apply -f "$REPO_ROOT/config/samples/seaweed_v1_bucket.yaml"

log "sleeping ${RECONCILE_WAIT}s for reconcile cycles to run"
sleep "$RECONCILE_WAIT"

log "scraping operator logs for RBAC permission errors"
LOGS="$(kubectl -n "$NAMESPACE" logs deployment.apps/"${RELEASE}-seaweedfs-operator" --tail=-1 2>/dev/null || true)"
if [ -z "$LOGS" ]; then
  fail "no operator logs found — deployment may not be running"
fi

# Grep for the family of error fragments controller-runtime emits when
# RBAC is missing. Both the apiserver-formatted phrase and the typed
# error message can show up depending on which client wrote the log:
#   - "is forbidden: User \"...\" cannot list resource \"buckets\""
#   - "failed to watch *v1.Bucket" / "failed to list *v1.Bucket"
#   - controller-runtime reflector errors carrying "forbidden"
PATTERN='is forbidden|cannot list|cannot watch|cannot get|cannot create|cannot update|cannot patch|cannot delete|failed to list|failed to watch'
if printf '%s\n' "$LOGS" | grep -E -- "$PATTERN" >/tmp/smoke-rbac-errors 2>/dev/null; then
  log "operator logs contain RBAC errors:"
  sed 's/^/  /' /tmp/smoke-rbac-errors >&2
  fail "operator emitted RBAC permission errors after Helm install with default values; see lines above"
fi

log "PASS — operator reconciled cleanly with chart-default RBAC"
