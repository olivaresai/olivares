---
title: Glossary
description: >-
  The product's vocabulary, precisely: the access map and its honesty axes,
  the observation kinds, the governance primitives, and the operational
  terms — each defined the way the engine actually uses it.
---

Terms are defined as the engine uses them — several are deliberately
narrower than their industry usage, and the narrowness is the point.

### Access map (R/RW map)

Module III's graph of **origins** (agents, identities, sessions) and the
**resources** they touch, every edge classified by [mode](#mode) and tagged
with its [signal source](#signal-source), [attribution](#attribution-confidence)
and [coverage tier](#coverage-tier). A key differentiated capability — one of the 30
modules, not the whole product. See [What is Olivares AI?](/start/what-is-olivares-ai/).

### Actuation states: `v1` / `on-demand` / `seam`

The three honest states of every module's *acting* half. **`v1`** — live in
the default binary with no provisioning. **`on-demand`** — built and wired,
but deny-closed or degraded until an operator provisions it (deploy
apply/retire, orchestration fire, voice dispatch). **`seam`** — a declared
interface with no backend. The [modules catalog](/reference/modules/overview/)
marks every module; a regression guard in CI keeps the table honest.

### Agent

An AI system (a coding agent, a service agent, an orchestrated workflow
step) governed as a first-class entity, distinct from the
[identity](#identity--nhi) (credential) it runs as. Binding agents to
identities is what sharpens [attribution](#attribution-confidence).

### Agent sprawl

The analyst term for AI agents, copilots and MCP servers proliferating
across an organization faster than anyone keeps an inventory — unknown
agents with unknown access. It is the problem the
[access map](#access-map-rrw-map) and discovery exist to make visible. See
[Analyst vocabulary](/explanation/positioning/analyst-vocabulary/).

### AI TRiSM

*AI Trust, Risk and Security Management* — a framework **coined and owned by
Gartner** for governing the trust, risk and security of AI. We map our
capabilities to its **themes** (governance, runtime inspection, runtime
enforcement, information governance); we do **not** reproduce Gartner's exact
model, claim conformance, or imply endorsement — the taxonomy is Gartner
proprietary research. See
[Analyst vocabulary](/explanation/positioning/analyst-vocabulary/).

### Approval (HITL)

A governed request to perform a gated action, opened **deny-closed and
time-boxed**, bound to the exact plan, decided by authorized humans with
separation-of-duty and expiry enforced server-side, and recorded in the
[ledger](#audit-ledger). See the [recipe](/how-to/cookbook/hitl-approvals/).

### Attribution (confidence)

How firmly an observed access is tied to a *specific* origin:
**`attributed`** (a per-agent identity is in the trail) or
**`approximate`** (inferred — a shared service account, a lossy store, a
kernel process not yet bound to an agent). The map shows the level instead
of fabricating certainty; the console also renders attributed edges as
*firm*. Upgrading attribution is an identity problem:
[SSO/SCIM & identity sources](/how-to/connectors/sso-scim-identity/).

### Audit ledger

The append-only, hash-chained record of every governance decision and every
privileged read, protected by Ed25519 signatures — each record carries
`seq`, `prev_hash`, `hash`, `sig`, so rewriting history is cryptographically
detectable. It never contains PII. Exposed as a pull export, a push sink,
and offline verification (`olivares audit verify`).

### Break-glass

A governed, audited emergency elevation for *specific* gated actions —
deliberately **not** available for everything: re-enabling a
[kill switch](#kill-switch) or finalizing an identity's lifecycle can never
be broken-glass into.

### Checkpoint

A signed anchor over a tenant's ledger chain, written on an interval
(default 1h). An **off-box** copy of the checkpoint and the public key is
what makes verification attacker-resistant after a host compromise.

### Collector

The push-only edge process (`olivares collector`) that runs
[sources](#source) near the observed systems and pushes observations to the
core over gRPC (optionally mTLS). Collectors have **no inbound listener**.

### Cooperative path

Observation that depends on the agent reporting — OTLP telemetry, hooks.
Highest fidelity when present, structurally evadable, which is why the
[kernel backstop](#kernel-backstop) and store-native audit exist beside it.

### Coverage tier

The fidelity of a *resource's* signal, orthogonal to attribution:
**clean** (native audit classifies R/W verbatim — pgAudit, CloudTrail),
**lossy** (edges land but imprecisely), **opaque / impossible passively**
(no usable passive audit surface — the product says so instead of guessing);
**mixed** marks an edge built from more than one tier.

### Demo estate

The synthetic estate `serve --seed-demo` loads through the **real** event
bus (loopback-only, public source-tree password, refuses non-loopback
binds). A learning tool, never an install path.

### Destination (output connector)

The delivery half of the connector catalog: Slack, Teams, PagerDuty,
webhook, Splunk HEC, ServiceNow, Jira, email and peers — they deliver
findings and notifications, and have no coverage tier because they observe
nothing.

### DR bundle / KEK

The encrypted, **ledger-continuity-safe** backup `olivares dr backup`
produces; sealed under a key-encryption key (passphrase-derived or
KMS-provided) that must travel separately from the bundles.
See [backup & restore](/how-to/backup-and-restore/).

### Drift (least-privilege drift)

The diff between [Permitted and Observed](#permitted-vs-observed): the gap
between granted and exercised access. Three classes — **unexpected access**
(observed, never granted), **unused grant** (granted, never observed),
**reconciliation pending** (observed, identity link unresolved).
[Triage recipe](/how-to/cookbook/drift-triage/).

### Edge / cost / finding

The **closed set** of observation kinds a source can emit: an access
relation, a usage-cost fact, or a detective finding. Closed by design — a
connector cannot invent new kinds, which is what keeps the minimal-data
contract enforceable.

### Estate

Everything you govern in one deployment: the agents, identities, MCP
servers, models, resources and their relations, across all your
organizations.

### Finding

A guardrail / posture / red-team / forensic observation, carrying a hash of
any sensitive detail rather than the detail. Routed on the notification rail
and to [SIEM sinks](/how-to/cookbook/push-to-siem/).

### Guardian agent

**Gartner's** term for AI that monitors or intervenes on *other* AI agents.
Olivares AI delivers the **governance outcome** of the category — observe,
diff permitted-vs-observed, gate deny-closed, record immutably — but as a
**read-first control plane outside the data path**, not an inline LLM
standing guard. See [Analyst vocabulary](/explanation/positioning/analyst-vocabulary/);
contrast the in-product [guardian loop](#guardian-loop).

### Guardian loop

A governance rule that watches findings and engages containment
automatically — including the [kill switch](#kill-switch) — with the
auto-path going through exactly the same gate as a human stop.

### Identity / NHI

A credential-bearing principal: human, or **non-human identity** (service
accounts, workload identities, API keys, agent identities). Rosters arrive
from [identity sources](/how-to/connectors/sso-scim-identity/); binding them
to agents is the bridge from observation to governance.

### Kernel backstop

The non-cooperative observation path: Tetragon captures kernel file/network
events outside the agent's control; the `ebpf` source consumes its export.
Always [`approximate`](#attribution-confidence) until an identity binds the
process to an agent. See [eBPF/Tetragon](/how-to/connectors/ebpf-tetragon/).

### Kill switch

The estate (or per-agent) emergency stop: one admin-tier call kills every
governed actuation, fail-closed; re-enabling requires two distinct humans
plus a post-review, with no break-glass around it.
[Drill recipe](/how-to/cookbook/kill-switch-drill/).

### MCP annotation

A server's self-declared `readOnlyHint` / `destructiveHint` — **untrusted by
the MCP specification**, ingested only as a declared-capability hint
(`approximate`, neither observed nor permitted), corroborated and never
trusted alone. See [MCP governance](/how-to/connectors/mcp-governance/).

### Minimal data

The wire-level property that observations carry identifiers and
classifications, never payloads, SQL bodies, prompts, secrets or PII. A
property of the connector vocabulary, not a setting.

### Mode

An edge's read/write classification: `read`, `write`, `readwrite`, or
`unknown` — taken verbatim from the signal and **never inferred**; `unknown`
is an honest answer, not a missing one.

### Observed / Permitted

See [Permitted vs Observed](#permitted-vs-observed).

### Opaque tokens

The product's credentials: random, revocable, server-side-validated tokens
(`olvs_…` sessions, `olvk_…` API keys, `olst_…` the one-time setup token) —
deliberately not JWTs, so possession of a signing key can never mint access.

### Organization (tenant)

The isolation boundary. Every module read and write is tenant-scoped; on
Postgres, row-level security backstops it (the engine refuses to run as a
role that could bypass RLS).

### Permitted vs Observed

The two halves the access map diffs: **permitted** edges come from declared
grants and policy; **observed** edges from telemetry and native audit. The
diff is [drift](#drift-least-privilege-drift).

### Sealed admission

The deny-closed trust gate for out-of-process connector plugins: pinned
digest + Sigstore attestation verified against operator-pinned trust
anchors, with no escape hatch.
See [build a connector](/how-to/build-a-connector/).

### Setup token

The single-use `olst_…` token printed to stdout on first boot — the entire
bootstrap credential story; there are no default credentials. Only its hash
is stored.

### Signal source

Which observer produced an edge: `pg_audit`, `cloudtrail`, `otel`, `ebpf`,
`mcp_annotation`, a declared policy grant, an A2A signal. Provenance is
never collapsed: a pgAudit READ and an MCP hint are not the same evidence.

### Sink

An eventing subscription that delivers events to a SIEM in its dialect
(Splunk HEC, Sentinel DCR, Datadog, New Relic, or a generic HMAC-signed
webhook), in OCSF/CEF/LEEF/syslog/OTLP/JSON.
See [push to SIEM](/how-to/cookbook/push-to-siem/).

### SLI / SLO

The published service levels: availability via `/readyz`, request success,
API and ingest latency p99 — with single-node and HA tiers stated
separately and honestly.
See [monitoring](/how-to/monitor-with-prometheus/).

### Source

An observation connector: it `Open`s with config, `Gather`s observations
into the engine's sink, and `Close`s. Engine-owned scheduling, minimal-data
vocabulary, Apache-2.0, never imports the core.
See [connect a source](/how-to/connect-a-source/).

### Stop gate

The enforcement check every governed actuation makes against the
[kill switch](#kill-switch) state — checked before any other gate, failing
**closed** (the inverse of the budget check, which fails open: a broken
meter must not cause an outage, but a broken stop check must).
