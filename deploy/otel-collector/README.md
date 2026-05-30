# OTel Collector recipe (collector deployment)

This directory holds the **collector** deployment recipe for shipping
agent-telemetry events to an external observability backend (Datadog as the
reference). It complements the **direct** recipe, where the client talks to the
backend intake itself.

See `docs/spec.md` ## サーバ送信 for the export target contract and the
direct/collector trade-off, and
[issues/closed/0040-design-pluggable-otlp-export-backends.md](../../issues/closed/0040-design-pluggable-otlp-export-backends.md)
for the design rationale.

## When to use collector vs direct

| Scale | Recipe | Why |
|---|---|---|
| Personal / small team (main use case) | **direct** | No extra process. Point the client's OTLP exporter at the backend; keep a submit-only credential on your own machine. |
| Team / many clients | **collector** (this dir) | The Collector holds the backend credential in **one** place; **clients carry no backend secret**. It also converts encodings and fans out / buffers / retries. |

The collector recipe's hidden advantage: **clients keep the default
`encoding = "json"` exporter unchanged**. Datadog's direct OTLP Logs intake
requires protobuf, but the Collector's `datadog` exporter performs the
protobuf + `DD-API-KEY` conversion, so only the Collector needs that.

## What this recipe does

```
agent-telemetry flush (OTLP/HTTP JSON Logs)
        │
        ▼
  OTel Collector  ──►  Datadog (datadog exporter: protobuf + DD-API-KEY)
        │
        └────────────►  agent-telemetry-server /v1/logs (optional; SQLite + Grafana)
```

- `config.yaml` — OTLP receiver → `batch` → fan out to the `datadog` exporter
  and (optionally) the agent-telemetry-server via `otlphttp/server`.
- `docker-compose.yaml` — runs `otel/opentelemetry-collector-contrib` locally.

## Run

```fish
set -x DD_API_KEY <your-datadog-api-key>
set -x DD_SITE datadoghq.com   # or datadoghq.eu, us5.datadoghq.com, ...
# optional second destination (SQLite/Grafana path):
set -x AGENT_TELEMETRY_SERVER_URL https://telemetry.example.com
set -x AGENT_TELEMETRY_SERVER_TOKEN <server-token>

docker compose up
```

Then add a client export target pointing at the Collector (no Datadog key on the
client):

```toml
[[export]]
id = "collector"
endpoint = "http://collector-host:4318"   # base URL; client appends /v1/logs
token = "${COLLECTOR_TOKEN}"              # whatever auth you front the Collector with
# encoding defaults to "json" — unchanged from the legacy [server] path
```

For a **Datadog-only** deployment, leave the `AGENT_TELEMETRY_SERVER_*` vars
unset and remove the `otlphttp/server` exporter from `config.yaml`'s `exporters`
and the `logs` pipeline.

## Out of scope here

- **Attribute reshaping** (rename / resource enrichment / dropping
  high-cardinality identifiers) for event-level analysis — child issue **0044**
  ships the Collector processor sample.
- **facet / measure promotion** — that is a Datadog-side index setting a
  Collector processor cannot replace (0040 B); it is a Datadog-side artifact
  regardless of recipe.
- **`pr_metrics` gauge** (OTLP Metrics) — child issue **0043**.
