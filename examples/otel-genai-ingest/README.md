# Ingest OpenTelemetry GenAI telemetry (any framework)

Send the OpenTelemetry **GenAI** semantic conventions (`gen_ai.*`) from *any*
OTel-instrumented agent — LangChain/LangGraph, CrewAI, the Microsoft Agent
Framework (AutoGen), Google ADK, the OpenAI SDK, … — and watch the token usage
become **attributed cost** in the control plane's FinOps. No Claude Code required:
this is the vendor-neutral on-ramp for the rest of your agent fleet.

> Everything below runs against the real `olivares` binary. The
> [`smoke.sh`](./smoke.sh) runs these exact steps in CI and asserts the result, so
> this example can't silently rot. Run it yourself:
>
> ```sh
> task build:bin              # the OTEL source ships as an embedded plugin
> examples/otel-genai-ingest/smoke.sh
> ```

## How it works

The Claude/OTEL source connector serves a standard **OTLP receiver** (gRPC `:4317`,
HTTP `:4318`). With the GenAI profile turned on it recognizes `gen_ai.*` records on
both traces and logs, normalizes the **three dialects** that coexist in 2026 fleets
(the legacy OpenLLMetry names, the deprecated v1.36 per-message events, and the
v1.37+ generation), and maps each operation to a cost sample and the agent/tool/
conversation access edges.

```
your agent (OTel SDK)  ──OTLP/HTTP gen_ai.* span──▶  olivares (Claude/OTEL source)
                                                            │ normalize + attribute
                                                            ▼
                                              FinOps cost ledger  ──▶  /v1/m/finops/spend
```

The profile is **opt-in** (`semconv_opt_in`) because the GenAI conventions are still
Development status — off, a `gen_ai.*` record still feeds the liveness watchdog but
is not mapped to cost/edges.

## 1. Install and create a tenant

A fresh install (same on-ramp as the [quickstart](../../docs-site/src/content/docs/start/quickstart.md)):

```sh
./bin/olivares serve --insecure --data-dir ./data &      # note the olst_… setup token
curl -sf -X POST localhost:8443/v1/setup \
  -d '{"token":"olst_…","email":"admin@local","password":"correct-horse-battery-staple"}'
TOKEN=$(curl -sf -X POST localhost:8443/v1/auth/login \
  -d '{"email":"admin@local","password":"correct-horse-battery-staple"}' | jq -r .token)
TENANT=$(curl -sf -X POST localhost:8443/v1/system/orgs -H "Authorization: Bearer $TOKEN" \
  -d '{"name":"Agents","slug":"agents"}' | jq -r .tenant_id)
```

## 2. Wire the OTEL source with the GenAI profile on

Point `OLIVARES_SOURCES_CONFIG` at a `claude`-kind source for your tenant and turn on
the profile, then restart:

```json
{ "sources": [ {
  "name": "genai-otlp",
  "kind": "claude",
  "tenant": "<your-tenant-id>",
  "config": {
    "semconv_opt_in": "gen_ai_latest_experimental",
    "enable_grpc": "false",
    "enable_http": "true",
    "http_addr": "127.0.0.1:4318"
  }
} ] }
```

```sh
OLIVARES_SOURCES_CONFIG=./sources.config.json \
  ./bin/olivares serve --insecure --data-dir ./data &
# log line: "ingest: wired source  name=genai-otlp kind=claude …"
```

## 3. Send a GenAI span and see the cost

Any OTel SDK exports this shape; here is one span as plain OTLP/HTTP JSON
([`span.json`](./span.json) — a `chat` operation, 1200 in / 350 out tokens, on
`openai`/`gpt-4o`, in conversation `demo-thread-1`):

```sh
curl -sf -X POST localhost:4318/v1/traces \
  -H 'Content-Type: application/json' --data-binary @span.json

curl -sf localhost:8443/v1/m/finops/spend/summary \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" | jq
# {
#   "samples": 1,
#   "input_tokens": 1200,
#   "output_tokens": 350,
#   "by_provider": [ { "key": "openai", … } ],
#   …
# }
```

The cost is recorded with `provenance: "estimated"` (a token signal, no billed
amount). Push the provider's billed `cost_report` (or live runtime cost) to
`POST /v1/m/finops/cost` to reconcile estimated-vs-billed.

## Notes for real instrumentation

- **Both name sets are read.** Emitters straddle the semconv rename, so the ingest
  accepts both the current `gen_ai.usage.input_tokens` / `output_tokens` and the
  deprecated `gen_ai.usage.prompt_tokens` / `completion_tokens`, and both
  `gen_ai.provider.name` and the older `gen_ai.system`. The current names win when
  both are present.
- **Spans *or* logs.** Frameworks put `gen_ai.*` on trace spans (LangChain, CrewAI,
  Google ADK) or on log records; both `/v1/traces` and `/v1/logs` are accepted, and
  the same operation arriving on a span *and* its `operation.details` log event
  (sharing a W3C span id) is costed once.
- **Protobuf too.** OTLP/protobuf works on the same paths; this example uses JSON so
  `curl` is enough. Point your collector/SDK exporter at `http://<host>:4318`.
- **If `/tmp` is `noexec`** (some hardened containers), set `TMPDIR` (or
  `OLIVARES_SMOKE_TMPDIR` for the smoke) to an exec-capable path — the engine
  extracts the embedded source plugin there before running it.

## References

- OTLP receiver + dialect normalization: `connectors/claude/otlp.go`, `connectors/claude/genai.go`
- Framework fixtures (proof of the shapes above): `connectors/claude/genai_frameworks_test.go`
- Spend analytics: `modules/finops/api.go`, `modules/finops/analytics.go`
- Dialect coverage (OTel GenAI semantic conventions): `connectors/claude/genai_dialect.go`
