#!/usr/bin/env bash
#
# Helm-install smoke test: deploys the operator from the chart at
# deploy/helm/ with **default values** (i.e., what a real user gets from
# `helm install seaweedfs/seaweedfs-operator`), applies a Seaweed CR
# and a Bucket CR to exercise both controllers, then asserts the
# operator emits no RBAC permission errors during reconcile. A second
# lap upgrades the release with webhook.certManager.enabled=true to
# exercise the cert-manager certificate path against the cert-manager
# instance `make kind-prepare` installs.
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
#                      the fixtures and inspecting operator logs)
#   RECONCILE_TIMEOUT  (default: 180 — seconds to wait for each fixture to
#                      report status.observedGeneration)
#   RECONCILE_DELETE_TIMEOUT
#                      (default: 300 — seconds to wait per delete for fixtures
#                      left by a previous run to disappear before they are
#                      re-applied; the Seaweed delete also waits for every
#                      resource it owns (StatefulSets, Services, ConfigMaps,
#                      …) to be garbage collected, which is the slow part)

set -euo pipefail

NAMESPACE="${NAMESPACE:-seaweedfs-operator-system}"
RELEASE="${RELEASE:-seaweedfs-operator}"
SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" >/dev/null 2>&1 && pwd -P)"
REPO_ROOT="$(cd -- "$SCRIPT_DIR/../.." >/dev/null 2>&1 && pwd -P)"
CHART_DIR="${CHART_DIR:-$REPO_ROOT/deploy/helm}"
IMAGE_REPOSITORY="${IMAGE_REPOSITORY:-ghcr.io/seaweedfs/seaweedfs-operator}"
IMAGE_TAG="${IMAGE_TAG:-v0.0.1}"
RECONCILE_WAIT="${RECONCILE_WAIT:-30}"
RECONCILE_TIMEOUT="${RECONCILE_TIMEOUT:-180}"
RECONCILE_DELETE_TIMEOUT="${RECONCILE_DELETE_TIMEOUT:-300}"
FIXTURE_DIR="$SCRIPT_DIR/fixtures"

log() { printf '[smoke] %s\n' "$*"; }
fail() { printf '[smoke] FAIL: %s\n' "$*" >&2; exit 1; }

# await_reconciled fails unless the named CR reports status.observedGeneration
# equal to its current metadata.generation within RECONCILE_TIMEOUT seconds:
# proof that its controller watches $NAMESPACE and reconciled the object as it
# exists now, rather than never seeing it or having reconciled an older
# generation.
await_reconciled() {
  local kind="$1" name="$2" deadline=$((SECONDS + RECONCILE_TIMEOUT)) generation="" observed=""
  log "waiting for $kind/$name to be reconciled"
  while [ "$SECONDS" -lt "$deadline" ]; do
    read -r generation observed <<<"$(kubectl -n "$NAMESPACE" get "$kind" "$name" \
      -o jsonpath='{.metadata.generation} {.status.observedGeneration}' 2>/dev/null || true)" || true
    if [ -n "$generation" ] && [ "$observed" = "$generation" ]; then
      log "  $kind/$name reconciled (generation=$generation)"
      return 0
    fi
    sleep 3
  done
  fail "$kind/$name was not reconciled within ${RECONCILE_TIMEOUT}s — is it outside the operator's watch namespace ($NAMESPACE)?"
}

require() {
  command -v "$1" >/dev/null 2>&1 || fail "$1 not found in PATH"
}

require helm
require kubectl

log "deploying operator via Helm with chart defaults"
# Override only the image registry/tag so the locally-loaded image is
# used; everything else — including the default watch scope, the release
# namespace — stays at chart defaults, so the test reflects what
# `helm install seaweedfs/seaweedfs-operator` produces for a real user.
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

