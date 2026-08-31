<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# Machine-readable evidence (FedRAMP-20x-style, without the FedRAMP claim)

> FedRAMP 20x's durable idea — verified against fedramp.gov on 2026-06-12 — is
> that security evidence should be **machine-readable, continuously validatable,
> and programmatically accessible** (Key Security Indicators as JSON; the
> Authorization Data Sharing standard requires providers to share authorization
> data "in both human-readable and machine-readable formats" with "documented
> programmatic access"). We adopt that model for **commercial buyers**.
>
> **What we are NOT claiming:** any FedRAMP status. As of 2026-06-12, 20x is in its
> Phase 2 pilot (GA rules announced for end of June 2026), and — decisively —
> FedRAMP's *Minimum Assessment Scope* places software "delivered separately for
> installation on agency systems and not operated in a shared responsibility
> model … **entirely outside the scope of FedRAMP**". A self-hosted Olivares
> deployment is that case. FedRAMP/ATO pursuit remains explicitly out of the
> roadmap (post-v1 posture); if a managed (SaaS) offering ships later,
> this position must be re-evaluated, because the scope analysis flips.

## The evidence API surface (pull this, don't read prose)

All endpoints are tenant-scoped, RBAC-gated, and the sensitive reads are
themselves audited (reading evidence leaves evidence).

| Evidence | Endpoint | Formats |
|---|---|---|
| Framework catalog (26 framework catalogs, version-pinned, with disclaimers) | `GET /v1/m/compliance/frameworks` | JSON |
| Live control status / gap analysis per framework | `GET /v1/m/compliance/frameworks/{id}/status` · `…/gaps` | JSON |
| Posture summary | `GET /v1/m/compliance/summary` | JSON, CSV |
| Sealed evidence packages (append-only, ledger-anchored) | `GET /v1/m/compliance/evidence/{id}/export` | JSON, CSV, **OSCAL** (NIST 1.1.3) |
| Regulatory calendar (dates as data: source + verified-on + status) | `GET /v1/m/compliance/calendar` | JSON |
| ICT-risk register (DORA-shaped, self-audited read) | `GET /v1/m/compliance/dora` | JSON |
| Risk classifications | `GET /v1/m/compliance/risk` | JSON |
| Residency attestation | `GET /v1/m/compliance/residency` | JSON |
| Model AIBOM (sealed) / SPDX AI profile | `GET /v1/m/models/owned-models/{id}/aibom` | CycloneDX 1.6; `?format=spdx` → SPDX 3.0.1 JSON-LD |
| Model card | `GET /v1/m/models/owned-models/{id}/model-card` | JSON; `?format=md` |
| Signed-admission verdicts | `GET /v1/m/models/model-admissions` | JSON |
| GPAI supplier posture (operator-verified) | `GET /v1/m/models/gpai-posture` | JSON |
| Inventory/posture export (control towers) | `GET /v1/m/posture/export` | JSON |
| Least-privilege drift | `GET /v1/m/accessmap/drift` | JSON |

Release-side machine-readable evidence (per release, offline-verifiable):
SBOM (SPDX + CycloneDX, in-toto attestation) · OpenVEX · SLSA Build L3 (SLSA v1.2) provenance ·
cosign signatures · checksums — verified end-to-end by `scripts/verify-release.sh`
(keyless or air-gapped modes).

## Continuous-validation pattern (KSI-style)

A buyer's GRC pipeline can poll the status endpoints and alert on regression —
the same loop 20x KSIs formalize. Suggested minimal probe set: framework `status`
(per framework you care about), `drift` (least privilege), ledger verify, release
verification on upgrade. The product's own honesty rules guarantee the signal is
meaningful: operational capabilities report only against **real tenant evidence**;
design-only evidence reports `by_design`, never `satisfied`.

> **Founder decision (recorded, not pending):** no FedRAMP/ATO pursuit pre-v1.
> Revisit only with a managed offering or a concrete federal deal that justifies
> the multi-year cost.
