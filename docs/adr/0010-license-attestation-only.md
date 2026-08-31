# ADR-0010: License is attestation only — never gate features

- **Status:** accepted
- **Date:** 2026-06
- **Deciders:** Fran Olivares
- **References:** licensing design (final decision); API/authz/audit contract (§7, §13.5)

## Context and problem statement

A commercial open-core product must decide what its license enforcement *does* at
runtime. The temptation is to gate features behind a license check. For a security
product that is also a potential authorization-bypass surface, and it conflicts with a
"pure dual license, nothing capped" philosophy.

## Decision drivers

- Do not cripple the open product.
- Do not make license validation an authz-bypass surface.
- Work air-gapped, with no license server.

## Considered options

- **Attestation-only license validation** that never blocks anything.
- **Feature-gating** by license tier.

## Decision outcome

Chosen option: **attestation only**. License validation records the holder and status
and is informative; it **never disables, degrades, or blocks** any request, module, or
boot **in the open (AGPL) binary**. Validation is **offline** (an Ed25519 signature; no
license server). The open binary is the complete CORE governance platform; the
commercial edition adds the separate, additive `enterprise/` add-ons (this is open
core, not "the same complete product" — see the amendment below and ADR-0011).

### Consequences

- **Good:** the open product is never crippled; license checks cannot be turned into an
  authorization bypass; the product runs air-gapped.
- **Bad / trade-offs:** commercial differentiation comes from the *license terms* and
  the separate `enterprise/` modules, not from gating the core.
- **Neutral:** license tests are fail-open by design.

## Why the alternatives were rejected

- **Feature-gating** — makes the open edition a worse product, erodes trust, and turns
  a spoofable license into a security-relevant gate. Rejected.

## Amendment (2026-06-23)

This ADR holds, scoped precisely: **the license key never gates the OPEN binary.** The
model is **open core** (ADR-0011 amendment), so the earlier phrase "the free and
licensed products are the same complete product" was inaccurate and is corrected above —
the commercial edition has the additive `enterprise/` add-ons (which were always "the
separate `enterprise/` modules" this ADR names). An attested claim is *consumed* only by
the closed enterprise build, and only to entitle those additive add-ons — a local decision
in the customer's own commercial build. Olivares's own cloud still must never trust the
self-reported status for billing.

**Amendment (2026-07-27, B10).** The one seat-shaped consumption this note used to
describe — `enterprise/seats` reading `license.Claims.MaxUsers` to lift a community
user-seat cap — is gone: self-hosted user accounts are UNLIMITED in every edition, the
cap of 3 was removed, and `MaxUsers` is display-only everywhere. That strengthens this
ADR rather than qualifying it: no build, open or commercial, now reads a license to cap
users. See `LICENSING.md` and the commercial pricing canon (maintained privately).
