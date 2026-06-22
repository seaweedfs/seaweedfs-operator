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
# hand-maintained RBAC. Past chart-RBAC drift (missing bucket RBAC,
# servicemonitor RBAC gated behind a Helm value the operator binary
# can't read) was invisible to the existing e2e tests for that reason —
# both classes of bug only manifested at `helm install` time. The
# static parity test in test/helm/rbac_drift_test.go catches obvious
# drift at compile-time, but a full install lap also catches packaging
# bugs (missing CRD include, broken ServiceAccount binding, helm
# template syntax errors that only manifest at apply time, conditional
# gates the static parity test doesn't render).
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
#   READY_TIMEOUT      (default: 10m — how long to wait for the Seaweed CR
#                      to reach its Ready condition)

set -euo pipefail

NAMESPACE="${NAMESPACE:-seaweedfs-operator-system}"
RELEASE="${RELEASE:-seaweedfs-operator}"
SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" >/dev/null 2>&1 && pwd -P)"
REPO_ROOT="$(cd -- "$SCRIPT_DIR/../.." >/dev/null 2>&1 && pwd -P)"
CHART_DIR="${CHART_DIR:-$REPO_ROOT/deploy/helm}"
IMAGE_REPOSITORY="${IMAGE_REPOSITORY:-ghcr.io/seaweedfs/seaweedfs-operator}"
IMAGE_TAG="${IMAGE_TAG:-v0.0.1}"
RECONCILE_WAIT="${RECONCILE_WAIT:-30}"
READY_TIMEOUT="${READY_TIMEOUT:-10m}"

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
# Resolve the operator Deployment by Helm's standard
# app.kubernetes.io/instance label rather than guessing the rendered
# fullname. The chart's `_helpers.tpl` collapses
# `<release>-<chart-name>` to just `<release>` when the release name
# already contains the chart name, so a hardcoded `<release>-<chart>`
# only works for some release names — use the label.
DEPLOYMENT="$(kubectl -n "$NAMESPACE" get deployment \
  -l app.kubernetes.io/instance="$RELEASE",app.kubernetes.io/name=seaweedfs-operator \
  -o jsonpath='{.items[0].metadata.name}')"
if [ -z "$DEPLOYMENT" ]; then
  fail "no Deployment matching the operator labels in namespace $NAMESPACE"
fi
log "  resolved operator Deployment: $DEPLOYMENT"
kubectl wait deployment.apps/"$DEPLOYMENT" \
  --for=condition=Available --namespace "$NAMESPACE" --timeout 5m

# Apply a basic Seaweed CR. This drives the SeaweedReconciler which,
# among other things, attempts to upsert ServiceMonitors for the
# master/volume/filer/admin/worker components — that's the path that
# would trip a missing servicemonitor RBAC rule.
log "applying sample Seaweed CR"
kubectl apply -f "$REPO_ROOT/config/samples/seaweed_v1_seaweed.yaml"

# The chart defaults give a no-TLS cluster with a filer — the exact shape
# that regressed once, when the operator mounted a security.toml requiring
# JWT-signed reads and the filer's unauthenticated readiness/liveness probe
# was 401'd into CrashLoopBackOff. Until now this smoke only scraped the
# operator log for RBAC errors, so a filer that never came up went
# unnoticed. The operator only flips the CR to Ready once every component's
# pods are Ready, so waiting on that condition turns a component-never-Ready
# regression red here, on the path users actually run (`helm install`).
SEAWEED_CR="$REPO_ROOT/config/samples/seaweed_v1_seaweed.yaml"
SEAWEED_NAME="$(kubectl get -f "$SEAWEED_CR" -o jsonpath='{.metadata.name}')"
SEAWEED_NS="$(kubectl get -f "$SEAWEED_CR" -o jsonpath='{.metadata.namespace}')"
SEAWEED_NS="${SEAWEED_NS:-default}"
log "waiting up to ${READY_TIMEOUT} for Seaweed/${SEAWEED_NAME} to become Ready"
if ! kubectl wait "seaweed/${SEAWEED_NAME}" --namespace "$SEAWEED_NS" \
    --for=condition=Ready --timeout="$READY_TIMEOUT"; then
  log "Seaweed CR did not reach Ready; dumping cluster state:"
  kubectl get pods -n "$SEAWEED_NS" -o wide >&2 || true
  kubectl describe "seaweed/${SEAWEED_NAME}" -n "$SEAWEED_NS" >&2 || true
  # The operator log is the most useful artifact here — it carries the
  # reconcile errors behind a component never reaching Ready. We exit
  # before the RBAC-scrape section below, so dump it now.
  log "dumping operator logs:"
  kubectl logs -n "$NAMESPACE" deployment.apps/"$DEPLOYMENT" --tail=-1 >&2 || true
  fail "Seaweed cluster did not become Ready after helm install with chart defaults"
fi

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
LOGS="$(kubectl -n "$NAMESPACE" logs deployment.apps/"$DEPLOYMENT" --tail=-1 2>/dev/null || true)"
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
RBAC_ERRORS="$(printf '%s\n' "$LOGS" | grep -E -- "$PATTERN" || true)"
if [ -n "$RBAC_ERRORS" ]; then
  log "operator logs contain RBAC errors:"
  printf '%s\n' "$RBAC_ERRORS" | sed 's/^/  /' >&2
  fail "operator emitted RBAC permission errors after Helm install with default values; see lines above"
fi

log "PASS — operator reconciled cleanly with chart-default RBAC"
