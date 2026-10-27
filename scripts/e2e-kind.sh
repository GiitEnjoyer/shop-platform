#!/usr/bin/env bash
# End-to-end test of the Blue-Green flow on a throwaway kind cluster:
#   v1 goes live on blue -> v2 deployed to green -> smoke -> switch -> verify v2
#   -> rollback -> verify v1 -> scale-down.
# Runs in GitHub Actions (job e2e) and locally: `make e2e` (needs docker, kind, helm, kubectl).
set -Eeuo pipefail

CLUSTER="${CLUSTER:-shop-e2e}"
export NAMESPACE=shop ENV=dev IDLE_REPLICAS=1
export HELM_EXTRA_ARGS="--set image.repository=shop/catalog --set image.pullPolicy=Never --set ingress.enabled=false --set serviceMonitor.enabled=false --set networkPolicy.enabled=false"

log() { printf '\n\033[1;32m=== %s\033[0m\n' "$*"; }

served_version() {
  kubectl -n "$NAMESPACE" run "probe-${RANDOM}" --rm -i --restart=Never --quiet \
    --image=curlimages/curl:8.10.1 -- curl -fsS http://catalog/version | sed -E 's/.*"version":"([^"]+)".*/\1/'
}

expect_version() {
  local want=$1 got
  got="$(served_version)"
  if [[ "$got" != "$want" ]]; then
    echo "expected live version ${want}, got ${got}" >&2
    exit 1
  fi
  echo "live version: ${got} ✓"
}

log "build images v1 and v2"
docker build -q --build-arg SERVICE=catalog --build-arg VERSION=v1 -t shop/catalog:v1 .
docker build -q --build-arg SERVICE=catalog --build-arg VERSION=v2 -t shop/catalog:v2 .

if ! kind get clusters | grep -qx "$CLUSTER"; then
  log "create kind cluster"
  kind create cluster --name "$CLUSTER" --wait 120s
fi
kind load docker-image --name "$CLUSTER" shop/catalog:v1 shop/catalog:v2
kubectl create namespace "$NAMESPACE" --dry-run=client -o yaml | kubectl apply -f -

log "v1 -> blue (first install)"
./scripts/blue-green.sh deploy catalog v1
expect_version v1

log "v2 -> green (idle), smoke via preview service"
./scripts/blue-green.sh deploy catalog v2
./scripts/blue-green.sh smoke catalog
expect_version v1   # traffic must still go to blue

log "switch traffic to green"
./scripts/blue-green.sh switch catalog
expect_version v2

log "rollback to blue"
./scripts/blue-green.sh rollback catalog
expect_version v1

log "scale down idle color"
./scripts/blue-green.sh scale-down catalog
kubectl -n "$NAMESPACE" get deploy -l app.kubernetes.io/name=catalog

log "E2E PASSED"
