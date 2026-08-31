---
title: Honesty & limits
description: What Olivares AI does today, what is design-stage or post-v1, and
  what the product deliberately does not do. No fabricated capabilities.
slug: 2026-06/start/honesty-and-limits
---

:::caution[Archived snapshot — read these corrections with it]
This is the **2026-06 pre-1.0 docs snapshot**, kept as a dated record of what the
documentation said then. Its body is never rewritten, so read these corrections with it:

- **The community user cap described below was removed on 2026-07-27** (decision B10).
  Self-hosted user accounts are **unlimited in every edition**, whatever the licence
  state; `user_cap_requires_enterprise` no longer refuses anything, and no build reads a
  licence to cap users. (The page says "two accounts" in one place and the README of the
  time said "three" — both figures are dead.)
- **The enterprise threat-intel add-on is not a live "feed".** It ships a base catalog
  compiled into the build plus optional signed, versioned feed artifacts the operator
  pins a key for and applies. Olivares operates no curated distribution and publishes no
  release cadence.

The current text is in the **Latest** version of this page.
:::

A control plane for AI is a security product. If it overstates what it covers, it
gives a false sense of safety — which is worse than no tool at all. So this page is
the explicit contract about **what runs today, what is planned, and what is out of
scope on purpose.** The rest of the documentation holds to it: commands in tutorials
and how-to guides are meant to be run as written, and where the product does not yet
cover something, the page says so rather than implying it does.

## What runs today

* **The single binary builds, boots and reaches a populated access graph.** The
  `olivares` binary compiles to one static artifact with the web UI embedded.
  Booting it with the demo estate (`serve --seed-demo`) and walking
  *discover → R/RW graph → Permitted-vs-Observed drift → inventory* is exercised
  **end-to-end** by the test suite. The [tutorial](/2026-06/tutorials/zero-to-graph/)
  reproduces exactly that path.
* **First-run setup is credential-free.** A fresh install has **no default
  credentials**; the engine prints a one-time, single-use setup token on first boot.
* **The REST API and audit ledger are real.** The [API reference](/reference/api/)
  is rendered from the product's own OpenAPI 3.1 contract. The audit ledger is
  append-only and hash-chained with Ed25519-signed checkpoints, and can be exported
  in several SIEM formats.
* **Releases are signed and verifiable offline.** Signature, SLSA provenance, SBOM
  and OpenVEX can all be [verified without network access](/2026-06/how-to/verify-a-release/),
  and the product ships an [air-gap bundle](/2026-06/how-to/air-gap-install/).

## Open core — what is open vs enterprise

The product is **open core**: the default (AGPL) binary is the whole governance
platform, and a small, **additive** commercial line (`enterprise/`, built only with
`-tags enterprise`, never in the public binary) holds the reserved features. Two
boundaries matter for day-to-day use, and the open build answers for them honestly
rather than faking them:

* **SSO is open for a single IdP.** Single-IdP login — **OIDC** (Authorization Code
  + PKCE) and **SAML 2.0** (signed responses, anti-replay) — runs in the default
  binary with **no** `-tags enterprise`. Running **more than one active IdP**
  (per-tenant / by-domain), **SSO-enforcement** (require-SSO / block password login)
  and **managed SCIM** are the reserved enterprise line; activating a second active
  IdP returns `multi_idp_requires_enterprise` — an explicit product limit, never a
  fake 501.
* **The community build is capped at two user accounts** — the bootstrap superadmin
  plus one more. Provisioning a further account returns `user_cap_requires_enterprise`;
  the enterprise build lifts the cap to the seat count attested by a commercial
  license. This is a **fair-monetization edition boundary, not a security control**:
  existing accounts over the limit are never removed, and because the open binary is
  AGPL the check lives in open source and a fork could remove it — the deterrents are
  the AGPL/commercial terms and the missing closed add-ons, never a claim of
  tamper-proofing.
