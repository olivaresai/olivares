<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# ADR-0026: AP2 payment mandates as Cedar scoped grants (governed procurement)

- **Status:** proposed (design only; the enterprise build lands in a separate phase)
- **Date:** 2026-07-20
- **Deciders:** Fran Olivares
- **References:** ADR-0019 (Cedar scoped grants), ADR-0022 (source-scoping subject axes),
  ADR-0025 (FinOps reserve→commit/release ledger, TOCTOU-safe), ADR-0009 (append-only
  hash-chained audit); the companion AP2 governed-payment threat-model spec; the AP2 v0.2.0
  specification (github.com/google-agentic-commerce/AP2, verified 2026-07-20).

## Context and problem statement

Agentic payments are arriving as a protocol layer. Google's **AP2 (Agent Payments
Protocol)** is one of the most visible; its current specification is **v0.2.0 (released
2026-04-28)** and it was donated to the FIDO Alliance the same day. AP2 lets a user delegate
a signed **mandate** to a shopping agent, which the agent later binds to a concrete
transaction that **Verifiers** (merchant, credential provider, network, payment processor)
check.

Two facts fix the shape of this decision:

1. **Currency (measured reality wins over the plan).** Earlier planning drew on AP2 v0.1 and
   described an *Intent / Cart / Payment* mandate triple signed by "verifiable credentials".
   That model is **superseded**. v0.2 defines exactly **two** mandate types — **Checkout
   Mandate** and **Payment Mandate** — each in an **Open** state (constraint-bearing,
   user-signed) and a **Closed** state (transaction-bound; the agent generates a Key Binding
   JWT / Proof-of-Possession over the key in the open mandate's `cnf` claim). Mandates are
   **SD-JWTs** (RFC 9901); the **binding hash / Key Binding JWT MUST use a non-deterministic
   scheme (ES256/ECDSA) and NOT a deterministic one (Ed25519)** — the spec states this guards
   the hash binding. This ADR targets **v0.2**, pinned to the published `vct` schema suffixes
   (per the v0.2 spec, `mandate.checkout.1` / `mandate.payment.1`; verify against the spec's
   `docs/ap2/*` at build time).

2. **What Olivares is — and is not.** Olivares is a **governance control plane**: a Policy
   Decision Point (PDP) and a tamper-evident evidence ledger. It is **not** a payment
   processor, PSP, card network, wallet, or fund custodian, and this ADR does not make it
   one. AP2 itself is **pre-1.0** with **early, largely aspirational adoption** (PayPal's own
   pages mention AP2 only taxonomically and emphasize OpenAI's ACP + Google's UCP; Mastercard
   "Agent Pay" is a distinct program; the "60+ organizations" figure is a Sept-2025 launch
   count; the FIDO signatory list is ~12). Honest labelling forbids claiming AP2 support
   beyond what is verifiable.

The problem: **how does Olivares govern an AP2-mediated agentic purchase using the primitives
it already has, born with a concrete enterprise use case, and covering the gaps AP2
deliberately leaves to the layer above it — without introducing an authorization
fall-through or a silent constraint downgrade?**

The concrete use case this design is born with: a **governed procurement agent** — an
enterprise buys through an agent operating under an AP2 open mandate whose constraints encode
the purchasing policy (budget ceiling, allowed suppliers, per-item limits, recurrence,
execution window); Olivares authorizes each concrete purchase against that policy, escalates
the high-value ones to a human, and seals the mandate+receipt as non-repudiable evidence.

**Precondition (in-path gate).** Every guarantee below holds only where the deployment routes
the purchase **through Olivares as an in-path gate** — the agent MUST obtain a fresh Olivares
authorization before presenting a closed mandate to the settlement layer. As a side/advisory
PDP, Olivares can no more reach a closed mandate already handed to a merchant than AP2 can.
The build MUST document this deployment requirement.

## Decision drivers

- **Reuse the existing authorization plane, do not fork it** — but only where the semantics
  actually match (see the Abstain-vs-deny correction below).
- **Cover AP2's stated gaps at our layer** (see the companion threat-model spec): AP2 has
  **no revocation**, makes verifier-side double-spend rejection **optional (MAY)**, does
  **not** prove human identity / SCA, is **silent on clock trust**, and leaves evidence
  retention/retrieval and liability out of scope. A PDP that "assumes all agents are potential
  attackers" (AP2's own threat model) must make these mandatory.
- **Fail closed on anything unmodellable.** A constraint we cannot encode, a disclosure the
  agent withholds, an unknown algorithm — each must reject the mandate, never widen it.
- **Honest scope and pre-1.0 risk.** Design now, pin to `vct`, don't ship claims we cannot
  verify, keep Olivares strictly on the PDP/evidence side of the line.

## Considered options

- **Option A — AP2 mandates as Cedar scoped grants; Olivares as the governing Verifier/PDP.**
  Model an AP2 **open mandate** as an authored **Cedar grant** (ADR-0019) bound to that one
  mandate, whose `when` conditions are the mandate constraints; treat a **closed mandate** as
  an **authorization request** (principal = the agent key in `cnf`; action = `purchase`/`pay`;
  resource = the payee / checkout) evaluated **deny-by-default for payment actions**. Olivares
  runs AP2's verification rules as the PDP, gates high-value ones through the single-use HITL
  approval, reserves FinOps budgets (ADR-0025) fail-closed, and seals the full signed
  mandate+receipt as evidence.
- **Option B — a bespoke AP2 mandate engine parallel to Cedar.**
- **Option C — watch only.**

## Decision outcome

Chosen option: **Option A**, because the constraint model maps onto Cedar grant conditions
and the surrounding controls (approvals, reserve ledger, signed audit chain) already exist —
**provided the three semantic corrections below are made**, without which the reuse is
unsafe.

### The three semantic corrections that make the reuse sound

1. **Payment actions are DENY-BY-DEFAULT, not abstain-defers-to-RBAC.** The scoped-grant
   engine returns **`EffectAbstain`** (not deny) when no permit matches — "no grant",
   "expired grant", and "no scoped grants for tenant" all Abstain, and Abstain means *the base
   RBAC decision stands* (`modules/governance/grants.go:31-38`, the RBAC back-compat
   invariant). Naively equating "no matching mandate" with "deny" is **wrong**: a cnf
   mismatch, an expired mandate, or a revoked grant would Abstain and could fall through to an
   **RBAC allow**. Correction: `purchase`/`pay` are authorized **only** by a matching, valid,
   mandate-bound grant, with **no RBAC fallback**. The build MUST enforce this by either
   (i) proving the base authorizer grants no `purchase`/`pay` permit to any role (so
   Abstain→deny), or (ii) a payment overlay that treats Abstain on a payment action as deny.
   A present-but-invalid mandate additionally authors an explicit **`forbid`**. A conformance
   test MUST assert RBAC alone never authorizes a payment.

2. **The mandate→grant translator FAILS CLOSED on any unmodellable constraint.** "Unknown
   constraint MUST fail" is a **translation-time** obligation, not something Cedar
   deny-by-default provides: if the translator silently omits a constraint it cannot encode,
   it produces a grant **broader than the user signed** and Cedar allows because it never saw
   the constraint. Correction: translate against an **allowlist** of recognized constraint
   keys, operators, and units; on any unrecognized element, **reject the entire mandate and
   author no grant**.

3. **Full disclosure is mandatory; the untrusted agent cannot withhold a constraint.** In
   SD-JWT the *holder* (the untrusted agent) chooses which disclosures to reveal. It could
   present only the disclosures that pass and withhold a tighter constraint. Correction: the
   verification adapter enumerates the `_sd` digests and, if any digest for a policy-relevant
   claim is **undisclosed**, treats it as an unevaluable constraint and **fails closed**.

### Correspondence (with the corrections applied)

| AP2 v0.2 concept | Olivares primitive (file:line) |
|---|---|
| Open mandate (constraints, user-signed) | Cedar scoped **grant** bound to that mandate's `jti`/`sd_hash` (`modules/governance/grants.go:67`, ADR-0019) |
| Closed mandate | Authorization **request**, evaluated **deny-by-default for `purchase`/`pay`** (correction 1) |
| "Verification and Processing Rules" | Adapter chain-verify + full-disclosure check (correction 3) + fail-closed translate (correction 2) + PDP decision |
| `payment.budget` (cumulative) / `amount_range` (per-tx) | FinOps reserve ledger (`modules/finops/budgets.go`, `spendlimits.go`, ADR-0025) with a **net-new per-mandate reservation key**; reserve against the mandate cap AND all Olivares scopes atomically (NOT `min()`) |
| `payment.agent_recurrence` (count/velocity) | **Net-new** count/velocity limiter (TOCTOU-safe under ADR-0025) — NOT an existing amount-based budget |
| `allowed_payees` / `allowed_merchants` / `allowed_payment_instruments` | Cedar set-membership `when` conditions |
| `execution_date` {not_before,not_after} | Temporal condition against the **DDIL trusted signed dead-man clock** (`modules/governance/ddiladopt.go`), injected into the SD-JWT adapter too |
| User approval; high-value gating | **Single-use HITL** approval consume (`modules/governance/approvals.go`) |
| Checkout/Payment Mandate + Receipt (dispute evidence) | Hash-chained **runtime audit ledger** keyed by `transaction_id` (`modules/sessions/runtime_ledger.go`, `sc.Audit().Append`, ADR-0009) — see decision 1 on WHAT is stored |

### The decisions this ADR makes

1. **Mandate representation — authority vs evidence are distinct stores.**
   - **Authority** is the **Cedar grant** (the evaluated policy), bound to the specific open
     mandate's stable id (`jti`/`sd_hash`) so a closed mandate can only be evaluated against
     the grant authored from *its* open mandate (prevents **mandate substitution**: an agent
     holding a lax mandate A cannot get a B-closed-mandate evaluated against grant-A). The
     grant is **never** the raw blob treated as self-asserted authority.
   - **Evidence** is the **full signed artifact**: the open SD-JWT, the closed Key Binding
     JWT, and the **disclosures actually presented** — retained (encrypted, access-controlled)
     so a dispute can *replay AP2's signature-verification sequence*, which a hash cannot. This
     evidence carries PII (amounts, payees), so it is **encrypted minimal-necessary evidence,
     not "never PII"** — the minimal-data rule applies to the *authority/grant* and to
     operational logs, not to the sealed dispute record.

2. **Signature verification — chain, with pinned algorithms and separated trust roots.**
   Verify the SD-JWT chain and the open→closed link via the `cnf`-bound Key Binding JWT (PoP),
   confirm the closed mandate preserves the open mandate's claims unchanged, and evaluate every
   constraint (corrections 2 and 3). Two hardening rules the raw spec does not give:
   - **Algorithm pinning.** Bind each trust-root key to its permitted algorithm set and verify
     strictly against it; **ignore the token's advertised `alg`**. Reject `alg:none`, HS/ES
     confusion, and curve/strength downgrade — AP2's Ed25519-ban is one narrow rule inside a
     header-controlled negotiation surface the untrusted agent drives.
   - **Separated trust roots.** The **User-Credential** root (OpenID4VP) verifies that the
     *human authorized* the open mandate; the **Trusted-Agent-Provider** list governs only
     which agent identity may **hold/bind** the `cnf` key. They attest different facts and are
     **both required on their own obligation** — never an interchangeable OR (an agent-provider
     attestation is not a substitute for the user's authorization signature). Deny-closed if
     the required root is absent.

3. **Expiry, single-use, and revocation (scoped to Olivares-gated flows).** AP2 has **no
   revocation**. Olivares closes this for **in-path** deployments: (a) the mandate-bound grant
   is **first-class revocable** — revoking it makes every *future Olivares authorization* for
   that mandate deny-by-default (correction 1); it cannot reach a closed mandate already
   released to settlement (same limit as AP2 — stated honestly). (b) A high-value closed
   mandate consumes a **single-use approval** so an approval cannot be replayed. (c)
   `exp`/`execution_date`/recurrence are enforced against the **DDIL trusted signed clock**,
   and the SD-JWT adapter takes its `now` from that same clock so the two layers cannot
   disagree.

4. **Replay / double-spend — verifier-side de-dup is MANDATORY (in-path).** AP2 puts the
   anti-double-spend MUST on the *shopping agent* (an attacker in its own threat model) and
   makes the verifier check only a MAY. The Olivares PDP tracks presented closed-mandate
   nonces / `transaction_id`s per open mandate and refuses overlapping/repeated presentations
   — for authorizations that route through Olivares (the in-path precondition).

5. **What Olivares does NOT do.** No fund custody, no payment execution, no card/token
   issuance, no acting as PSP/network/wallet. Olivares is the **PDP** authorizing the agentic
   purchase against policy and the **evidence plane** sealing the mandate/receipt. Settlement
   stays with the merchant/PSP/network.

### Consequences

- **Good:** reuses Cedar/reserve-ledger/approvals/audit-chain where the semantics genuinely
  match; AP2's gaps become enforced guarantees; sealed non-repudiable evidence; honest,
  verifiable positioning.
- **Bad / trade-offs:** the reuse is **conditional** — it needs a payment-action
  deny-by-default overlay, a fail-closed translator, full-disclosure enforcement, a per-mandate
  reservation key, and a net-new recurrence limiter (none free); AP2 is pre-1.0 (a v0.3 will
  force a re-map, isolated behind the adapter and pinned to `vct`); retaining signed evidence
  with PII adds an encryption/retention obligation.
- **Neutral / follow-ups:** agent-to-agent mandate delegation is **out of AP2 scope** → out of
  ours; x402 (crypto-rail AP2 extension) and ACP (OpenAI/Stripe) are separate and tracked, not
  built here.

## Why the alternatives were rejected

- **Option B (bespoke engine)** — rejected: it duplicates the reserve-ledger/approval/audit
  machinery for a pre-1.0 protocol; the corrections above show the reuse is sound once the
  payment-action deny-by-default and fail-closed translation are in place.
- **Option C (watch only)** — rejected: the ratified direction is to design now and start the
  enterprise build early *without blocking the public release*. Watch-only would forfeit the
  differentiator (governed agentic spend with sealed evidence) while the standard consolidates
  at FIDO. The honest-labelling concern is met by shipping **design** now and gating the
  **build** behind verified need, not by doing nothing.
