#!/usr/bin/env bash
# Deterministic screenshot pipeline for the OSS otel dashboard (issue 0055 ⑤).
#
# Mirrors the SQLite e2e (e2e/screenshot.sh) but drives the Mimir/Loki backend:
#   fixture DB (e2e/testdata, time-shifted to now)
#     -> flush (HOME-sandboxed, never touches the real ~/.claude or config)
#     -> OTel Collector -> Mimir (gauge) / Loki (raw events)
#     -> Grafana /render -> PNG
#
# Determinism: same fixture => same data => same panel values. Absolute time is
# "now"-relative every run (so pixels shift on the time axis), exactly like the
# SQLite e2e — we guarantee data/value determinism, not byte-identical images.
# A fresh stack (down -v) before each run keeps Loki from accumulating duplicate
# log lines across runs (gauges are last-value so re-flush is value-stable, but
# raw event logs are not idempotent in Loki).
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

# --- knobs (overridable) ---------------------------------------------------
# Host ports default away from `make grafana-up` (4318/9009/3100/13001) so the
# screenshot stack can run alongside a live grafana-up stack.
export OSS_COLLECTOR_PORT="${OSS_COLLECTOR_PORT:-4328}"
export OSS_MIMIR_PORT="${OSS_MIMIR_PORT:-9019}"
export OSS_LOKI_PORT="${OSS_LOKI_PORT:-3110}"
export GRAFANA_PORT="${GRAFANA_PORT:-13021}"
export DEPLOY_ENV="${DEPLOY_ENV:-e2e}"

PROJECT="${COMPOSE_PROJECT_OSS_E2E:-agent-telemetry-oss-e2e}"
OUTDIR="${OUTDIR:-.outputs/oss-screenshots}"
KEEP_UP="${KEEP_UP:-0}" # set 1 to leave the stack running for inspection

BASE_COMPOSE="deploy/oss-observability/docker-compose.yaml"
SHOT_COMPOSE="deploy/oss-observability/docker-compose.screenshot.yaml"
BIN="bin/agent-telemetry"
FIXTURE_DB="e2e/testdata/agent-telemetry.db"

compose() { docker compose -f "$BASE_COMPOSE" -f "$SHOT_COMPOSE" -p "$PROJECT" "$@"; }

