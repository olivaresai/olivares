---
title: "Govern and approve (human-in-the-loop)"
description: "How an operator governs the estate: identity and permissions, the deny-by-default RBAC model, the restrict-only policy seam, and the human-in-the-loop posture where decisions are recorded in the audit ledger."
---

This page is for the operator who has connected at least one source and now needs
to **govern** the estate: decide who and what can act, review what the platform
surfaces, and act on it. Governance lives in **module VI (identity, permissions,
governance)**, sits on the same authorization core as the rest of the API, and is
**fully audited**.

:::caution[Honest scope: the approval engine is built; the operator console is still maturing]
What runs today is the **authorization core** — deny-by-default RBAC, a restrict-only
policy seam, tenant-scoped access, and an append-only signed audit ledger that records
every governance decision and every privileged read — **plus a working human-in-the-loop
approval engine**: governed approval requests bound to a plan hash, opened deny-closed and
time-boxed, with **separation-of-duty, duplicate-decider and expiry enforced server-side**,
and approve/deny endpoints under the governance module's namespace. What is **still
maturing** is the richer **operator review surface** — a full approval-queue console and
structured review UI. This page describes the model, the live endpoints and the
recorded-decisions guarantee; where the operator UI is still design-stage, it says so.
:::

## The authorization model you govern within

Every governance decision is made by the same authorization core that protects the
rest of the control plane. Understand its three properties before you change anything.

### RBAC is deny-by-default

Authorization runs **RBAC first**. A principal with no membership in a tenant is
**denied** — there is no implicit grant. Permissions are scoped to a tenant, and the
handler acts only on the **single tenant the request resolved to**, never one it
re-derives, which closes confused-deputy and IDOR classes by construction.

The built-in roles form a ladder of increasing capability:

| Role | What it can do |
|---|---|
| `viewer` | read operational data and the audit trail |
| `editor` | the above, plus write operational data |
| `admin` | the above, plus tenant IAM — users, memberships, tokens, settings |
| `owner` | all permissions within the tenant |

A module declares its own namespaced permissions (`<namespace>:<resource>:<verb>`),
and roles are granted those permissions **by verb tier** (viewer maps to read, editor
to write, admin and owner to admin). A new module therefore introduces governance
surface without an engine release.

