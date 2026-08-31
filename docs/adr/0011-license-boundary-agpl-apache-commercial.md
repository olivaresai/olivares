# ADR-0011: License boundary — AGPL product, Apache SDK/connectors, commercial enterprise

- **Status:** accepted
- **Date:** 2026-06
- **Deciders:** Fran Olivares
- **References:** licensing design (final decision); stack license boundary

## Context and problem statement

The product needed a licensing model that keeps the product genuinely open, keeps a
third-party connector ecosystem free of copyleft friction, and leaves a clean
commercial path — without feature-gating (see ADR-0010).

## Decision drivers

- A genuinely open, copyleft product (not source-available, not crippled).
- A permissive connector ecosystem so third parties extend it freely.
- A clean commercial exception for those who need it.

## Considered options

- **Pure dual license:** AGPL product + Apache-2.0 SDK/connectors + commercial exception.
- **Feature-gated open core** (MIT/Apache core + paid features).
- **Permissive everything** (MIT/Apache core).
- **Source-available** (BSL, SSPL, PolyForm).

## Decision outcome

Chosen option: a **pure dual license**. `core/`, `modules/`, `web/` are
**AGPL-3.0-only**; `sdk/` and `connectors/` are **Apache-2.0**; `enterprise/` is
**commercial** (`LicenseRef-Olivares-Commercial`). The boundary is enforced from
commit one by per-file SPDX headers and a CI check: an Apache-2.0 connector **never**
imports the AGPL engine.

### Consequences

- **Good:** the product is genuinely open and copyleft; connectors stay permissive and
  frictionless; the boundary is mechanically enforced; a commercial path exists without
  capping anything.
- **Bad / trade-offs:** contributors must keep SPDX headers correct and respect the
  import boundary (CI catches violations).
- **Neutral:** the commercial exception is self-serve plus an enterprise contact.

## Why the alternatives were rejected

- **Feature-gated open core** — caps the product (see ADR-0010), rejected.
- **Permissive everything** — gives the core away with no commercial footing.
- **Source-available (BSL/SSPL/PolyForm)** — not OSS; kills the adoption that the
  connector ecosystem depends on.

## Amendment (2026-06-23) — the model is open core

The **license boundary above is unchanged and correct**: `core/`+`modules/`+`web/` are
AGPL-3.0-only, `sdk/`+`connectors/` are Apache-2.0, `enterprise/` is commercial, and an
Apache connector never imports the AGPL engine. What is corrected is the *framing*: the
shipped product is **open core** (the GitLab `ee/` model), **not** a "pure dual license"
with no feature differences. The AGPL build is the complete governance platform and is
never crippled from within to upsell — but it is **not identical** to the commercial
edition: the `enterprise/` line (multi-IdP federation, content firewall/DLP, hook
hardening, threat-intel feed, server-tool egress, CyberArk Conjur, incident close-loop)
is **additive new code that
was never in the open build** (no rug-pull). So "considered/chosen: pure dual license"
should be read as the AGPL/Apache *frontier* decision; the open-vs-commercial *edition*
decision is open core. See `LICENSING.md`

The license **boundary** here is not superseded. What changed separately is the
**distribution** of the commercial line: the `enterprise/` source no longer ships in the
public repo — it moved to a private repository so the build-tag gating is real, not
cosmetic. That is a distribution decision, recorded in **ADR-0020**; the boundary and the
attestation-only license (ADR-0010) are unchanged.

## Amendment (2026-07-28) — two dead claims in the 2026-06-23 note

The boundary and the open-core framing above stand. Two items in the enterprise list of
the 2026-06-23 amendment no longer describe the product; the note itself is left exactly
as written, because it is a dated record of what was believed then.

1. **"the seat entitlement that lifts the community user cap" no longer exists.** Decision
   B10 (2026-07-27) removed the user cap outright: self-hosted accounts are unlimited in
   every edition, `core/auth.CommunitySeatLimit` is `0`, `enforceSeatCapTx` is an
   unconditional no-op, and no build — open or commercial — reads a license to cap users.
   Current decision: the commercial pricing canon (maintained privately) (`self_hosted.users: unlimited`) and
   `LICENSING.md`
2. **"threat-intel feed" is not how the add-on may be sold.** `enterprise/threatintel`
   ships a base catalog compiled into the build, plus optional signed, versioned feed
   artifacts the operator pins a publisher key for and applies; Olivares operates no
   curated feed distribution and publishes no release cadence. The commercial canon
   (the commercial pricing canon (maintained privately), `self_hosted.business.preset`) forbids marketing it as a
   "feed" unless a signed feed is actually operated. The operator CLI keeps the word for
   the artifact it verifies and applies (`olivares threatintel verify|apply|pull`) — that
   is the artifact's name, not a claim about who publishes it.

Neither item touches the license boundary this ADR decides.