cleanup() {
  if [ "$KEEP_UP" = "1" ]; then
    echo "KEEP_UP=1: leaving stack '$PROJECT' running (Grafana http://localhost:${GRAFANA_PORT})"
  else
    compose down -v >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

mkdir -p "$OUTDIR"

# --- 1. build binary + fixture DB from the current tree ---------------------
# Build from source so a stale installed binary (no `flush`) can't poison the
# run, and regenerate the fixture so its timestamps land in the last 30 days.
echo "[1/6] building binary + fixture DB from current tree..."
CGO_ENABLED=0 go build -o "$BIN" ./cmd/agent-telemetry/
CGO_ENABLED=0 GOTOOLCHAIN=local go test -run TestGenTestDB ./e2e/ >/dev/null

# --- 2. fresh stack ---------------------------------------------------------
echo "[2/6] (re)creating OSS stack '$PROJECT' (down -v -> up)..."
compose down -v >/dev/null 2>&1 || true
compose up -d

wait_http() { # url label
  local url="$1" label="$2" i
  for i in $(seq 1 90); do
    if curl -sf -o /dev/null "$url"; then echo "  $label ready"; return 0; fi
    sleep 1
  done
  echo "  $label NOT ready after 90s: $url" >&2
  return 1
}

echo "[3/6] waiting for backends..."
wait_http "http://localhost:${GRAFANA_PORT}/api/health" "grafana"
wait_http "http://localhost:${OSS_MIMIR_PORT}/ready" "mimir"
wait_http "http://localhost:${OSS_LOKI_PORT}/ready" "loki"

# --- 4. flush the fixture into the stack (HOME-sandboxed) -------------------
# The CLI has no --db/--config/--state flags; everything resolves off HOME /
# XDG_CONFIG_HOME. Sandbox both so we flush the fixture DB through a tokenless
# localhost-collector target without touching the real ~/.claude or config.
echo "[4/6] flushing fixture into Collector (sandboxed HOME)..."
SANDBOX="$(mktemp -d)"
trap 'rm -rf "$SANDBOX"; cleanup' EXIT
mkdir -p "$SANDBOX/.claude" "$SANDBOX/.config/agent-telemetry"
cp "$FIXTURE_DB" "$SANDBOX/.claude/agent-telemetry.db"
cat > "$SANDBOX/.config/agent-telemetry/config.toml" <<EOF
[[export]]
id = "oss-collector"
endpoint = "http://localhost:${OSS_COLLECTOR_PORT}"
encoding = "json"
signals = ["logs", "metrics"]
EOF
HOME="$SANDBOX" XDG_CONFIG_HOME="$SANDBOX/.config" "$BIN" flush --full --agent claude

# --- 5. wait for data to be queryable --------------------------------------
# Collector batches (5s) then Mimir/Loki ingest. Poll the same query APIs
# Grafana uses so render never races an empty backend. mimir.yaml has
# multitenancy_enabled=false, so no X-Scope-OrgID header is needed.
echo "[5/6] waiting for Mimir gauge + Loki logs to be queryable..."
NOW_NS="$(( $(date +%s) * 1000000000 ))"
START_NS="$(( ( $(date +%s) - 2592000 ) * 1000000000 ))" # now-30d
mimir_has_data() {
  curl -sf "http://localhost:${OSS_MIMIR_PORT}/prometheus/api/v1/query?query=agent_pr_total_tokens" \
    2>/dev/null | grep -q '"result":\[{'
}
loki_has_data() {
  curl -sf -G "http://localhost:${OSS_LOKI_PORT}/loki/api/v1/query_range" \
    --data-urlencode 'query={service_name="agent-telemetry"}' \
    --data-urlencode "start=${START_NS}" --data-urlencode "end=${NOW_NS}" \
    --data-urlencode 'limit=5' 2>/dev/null | grep -q '"values":\['
}
for i in $(seq 1 60); do
  if mimir_has_data && loki_has_data; then echo "  backends populated"; break; fi
  if [ "$i" = "60" ]; then echo "  data not visible after 60s" >&2; exit 1; fi
  sleep 2
done

# Report counts so two runs can be compared for data-level determinism.
SERIES="$(curl -sf "http://localhost:${OSS_MIMIR_PORT}/prometheus/api/v1/query?query=count(last_over_time(agent_pr_total_tokens%5B30d%5D))" | sed -n 's/.*"value":\[[^,]*,"\([0-9.]*\)"\].*/\1/p')"
echo "  mimir: agent_pr_total_tokens distinct series = ${SERIES:-?}"

# --- 6. render -------------------------------------------------------------
echo "[6/6] rendering panels + full dashboard..."
BASE="http://localhost:${GRAFANA_PORT}"
DASH="agent-telemetry-oss"
FROM="now-30d"; TO="now"; SCALE=2; TZ="Asia/Tokyo"
VAR="var-coding_agent=\$__all"
W=1600; H=600

# panelId:name (rows 100-103 are headers, skipped)
declare -a PANELS=(
  "24:tier2-merged-prs"
  "25:tier2-total-tokens"
  "12:tier2-pr-per-million-tokens"
  "20:tier2-weekly-merged-prs"
  "30:tier3-top-level-sessions"
  "31:tier3-weekly-token-consumption"
  "32:tier3-weekly-tokens-per-session"
  "33:tier3-weekly-ask-user-question"
  "2:tier1-pr-scorecard:${W}:900"
  "14:tier1-session-count"
  "15:tier1-tokens-per-tool-use"
  "6:tier1-raw-events"
)
for entry in "${PANELS[@]}"; do
  IFS=: read -r ID NAME PW PH <<< "$entry"
  PW="${PW:-$W}"; PH="${PH:-$H}"
  OUT="${OUTDIR}/panel-${ID}-${NAME}.png"
  URL="${BASE}/render/d-solo/${DASH}/x?panelId=${ID}&from=${FROM}&to=${TO}&width=${PW}&height=${PH}&scale=${SCALE}&tz=${TZ}&${VAR}"
  echo "  panel ${ID} (${NAME})"
  curl -sf -o "$OUT" "$URL"
done

# Full dashboard -> README hero (issue 0055 ⑥). The OSS dashboard is tall;
# height must cover all rows or the renderer clips the bottom.
FULL="${OUTDIR}/dashboard-full.png"
echo "  full dashboard"
curl -sf -o "$FULL" "${BASE}/render/d/${DASH}/x?from=${FROM}&to=${TO}&width=1600&height=2100&scale=${SCALE}&tz=${TZ}&${VAR}&kiosk"

DOCDIR="docs/assets"
mkdir -p "$DOCDIR"
cp "$FULL" "${DOCDIR}/dashboard-full.png"

echo "Done: ${#PANELS[@]} panels + full dashboard in ${OUTDIR}"
echo "README hero updated: ${DOCDIR}/dashboard-full.png"
