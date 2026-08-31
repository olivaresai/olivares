<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# Agent safety case — forward-looking evidence bundle

> **Status: forward-looking artifact**, offered because sophisticated buyers have
> started asking for safety-case-shaped arguments for agentic deployments. It is a
> structured argument template **instantiated per deployment**, not a certificate.
>
> **Format provenance (verified 2026-06-12):** "Safety Cases: A Scalable Approach
> to Frontier AI Safety" (Hilton, Buhl, Korbak, Irving — arXiv:2503.04744) defines
> a safety case as *"a structured argument, supported by a body of evidence, that
> provides a compelling, comprehensible and valid case that a system is safe for a
> given application in a given environment"* (UK MoD definition) and describes a
> three-role process: a **writer** develops the argument, a **red team** attacks
> it, a **decision-maker** rules on it. That paper is about frontier AI in general
> and proposes no concrete template; the template lineage we follow is Goemans et
> al., "Safety case template for frontier AI: A cyber inability argument"
> (arXiv:2411.08088), adapted from inability/control arguments to **control-plane
> containment** arguments. Notation is CAE-style (Claims–Arguments–Evidence).

## Top-level claim

> **C0.** Agents operating under this Olivares deployment cannot take a
> consequential action outside their granted scopes without it being (a) denied,
> (b) gated on a human, or (c) recorded tamper-evidently and interruptible.

This is a **control/containment** argument (the system of controls is safe), not an
*inability* argument about any model's capabilities — we make no claims about the
models themselves.

## Sub-claims and evidence

| Claim | Argument | Evidence (live) | Known assumptions / residual risk |
|---|---|---|---|
| **C1 Containment** — actions outside permitted scope are denied | Deny-closed policy enforcement at the tool/exec boundary; permitted-vs-observed drift detects scope creep the policy missed | PEP gates; `GET /v1/m/accessmap/drift`; connector trust (exec deny-closed) | Observe-only surfaces don't enforce (they detect); coverage tiers are honest: some stores are passively unobservable |
| **C2 Oversight** — consequential actions gate on humans | Deny-by-default HITL; quorum + SoD + anti-self-approval; break-glass exists but is audited, floored (CRITICAL ≥2) and excluded where prohibited | `modules/governance/approvals.go` | Approval fatigue is a real attack (OWASP T10); mitigations: guardrail detection + dedup, but not eliminated |
| **C3 Interruptibility** — a human can stop the estate or one agent, now | Graduated kill switch (estate/agent), checked **before** HITL; fail-closed stop gates per module; structural two-person re-enable | Kill-switch state + stop gates | Stop halts actuation; in-flight external side effects can complete |
| **C4 Traceability** — every action is attributable and tamper-evident | Append-only hash-chained ledger, per-event Ed25519, offline verify; session recording with payload-hash anchoring; WORM archival | Ledger verify | Pre-shred backups & provider-side copies have documented retention floors (`docs/RIGHT-TO-ERASURE.md`) |
| **C5 Adversarial robustness** — known agentic attacks are detected/tested | Guardrail detection mapped to OWASP ASI01–ASI10 + T1–T15; red-team probes run only in the isolated sandbox; eval regression gates | Findings; red-team module; catalog crosswalks | Prompt injection is mitigated, not solved — industry-wide; detection latency is non-zero |
| **C6 Supply-chain integrity** — what runs is what was signed | Signed releases + SBOM + SLSA Build L3 (SLSA v1.2) + reproducible builds; deny-closed signed **model** admission | `scripts/verify-release.sh` | Customer-side image admission policy is the customer's to enforce |

## The three roles, honestly assigned

- **Writer:** Olivares.AI (this template + per-deployment instantiation from the
  machine-readable evidence endpoints).
- **Red team:** **currently unfilled by an independent party** — the in-product
  red-team module attacks guardrail surfaces, but an independent human red team of
  the *argument* has not been engaged (it would naturally ride the first
  third-party pen-test — see [penetration-testing.md](./penetration-testing.md)).
- **Decision-maker:** the buyer's risk owner. This bundle gives them the argument
  and the evidence pointers; the acceptance decision is theirs, and the residual
  risks column is written so it can be rejected on the merits.

> **Founder decision recorded (2026-07-18):** none was ever required to publish this
> template, and the independent red-team review of the argument rides the pen-test
> contracting decision — which that date **deferred**, directing internal, automated
> adversarial campaigns before the public release instead. So the honest status of the
> independent review of this argument is *deferred with the external engagement*, not
> *awaiting a decision*: the platform's own red-team module is what exercises the
> guardrail surfaces meanwhile, and it does not review the argument.