* **The rest of the platform is open.** The full governance loop — inventory, the
  R/RW access map, RBAC/ABAC/Cedar policy, the sealed audit ledger, FinOps,
  compliance, SIEM egress, MCP, HA/distributed — runs in the open binary with no
  license check. The additive `enterprise/` add-ons (multi-IdP federation, content
  firewall/DLP, hook hardening, the threat-intel feed, server-tool egress, the
  CyberArk Conjur connector, the incident close-loop, and the seat entitlement) are
  new code that was never in the open product, not features removed from it. License
  validation in the open binary is **attestation-only** — it never enables, disables
  or blocks anything (see
  [Open core & licensing](/2026-06/explanation/open-core-and-licensing/)).

## What is design-stage or pre-1.0

Olivares AI is **pre-1.0**. The product design documents are explicit that much of
the platform is at the design stage in parts even where the engine already runs.
Treat module-level depth as **work in progress** unless a page states otherwise.

* **Coverage of the R/RW map is tiered, by design.** Fidelity depends on what the
  source can prove. It is **clean** on stores with native audit (SQL via pgAudit,
  object storage via CloudTrail, warehouses/lakes), **lossy** on some stores
  (document/vector), and **impossible to reconstruct passively** on others (e.g.
  Redis, SQLite, D1) — where read vs write cannot be determined, the edge is marked
  `unknown`. Attribution is **firm** when a source carries per-agent identity and
  collapses to **`approximate`** when a shared service account hides it. The product
  shows these honestly; it does not fabricate certainty.
* **The canonical R/RW sources are wired in the stock `serve`.** The composition
  root registers the host-level observers — `pgaudit`, `s3cloudtrail`, `ebpf`,
  `runtime` and the `mcp` introspection source — alongside the warehouse/lake
  observers (snowflake/databricks/bigquery/mssql/oracle/mongo/redshift/gcs/
  azure-blob/iceberg/openlineage/delta-sharing), all configurable through
  `OLIVARES_SOURCES_CONFIG` (this follow-up has since been completed; the
  [quickstart](/2026-06/start/quickstart/) wires a real `pgaudit` source against the stock
  binary and the smoke test asserts it). The knowledge **document sources**
  (gdrive/confluence/notion/sharepoint/s3content) are deliberately *not* runtime
  sources — they are loaded on demand by knowledge ingest requests. The
  [connectors reference](/2026-06/reference/connectors/) marks each kind.
* **The default is a single binary; the distributed event bus exists and is honest
  about its semantics.** The default runs as one binary with an **in-process** event
  bus. The **remote collector→core data path is built and shipped**: edge collectors
  run source connectors locally and push observations to a central core over
  verified-client-cert mutual TLS, with no inbound listener (the `collector` mode).
  The **distributed event bus** shipped with the scale-out work: a hybrid
  that keeps the in-process fan-out for local delivery (blocking backpressure, no
  local loss) and bridges events across nodes over **NATS**, enabled by
  `OLIVARES_BUS_CONFIG` (a misconfigured bus config **fails the boot** rather than
  silently partitioning the bus). Cross-node delivery is documented honestly as
  **at-most-once** — bridge disconnects and drops are counted in dedicated metrics,
  never silent ([monitoring](/2026-06/how-to/monitor-with-prometheus/)).
* **Governed *actuation* has three honest states: live, on-demand, and seam.** The
  product observes and governs broadly today. A small set of actuations is **live in
  the default binary** with no provisioning: FinOps budget enforcement (an enforcing
  budget at its cap denies the spend), the notification dispatch transport (it routes
  once a destination is configured), the security detective findings/guardrails, and
  the in-process synthetic sandbox runner (isolated by construction). Several more are
  **wired on-demand** — the backend is built and wired, but stays **deny-closed or
  degraded until an operator provisions it** via env config: module VII (deploy)
  `apply`/`retire` (a `503` until an executor is provisioned), module IV orchestration
  *fire* and module XVI voice dispatch (both deny-closed until a dispatcher is
  configured), the OS-isolated sandbox/red-team runtime (synthetic / DEGRADED until
  provisioned), model-backed **semantic** retrieval (lexical and public-only by
  default), and model *execution* in module X (`503` until an inference credential is
  provisioned). What stays a **declared, deny-closed seam** with no backend at all is
  the dormant voice telemetry probe (the distributed event bus left this list when
  the NATS bridge shipped — see above). The
  [modules catalog](/2026-06/reference/modules/overview/) marks each module's Govern/Observe
  and Actuate status; nothing claims to act where it does not. (This corrects an
  earlier reading that listed voice, the sandbox/red-team runtime and semantic
  retrieval as "live" — they are on-demand: verified against a stock `serve --seed-demo` boot, 2026-06-08.)