:::note[Viewing the access graph is a privileged action — by design]
Module III's R/RW access map is the single most sensitive asset in the product: a map
of what every agent can touch is a recon roadmap to an attacker. So **reading the
access graph is a privileged action**, granted from the **editor role and up — never
the lowest viewer**. It is **tenant-scoped** (a read can only see one tenant's graph),
and **every read is written to the audit ledger** — who looked at whose access, and
when. Privilege, tenant scoping, and self-audit are layered deliberately; see the
[security model](/explanation/security/security-model/).
:::

### The policy seam (ABAC/PDP) only restricts

On top of RBAC, the operator may wire an external **policy decision point (PDP)** for
attribute-based rules. You choose the engine with a single environment variable:

```bash
# Choose one. Cedar is the embedded, pure-Go primary; OPA is an over-HTTP adapter.
OLIVARES_PDP_ENGINE=cedar   # or: opa | none
```

Both engines sit behind one seam, and the seam has one invariant that governs how you
must reason about it:

:::tip[The PDP can only take access away, never add it]
The policy seam composes as **RBAC ∩ native ABAC ∩ external PDP**, intersected. A PDP
**only restricts; it never widens** what RBAC already allowed. You cannot use a Cedar
or OPA policy to *grant* access that the role model denies — only to deny access the
role model would otherwise allow. This is enforced, not a convention.
:::

The two adapters preserve that invariant in different ways, and you author policy
accordingly:

- **Cedar (embedded, primary, pure-Go).** You write `forbid` rules. A rule that matches
  is a restriction; an empty rule set means the RBAC decision stands. A `permit` in Cedar
  can never widen the decision.
- **OPA (over HTTP).** Your Rego must be **permit-by-default** (`default allow := true`,
  with `allow := false` clauses for your denials). A `true` result means no restriction;
  `false`, a missing result, or any transport or non-2xx error **fails closed** — the
  request is denied.

An **invalid PDP configuration disables only the external PDP** and logs the fact —
native ABAC and RBAC keep governing. A misconfigured policy engine never leaves
requests ungoverned and never takes the control plane down. **Every restriction the
PDP applies is audited.**

## What surfaces tells you to act on

Human-in-the-loop governance is driven by what the platform observes and presents.
Two streams tell an operator what warrants a decision:

| Stream | Module | What it surfaces |
|---|---|---|
| **Least-privilege drift** | III (access map) | the **permitted-vs-observed** diff — a granted capability used in a way no one intended, or a path that is reachable but never exercised |
| **Findings** | IX (security, guardrails, forensics) | guardrail and red-team findings, plus the notification stream the platform routes |

Module III, the access map, is **read-first** — it observes through logs,
OpenTelemetry and (as a non-cooperative kernel backstop) eBPF, and is **never in the
agent's data path**, so a collector failure cannot break production. It is also
**minimal-data**: it stores the relation `agent → resource (read/write)`, never
payloads, secrets, or PII. The signal it carries is honest about its own confidence
(`attributed` vs `approximate`) and its own reach.

:::caution[Coverage is tiered — drift is not uniformly complete]
The access map's fidelity depends on the resource. Coverage is **tiered**: *clean* for
SQL databases, object stores and warehouses (native audit classifies read vs write
verbatim); *lossy* for stores like document and vector databases; and **impossible to
observe passively** for in-memory and embedded stores. Govern with this in mind: an
absence of observed access is not proof of no access where coverage is lossy or absent.
Read [the threat model](/explanation/security/threat-model/) for what each tier can and
cannot attest.
:::

One signal class needs explicit governance judgment. MCP tool annotations
(`readOnlyHint` / `destructiveHint`) are a useful read/write hint but are **untrusted
by the MCP specification** — clients must treat them as untrusted. The platform
**corroborates** them against trusted signals and never trusts them alone, and so
should you when acting on a drift item that rests only on an annotation.

## The human-in-the-loop posture

The intended governance loop is: **surfaces present** (drift from module III, findings
from module IX) → **an authorized operator decides** → **the decision is recorded in
the audit ledger**.

All three parts of that loop run today. **The surfaces are real** — module III produces
the permitted-vs-observed diff and module IX produces findings. **The approval engine is
real** — a governed approval request opens against the governance module (deny-closed,
plan-hash-bound, time-boxed); an authorized operator approves or rejects via the decision
endpoint, and **separation-of-duty, duplicate-decider and expiry are enforced
server-side** so the requester can never decide their own request and an expired one can
never bind. And **the recording is real and strong** — see the guarantee below. What is
**still design-stage** is the built-out **operator review console** — a rich
approval-queue UI; the endpoints and the engine are shipped, the polished review surface
is the path forward for module VI.

The dependency that makes this loop credible is **per-agent identity**. The platform's
audit attributes activity to a credential or role, not inherently to an agent; a shared
service account with a connection pool collapses attribution. Governing well therefore
means **issuing and enforcing identity per agent** — the bridge from observation
(module III) to governance (module VI). The identity side of this is built around
opaque, revocable first-party credentials and a roster of non-human identities; the
**only credential-minting primitive** in the product is opt-in, attested, audited, and
never persists the minted token. See the
[modules catalog](/reference/modules/overview/) for how identity, permissions and
governance compose across the estate.

:::tip[The recorded-decisions guarantee]
Whatever the depth of the workflow above it, **a governance decision is a recorded
fact**. Mutating actions are appended to the audit ledger with the **real actor** in
the **same transaction** as the change, and sensitive reads (the access graph, the
ledger itself) self-audit in a committed write. The ledger is **append-only,
hash-chained, and protected by Ed25519 signatures** — each record carries
`seq`, `prev_hash`, `hash` and `sig`, so rewriting history is cryptographically
detectable, and **it never contains PII**. You cannot make an ungoverned change that
the ledger silently forgets.
:::

### Get the record out of the box

For an external, immutable copy — the thing an enterprise auditor asks for that native
telemetry does not provide — the ledger is exposed as an **authenticated pull export**:

```bash
# Pull the signed, hash-chained ledger for offline re-verification.
# Requires a token whose role can read the audit trail (viewer and up).
curl -fsS "https://localhost:8443/v1/audit/export?format=cef" \
  -H "Authorization: Bearer $OLVK_TOKEN" \
  -H "X-Olivares-Tenant: $TENANT" >> /var/log/olivares/audit.cef
```

Supported `format` values are `cef`, `leef`, `syslog`, `otlp`, `otlp_envelope`,
`otlp_log_record` and `ocsf` — `otlp` emits the complete, postable export request,
`otlp_envelope` is an exact alias of it, and `otlp_log_record` is the bare
one-LogRecord-per-line projection. Every record
carries the chain-integrity fields so your SIEM or WORM store can **re-verify the chain
offline**. The detached signature defends against a DB-only compromise (injection, a
stolen backup or replica, an RLS-bypassing role) and against checkpoint deletion; an
**off-box copy** is the control against a fully compromised host. See
[forwarding audit to Splunk](/how-to/forward-audit-to-splunk/) for a complete
file-tail pipeline.

The least-privilege drift these decisions act on is the access map's
permitted-vs-observed result. The [zero-to-graph tutorial](/tutorials/zero-to-graph/)
walks through reaching it concretely on the demo estate; the access-map module surface
is subject to the same deny-by-default RBAC, tenant scoping and per-read auditing as
everything else, which is why reading it is an editor-and-up action.

## Where to go next

- [Security model](/explanation/security/security-model/) — privilege, tenant scoping,
  self-audit, and the minimal-data posture in full.
- [Threat model](/explanation/security/threat-model/) — the assets, trust boundaries,
  and what each coverage tier can attest.
- [Modules catalog](/reference/modules/overview/) — how identity, permissions and
  governance (module VI) compose with the access map (module III) and findings
  (module IX).
- [Connect a source](/how-to/connect-a-source/) — wire the signals that drift and
  findings are built from.
