#!/usr/bin/env bash
# Automated canary analysis: compares canary against stable for ANALYSIS_DURATION
# and exits non-zero if the canary is worse. Used by the `canary:analyze:*` CI jobs.
#
# Gates (all must pass on every check):
#   1. canary error ratio <= max(MAX_ERROR_RATIO, stable error ratio * ERROR_RATIO_FACTOR)
#   2. canary p99 latency <= stable p99 * LATENCY_FACTOR  (and <= MAX_P99_SECONDS)
#   3. canary received at least MIN_REQUESTS requests (no verdict on zero traffic)
#
# Requires: curl, jq. PROM_URL points to VictoriaMetrics/Prometheus.
set -Eeuo pipefail

APP="${APP:?APP is required}"
PROM_URL="${PROM_URL:?PROM_URL is required}"
ANALYSIS_DURATION="${ANALYSIS_DURATION:-300}"   # seconds
INTERVAL="${INTERVAL:-30}"
MAX_ERROR_RATIO="${MAX_ERROR_RATIO:-0.01}"
ERROR_RATIO_FACTOR="${ERROR_RATIO_FACTOR:-2}"
LATENCY_FACTOR="${LATENCY_FACTOR:-1.5}"
MAX_P99_SECONDS="${MAX_P99_SECONDS:-0.5}"
MIN_REQUESTS="${MIN_REQUESTS:-100}"

log() { printf '%s [canary:%s] %s\n' "$(date -u +%H:%M:%S)" "$APP" "$*"; }

# query <promql> -> prints scalar value or "NaN"
query() {
  curl -fsS --get "${PROM_URL%/}/api/v1/query" --data-urlencode "query=$1" \
    ${PROM_BEARER_TOKEN:+-H "Authorization: Bearer ${PROM_BEARER_TOKEN}"} \
    | jq -r '.data.result[0].value[1] // "NaN"'
}

# float comparison via awk: gt a b -> true if a > b
gt() { awk -v a="$1" -v b="$2" 'BEGIN { exit !(a > b) }'; }
is_nan() { [[ "$1" == "NaN" || "$1" == "+Inf" ]]; }

sel_api='route=~"[A-Z]+ /api/.*"'
err_ratio() {
  local track=$1
  query "(sum(rate(http_requests_total{app=\"${APP}\",track=\"${track}\",${sel_api},code=~\"5..\"}[2m])) or vector(0))
         / sum(rate(http_requests_total{app=\"${APP}\",track=\"${track}\",${sel_api}}[2m]))"
}
p99() {
  local track=$1
  query "histogram_quantile(0.99, sum by (le) (rate(http_request_duration_seconds_bucket{app=\"${APP}\",track=\"${track}\",${sel_api}}[2m])))"
}
requests() {
  query "sum(increase(http_requests_total{app=\"${APP}\",track=\"canary\",${sel_api}}[${ANALYSIS_DURATION}s]))"
}

checks=$(( ANALYSIS_DURATION / INTERVAL ))
log "analysis started: ${checks} checks every ${INTERVAL}s"

for ((i = 1; i <= checks; i++)); do
  sleep "$INTERVAL"

  c_err=$(err_ratio canary);  s_err=$(err_ratio stable)
  c_p99=$(p99 canary);        s_p99=$(p99 stable)
  is_nan "$s_err" && s_err=0

  err_limit=$(awk -v m="$MAX_ERROR_RATIO" -v s="$s_err" -v f="$ERROR_RATIO_FACTOR" \
    'BEGIN { l = s * f; print (l > m ? l : m) }')

  log "check ${i}/${checks}: errors canary=${c_err} stable=${s_err} (limit ${err_limit}); p99 canary=${c_p99}s stable=${s_p99}s"

  if ! is_nan "$c_err" && gt "$c_err" "$err_limit"; then
    log "FAIL: canary error ratio ${c_err} > ${err_limit}"
    exit 1
  fi
  if ! is_nan "$c_p99"; then
    if gt "$c_p99" "$MAX_P99_SECONDS"; then
      log "FAIL: canary p99 ${c_p99}s > absolute limit ${MAX_P99_SECONDS}s"
      exit 1
    fi
    if ! is_nan "$s_p99" && gt "$c_p99" "$(awk -v s="$s_p99" -v f="$LATENCY_FACTOR" 'BEGIN { print s * f }')"; then
      log "FAIL: canary p99 ${c_p99}s > stable p99 ${s_p99}s x ${LATENCY_FACTOR}"
      exit 1
    fi
  fi
done

total=$(requests)
if is_nan "$total" || ! gt "$total" "$MIN_REQUESTS"; then
  log "FAIL: canary served only ${total} requests (< ${MIN_REQUESTS}); no verdict possible"
  exit 1
fi

log "PASS: canary healthy over ${ANALYSIS_DURATION}s (${total} requests)"