* **Air-gap applies to the control plane, not to Claude inference.** The control plane
  runs fully self-hosted and can be air-gapped (SQLite single-node, signed offline
  release, air-gap bundle). **Claude itself is not self-hostable** — Anthropic does not
  publish weights — so any Claude *inference* reaches Anthropic's API, directly or via
  Bedrock/Vertex/Foundry. "Air-gapped" here means the *governance and observation* plane
  and its data stay inside your perimeter; it does **not** mean Claude runs offline.
  Models you genuinely self-host (e.g. via vLLM/Ollama under module XXIII) can run
  air-gapped; brokered frontier models cannot.
* **Module routes are not all in the public API contract.** Some module endpoints
  (for example the access-map graph and drift) are reachable but are *deliberately*
  not part of the served OpenAPI document; their field-level contracts live in the
  product's typed interfaces. The [API reference](/reference/api/) documents the
  core REST surface; it is not the whole product surface.

## What the product deliberately does **not** do

* **No offensive features.** Olivares AI is **not** a command-and-control framework
  and does **not** scan other people's credentials. The access map is a powerful
  reconnaissance tool *for defenders to govern their own estate* — viewing it is a
  privileged, tenant-scoped, fully-audited action. This defensive line is
  intentional and kept explicit (see the
  [threat model](/2026-06/explanation/security/threat-model/)).
* **No native Splunk S2S forwarder.** Forwarding to Splunk is a documented *posture*
  — point a Universal Forwarder at a file the control plane appends, or push over
  Splunk HEC — **not** a native Splunk-to-Splunk emitter. The
  [Splunk how-to](/2026-06/how-to/forward-audit-to-splunk/) is explicit about which stream
  is which.
* **No outbound webhooks in the REST contract.** The OpenAPI document defines no
  `webhooks`. Outbound signed delivery exists as an internal notification
  *destination connector*, and the SCIM Security-Event-Token endpoint is an
  *inbound* receiver — neither is an OpenAPI webhook. See the
  [API reference](/reference/api/).
* **Model fine-tuning (module XXIII) is post-v1.** Its absence is a decision, not a
  gap.

## Where the docs note a gap upstream

A few things this documentation surfaces are **gaps in the product**, reported to
the teams that own the relevant contract rather than papered over here:

* The committed OpenAPI file the site renders is now **regenerated from — and
  CI-checked byte-for-byte against — the engine's own generator**, so it no longer
  lags it (the earlier endpoint gap was reconciled). The earlier under-documentation
  of the `/v1/audit/export` format list was also fixed upstream: the summary and the
  bad-request message now both name `cef|leef|syslog|otlp|ocsf` — this section keeps
  the record because earlier editions of these docs reported the gap.
* The audit-ledger **push** path shipped with the SIEM/ITSM interop work: an
  `audit.recorded` eventing subscription turns on a per-tenant ledger pump that
  forwards sealed records **at-least-once** to a configured sink (Splunk HEC,
  Sentinel, Datadog, New Relic, or an HMAC-signed webhook). The **pull** export
  remains the right shape for WORM archival and offline re-verification. See
  [push to your SIEM](/2026-06/how-to/cookbook/push-to-siem/) and the
  [Splunk how-to](/2026-06/how-to/forward-audit-to-splunk/). What still does **not** exist
  is a native Splunk **S2S-protocol** emitter (below).

If you find a command that does not behave as documented, that is a bug in the docs
or the product — please report it.
