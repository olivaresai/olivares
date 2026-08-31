<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# Monitoring artifacts — SLO alerting, dashboards & status page

Operational glue that turns the published SLOs (`docs/17-PRODUCTION-READINESS-SLO.md`) into something that pages on-call and shows the buyer real uptime. These are **portable, decoupled artifacts** (not chart templates), so they install on any Prometheus/Alertmanager and don't perturb `deploy/manifests/install.yaml` (the `manifests:check` drift guard).

| File | What it is | How to use |
|---|---|---|
| `olivares-slo.rules.yaml` | Prometheus recording + multiwindow/multi-burn-rate alert rules, wired to the real `olivares_*` series. | `kubectl apply -f` (prometheus-operator `PrometheusRule`), or lift `spec.groups:` into a non-operator Prometheus `rule_files:`. |
| `status-page.gatus.yaml` | Self-hostable status/uptime page (Gatus) that blackbox-probes `/readyz` + `/livez` and computes uptime%/SLA. | Set `CONTROL_PLANE_HOST`, pin the probe CA, run `gatus --config …`. The authoritative availability source (a down engine can't report itself). |
| `dashboards/olivares-overview.json` | Grafana dashboard: operational overview — uptime, request throughput, error rate, store health, ingest pipeline, runtime. | Import into Grafana 10+ (Dashboards → Import → Upload JSON), or provision via Helm ConfigMap (see below). |
| `dashboards/olivares-policy-compliance.json` | Grafana dashboard: policy & compliance — login attempts, rate limiting, audit checkpoint health, gRPC enforcement. | Same. |
| `dashboards/olivares-performance.json` | Grafana dashboard: performance deep-dive — HTTP/gRPC/ingest latency distributions, event bus queue saturation, NATS bridge health, durable bus. | Same. |

## Scraping `/metrics`

`/metrics` is the engine's native Prometheus text exposition endpoint (format version `0.0.4`) served on **:8443 over HTTPS** (`core/api/metrics.go`). It exposes only structural engine counters/gauges — request rates, in-flight, ingest throughput, Go runtime, store reachability — never tenant data, tokens, or any value useful for recon (OBS-06; `docs/SECURITY-HARDENING.md`).

### Basic scrape config (no auth)

```yaml
scrape_configs:
  - job_name: olivares
    scheme: https
    tls_config:
      insecure_skip_verify: true  # or ca_file: /path/to/ca.crt
    static_configs:
      - targets: ["olivares.example.com:8443"]
```

### With bearer token auth

When `OLIVARES_METRICS_TOKEN` is set on the engine, the `/metrics` endpoint requires a matching `Authorization: Bearer <token>` header. Configure the scraper:

```yaml
scrape_configs:
  - job_name: olivares
    scheme: https
    tls_config:
      insecure_skip_verify: true
    authorization:
      type: Bearer
      credentials: "your-metrics-token-here"
      # or credentials_file: /path/to/token-file
    static_configs:
      - targets: ["olivares.example.com:8443"]
```

### With CIDR allowlist

When `OLIVARES_METRICS_ALLOWED_CIDRS` is set (comma-separated CIDRs), the engine checks the direct peer IP. Ensure your Prometheus scraper's IP is within the allowed ranges. Both token and CIDR can be combined (AND logic).

### Prometheus Operator (ServiceMonitor)

The Helm chart ships a `ServiceMonitor` (gated, `values.yaml metrics.serviceMonitor.enabled`):

```yaml
metrics:
  serviceMonitor:
    enabled: true
    interval: 30s
    # When the engine requires a bearer token:
    bearerTokenSecret:
      name: olivares-metrics-token
      key: token
```

## Grafana dashboards

Three pre-built Grafana dashboards are provided in `dashboards/`. All use template variables (`$datasource`, `$namespace`, `$instance`) for portability across environments.

### Manual import

1. Open Grafana → Dashboards → Import
2. Upload the JSON file (or paste its content)
3. Select your Prometheus data source
4. Save

### Helm chart provisioning (Grafana sidecar)

The Helm chart can provision dashboards as ConfigMaps labeled for the Grafana sidecar ([kiwigrid/k8s-sidecar](https://github.com/kiwigrid/k8s-sidecar)):

```yaml
grafana:
  dashboards:
    enabled: true
    label: grafana_dashboard      # matches kube-prometheus-stack default
    labelValue: "1"
    annotations: {}               # e.g. grafana_folder: "Olivares"
```

The sidecar watches for ConfigMaps with the configured label and mounts them into Grafana's provisioning directory automatically.

### Dashboard contents

| Dashboard | Metrics consumed | Key signals |
|---|---|---|
| **Overview** | `olivares_store_up`, `olivares_uptime_seconds`, `olivares_http_requests_total`, `olivares_http_requests_in_flight`, `olivares_ingest_*`, `olivares_build_info`, `go_*` | Store health, request rate by status/method, error ratio, ingest pipeline, memory, goroutines, GC |
| **Policy & Compliance** | `olivares_auth_login_attempts_total`, `olivares_http_ratelimit_*`, `olivares_audit_checkpoint_*`, `olivares_grpc_requests_total` | Login success/failure, rate-limit decisions, audit checkpoint freshness/failures, gRPC error ratio |
| **Performance** | `olivares_http_request_duration_seconds`, `olivares_grpc_request_duration_seconds`, `olivares_ingest_duration_seconds`, `olivares_eventbus_*`, `olivares_durablebus_*` | HTTP/gRPC/ingest latency quantiles + heatmap, bus queue depth/saturation, NATS bridge health, durable bus throughput |

## Metrics access control

The `/metrics` endpoint supports two layers of application-level access control, configurable via environment variables:

| Variable | Effect |
|---|---|
| `OLIVARES_METRICS_TOKEN` | Static bearer token. When set, scrape requests must present `Authorization: Bearer <token>`. |
| `OLIVARES_METRICS_ALLOWED_CIDRS` | Comma-separated CIDRs (e.g. `10.0.0.0/8,172.16.0.0/12`). Checks the direct peer IP (not `X-Forwarded-For`). |

When **both** are set, a request must satisfy **both** (AND logic). When **neither** is set, the endpoint is unauthenticated (the default — rely on network-level controls).

## Tier note

The SLO alert thresholds default to a **99.9%** budget (SRE Table 5-8). The published single-node tier is **99.5%** — see the header of `olivares-slo.rules.yaml` for the ×5 budget scaling. Latency/store-down alerts are absolute and apply to both tiers.

See `docs/STATUS-AND-INCIDENT-COMMS.md` for severities, the comms process, and templates; `deploy/runbooks/` for what to do when these alerts fire.
