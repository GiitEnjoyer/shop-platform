#!/usr/bin/env bash
# Blue-Green release of a Helm release built from deploy/helm/microservice.
#
#   blue-green.sh deploy  <release> <tag>   # roll <tag> out to the idle color
#   blue-green.sh smoke   <release>         # test the idle color via -preview Service
#   blue-green.sh switch  <release>         # move traffic to the idle color
#   blue-green.sh rollback <release>        # move traffic back (old color still running)
#   blue-green.sh scale-down <release>      # stop the idle color after the bake time
set -Eeuo pipefail

cmd="${1:?command}"; release="${2:?release}"
ns="${NAMESPACE:-shop}"
chart="${CHART:-deploy/helm/microservice}"
values=(-f "deploy/helm/values/${release}/common.yaml" -f "deploy/helm/values/${release}/${ENV:-prod}.yaml")
# Extra flags for the first install/deploy, e.g. HELM_EXTRA_ARGS="--set image.repository=shop/catalog"
read -r -a extra <<< "${HELM_EXTRA_ARGS:-}"

log() { printf '\033[1;34m[bg:%s]\033[0m %s\n' "$release" "$*"; }

active() { helm -n "$ns" get values "$release" -a -o json | jq -r '.blueGreen.activeColor'; }
other()  { [[ "$1" == "blue" ]] && echo green || echo blue; }
# Current replica count of a color (HPA may have scaled it beyond replicaCount).
replicas() { kubectl -n "$ns" get deploy "${release}-$1" -o jsonpath='{.spec.replicas}' 2>/dev/null || echo 0; }

case "$cmd" in
  deploy)
    tag="${3:?tag}"
    if ! helm -n "$ns" status "$release" >/dev/null 2>&1; then
      log "first install: ${tag} goes live on blue"
      helm upgrade --install "$release" "$chart" -n "$ns" "${values[@]}" "${extra[@]}" \
        --set "blueGreen.activeColor=blue" --set "blueGreen.blue.tag=${tag}" \
        --wait --timeout 10m
      exit 0
    fi
    cur="$(active)"
    idle="$(other "$cur")"
    n="${IDLE_REPLICAS:-$(replicas "$cur")}"
    log "active=${cur} (${n} replicas); deploying ${tag} to ${idle} with ${n} replicas"
    helm upgrade --install "$release" "$chart" -n "$ns" "${values[@]}" "${extra[@]}" \
      --reuse-values \
      --set "blueGreen.activeColor=${cur}" \
      --set "blueGreen.${idle}.tag=${tag}" \
      --set "blueGreen.activeReplicas=$(replicas "$cur")" \
      --set "blueGreen.idleReplicas=${n}" \
      --wait --timeout 10m
    kubectl -n "$ns" rollout status "deploy/${release}-${idle}" --timeout=5m
    ;;

  smoke)
    idle="$(other "$(active)")"
    log "smoke testing ${idle} via ${release}-preview"
    kubectl -n "$ns" run "smoke-${release}-${RANDOM}" --rm -i --restart=Never \
      --image=curlimages/curl:8.10.1 -- sh -ec "
        curl -fsS http://${release}-preview/readyz
        curl -fsS http://${release}-preview/version
        ${SMOKE_CMD:-true}"
    ;;

  switch)
    cur="$(active)"; next="$(other "$cur")"; n="$(replicas "$cur")"
    log "switching traffic ${cur} -> ${next} (${n} replicas)"
    helm upgrade "$release" "$chart" -n "$ns" --reuse-values \
      --set "blueGreen.activeColor=${next}" \
      --set "blueGreen.activeReplicas=${n}" --set "blueGreen.idleReplicas=${n}" --wait
    log "done; ${cur} keeps running for instant rollback until scale-down"
    ;;

  rollback)
    cur="$(active)"; prev="$(other "$cur")"; n="$(replicas "$cur")"
    log "rolling back traffic ${cur} -> ${prev}"
    helm upgrade "$release" "$chart" -n "$ns" --reuse-values \
      --set "blueGreen.activeColor=${prev}" \
      --set "blueGreen.activeReplicas=${n}" --set "blueGreen.idleReplicas=${n}" --wait
    ;;

  scale-down)
    log "scaling idle color to 0"
    helm upgrade "$release" "$chart" -n "$ns" --reuse-values \
      --set "blueGreen.activeReplicas=$(replicas "$(active)")" \
      --set "blueGreen.idleReplicas=0" --wait
    ;;

  *)
    echo "unknown command: $cmd" >&2; exit 2 ;;
esac
