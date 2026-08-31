---
title: "Inline inference proxy (PEP for /v1/messages)"
description: >-
  An optional, opt-in policy-enforcement point that fronts the Claude
  /v1/messages contract for raw SDK and curl callers, applying residency,
  model-access, context-window, DLP and budget in-band before forwarding —
  closing the ANTHROPIC_BASE_URL bypass — with tamper-evident recording
  anchored before the forward by default, and the exceptions to that stated on
  the page rather than discovered. The config and DLP authoring are live; the
  listener is unmounted until an operator provisions it.
---

The inline inference proxy is the enforcement point for inference traffic that is **not**
Claude Code — raw SDK and `curl` callers hitting the Claude `/v1/messages` contract directly.
The decision is taken at the composition root (`cmd/olivares/inferenceproxy.go`);
`modules/inferenceproxy` owns the per-tenant governance config and the inference-egress DLP
policy that decision reads, and decides nothing about a live request itself. Server-managed
settings cannot reach that traffic: a custom `ANTHROPIC_BASE_URL` bypasses them
entirely. The proxy fronts `api.anthropic.com` and runs a governed pipeline
**in-band** — residency, [model-access](/reference/modules/x-models/), DLP and
the content gates, then the context-window sizing and budget — before any byte
forwards on `/v1/messages`. Recording is **pre-forward by default**: the
authorized intent is written to the tamper-evident ledger *before* the forward,
and no evidence means no forward (deny-closed). A tenant can turn that off
deliberately (`record_mandatory: false`), and then evidence is anchored
**post-forward, best-effort and loud** — a failed anchor is reported, never
hidden.

That default used to be the other way round, and the difference was not
academic: a tenant that has never opened the config page is precisely the one
nobody has reasoned about, so anchoring it best-effort made the evidence
guarantee opt-in for everyone who had opted into nothing. Two limits are worth
stating plainly rather than discovering. First, the posture governs the
**pre-forward** moment only — after the forward the call has happened and no
posture can undo it, so that path is a loud gap by construction. Second, an
operator who has set the audit spool to `degrade` has already said what should
happen when it is exhausted: for a tenant that never chose an evidence posture,
that declared `degrade` wins and the call is forwarded with a recorded gap. A
tenant that explicitly set `record_mandatory: true` is refused instead — its own
choice outranks the spool's. The `count_tokens` sizing
pre-flight is itself provider egress, so it runs only **after** every local
content gate has passed: a DLP- or firewall-denied prompt is never transmitted,
not even for counting. The proxy is one of the **four deny-closed PEPs** the
platform ships.

## Maturity, stated plainly

**PARTIAL.** The split is honest and deliberate:

- **LIVE** — the per-tenant governance config and the inference-egress DLP
  policy: authoring, persistence and audit. Two stores under
  `/v1/m/inferenceproxy/`: a singleton `config` (per-gate toggles, the
  proxy-down fail posture, the response-DLP mode, the recording mandate) and a
  `dlp/rules` set (one rule per sensitivity class → `allow`|`deny`).
- **OPT-IN, unmounted by default** — the actual `/v1/messages` listener. It
  defaults to **loopback** (`127.0.0.1:8448`) and an operator can configure
  another bind address explicitly; it defaults **fail-CLOSED** (a proxy that
  cannot decide must not forward), and it is not mounted unless an operator
  provisions it.

This module **decides nothing** about a live request. It is the durable,
console-authorable policy the composition root reads via `Policy()`; the
decision is composed from existing seams (`EvaluateModelAccess`, `CheckBudget`,
residency, `ClassifySensitivity`, the context-window check) at the edge.

## The in-band pipeline

Each gate defaults **enabled**, and each stays inert under its own native
opt-in until configured — DLP until the first rule, model-access until the first
grant, residency only when a region is pinned, budget until an enforcing budget
exists. A tenant relaxes a specific gate explicitly, and the audit records who
opened the perimeter. Writing the DLP egress policy is **admin-tier**:
authorizing what content may leave is a privileged governance change.

## Bounded context

- **Minimal data by construction.** No row it persists — config, DLP rule,
  audit — ever carries a prompt, response, secret or matched PII value. Bytes
  the proxy inspects in flight are fingerprinted (SHA-256) and anchored to the
  ledger by the composition root, never stored here.
- It is the **third leg** of the proxy: the protocol shell (parse, forward,
  tee the bodies) is the identity-blind Apache connector; the governed decider
  is the engine. This module owns only the policy both consult — keeping the
  decision out of the open-core connector boundary.

## Related

- [Module X — model & provider management](/reference/modules/x-models/) — the
  model-access and per-surface context-window policy this proxy enforces.
- [Run Claude Code with Olivares](/how-to/run-claude-code-with-olivares/) — the
  governed Claude Code path, which the in-process hook covers; this proxy is the
  fallback PEP for callers that path cannot reach.
- [Honesty & limits](/start/honesty-and-limits/) — what is live, opt-in or
  design-stage across the platform.
