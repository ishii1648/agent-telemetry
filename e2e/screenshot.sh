#!/usr/bin/env bash
set -euo pipefail

OUTDIR="${1:-.outputs/grafana-screenshots}"
mkdir -p "$OUTDIR"

BASE="http://localhost:${GRAFANA_PORT:-13000}"
# fixture は gen_testdb_test.go によりテスト実行時の日時へシフトされる。
# 「Last 30 days」相当の相対指定で最新データが必ず描画されるようにする。
FROM="now-30d"
TO="now"
WIDTH=1600
HEIGHT=600
SCALE=2
TZ="Asia/Tokyo"

# Format: "panelId:name:width:height"  (width/height override defaults)
declare -a PANELS=(
  "1:headline-sessions:${WIDTH}:${HEIGHT}"
  "24:headline-merged-prs:${WIDTH}:${HEIGHT}"
  "25:headline-total-tokens:${WIDTH}:${HEIGHT}"
  "9:weekly-token-consumption:${WIDTH}:${HEIGHT}"
  "20:weekly-merged-prs:${WIDTH}:${HEIGHT}"
  "12:weekly-pr-per-million-tokens:${WIDTH}:${HEIGHT}"
  "21:weekly-tokens-per-session:${WIDTH}:${HEIGHT}"
  "2:pr-scorecard:${WIDTH}:900"
  "14:session-count:${WIDTH}:${HEIGHT}"
  "15:tokens-per-tool-use:${WIDTH}:${HEIGHT}"
)

for entry in "${PANELS[@]}"; do
  IFS=: read -r ID NAME W H <<< "$entry"
  OUTFILE="${OUTDIR}/panel-${ID}-${NAME}.png"
  URL="${BASE}/render/d-solo/agent-telemetry/agent-telemetry?panelId=${ID}&from=${FROM}&to=${TO}&width=${W}&height=${H}&scale=${SCALE}&tz=${TZ}"
  echo "Capturing panel ${ID} (${NAME})..."
  curl -sf -o "$OUTFILE" "$URL"
  echo "  → ${OUTFILE}"
done

# Capture full dashboard for README
FULL="${OUTDIR}/dashboard-full.png"
echo "Capturing full dashboard..."
curl -sf -o "$FULL" "${BASE}/render/d/agent-telemetry/agent-telemetry?from=${FROM}&to=${TO}&width=1800&height=1500&scale=${SCALE}&tz=${TZ}&kiosk"
echo "  → ${FULL}"

# Export the SQLite-specific PR scorecard drilldown for docs. The README hero
# (docs/assets/dashboard-full.png) is NOT written here: after the otel cutover
# (issue 0055) the hero is the OSS otel dashboard, owned by `make oss-screenshot`
# / e2e/oss-screenshot.sh. Writing dashboard-full.png from the SQLite render
# here would clobber that otel hero with the legacy SQLite image.
DOCDIR="docs/assets"
mkdir -p "$DOCDIR"
for pair in "2:pr-scorecard:dashboard-pr-scorecard"; do
  ID="${pair%%:*}"
  rest="${pair#*:}"
  PANEL_NAME="${rest%%:*}"
  DOC_NAME="${rest#*:}"
  cp "${OUTDIR}/panel-${ID}-${PANEL_NAME}.png" "${DOCDIR}/${DOC_NAME}.png"
done

echo "Done: ${#PANELS[@]} panels captured in ${OUTDIR}"