# Apply a basic Seaweed fixture. It drives the SeaweedReconciler which,
# among other things, attempts to upsert ServiceMonitors for the
# master/volume/filer/admin/worker components — that's the path that
# would trip a missing servicemonitor RBAC rule.
#
# Both fixtures go into $NAMESPACE, the namespace a default install
# watches. Anywhere else they would be neither admitted by the webhooks nor
# reconciled, and the log scan below would pass without exercising either
# controller.
#
# A rerun reuses the Kind cluster, so drop whatever the previous run left
# behind first: await_reconciled would otherwise pass on status written by
# that older operator run. Bucket before Seaweed only so that a Bucket
# deleted under reclaimPolicy: Delete would still reach the cluster to
# remove its S3 bucket; this fixture keeps the default Retain, so its
# finalizer drops without touching S3 either way.
log "removing fixtures left by a previous run"
kubectl -n "$NAMESPACE" delete -f "$FIXTURE_DIR/bucket.yaml" \
  --ignore-not-found --timeout="${RECONCILE_DELETE_TIMEOUT}s" \
  || fail "leftover Bucket/photos did not delete within ${RECONCILE_DELETE_TIMEOUT}s"
kubectl -n "$NAMESPACE" delete -f "$FIXTURE_DIR/seaweed.yaml" \
  --ignore-not-found --cascade=foreground --timeout="${RECONCILE_DELETE_TIMEOUT}s" \
  || fail "leftover Seaweed/seaweed1 did not delete within ${RECONCILE_DELETE_TIMEOUT}s"

log "applying Seaweed fixture in $NAMESPACE"
kubectl apply -n "$NAMESPACE" -f "$FIXTURE_DIR/seaweed.yaml"

# Apply a basic Bucket CR. This drives the BucketReconciler all the
# way through its full reconcile sequence (List, Get, finalizer
# Update, Status Patch), not just the initial List. Its clusterRef is
# same-namespace, so no ResourceReferenceGrant is involved.
log "applying Bucket fixture in $NAMESPACE"
kubectl apply -n "$NAMESPACE" -f "$FIXTURE_DIR/bucket.yaml"

# Prove the controllers saw the CRs before trusting the log scan: both
# publish status.observedGeneration only after reconciling the object.
await_reconciled seaweed seaweed1
await_reconciled bucket photos

log "sleeping ${RECONCILE_WAIT}s for further reconcile cycles to run"
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

# Second lap: flip the webhook certificates over to cert-manager. The
# upgrade swaps the certgen hook Jobs for an Issuer/Certificate pair,
# cert-manager takes over the existing serving-cert Secret, and
# cainjector replaces the caBundle on both webhook configurations.
# The webhooks use failurePolicy=Fail, so a successful admission below
# proves the full TLS chain converged on the cert-manager certificate.
log "upgrading release to cert-manager-managed webhook certificates"
helm upgrade "$RELEASE" "$CHART_DIR" \
  --namespace "$NAMESPACE" \
  --reuse-values \
  --set webhook.certManager.enabled=true \
  --wait --timeout 5m

# The Certificate shares the Deployment's fullname prefix.
CERTIFICATE="$DEPLOYMENT-webhook-cert"
log "waiting for Certificate $CERTIFICATE to become Ready"
kubectl -n "$NAMESPACE" wait certificates.cert-manager.io/"$CERTIFICATE" \
  --for=condition=Ready --timeout 2m

# `kubectl apply` skips the write (and therefore the webhooks) when the
# manifest is unchanged, so mutate an annotation to force an UPDATE
# through admission. Retry while cainjector and the rolled pods
# converge on the new certificate.
log "verifying webhook admission through the cert-manager certificate"
ADMITTED=""
for attempt in $(seq 1 24); do
  if kubectl -n "$NAMESPACE" annotate -f "$FIXTURE_DIR/seaweed.yaml" \
       smoke-test/cert-manager="attempt-$attempt" --overwrite >/dev/null 2>&1; then
    ADMITTED=1
    break
  fi
  sleep 5
done
if [ -z "$ADMITTED" ]; then
  fail "admission kept failing after switching webhook certificates to cert-manager"
fi

log "PASS — webhooks admitted CRs with the cert-manager certificate"
