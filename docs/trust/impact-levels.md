<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# DoD Impact Levels — where a self-hosted control plane fits, honestly

> **Status: informational mapping, NOT a DoD Provisional Authorization and NOT an accreditation.**
> Impact Levels categorize **cloud service offerings** for DoD use; they are defined by the DISA
> Cloud Computing SRG family. Primary source re-read on **2026-07-09**: *Cloud Computing Mission
> Owner SRG — Overview*, DISA, **30 January 2025** (DoD Cyber Exchange, dl.dod.cyber.mil / DCCS
> library). Olivares is not a cloud service offering — it is software the Mission Owner installs
> and operates inside an environment that already has its authorization. So the honest question
> this page answers is: **what does the product bring to a system that must live at each IL, and
> what necessarily comes from the environment instead.**

## The Impact Levels as the SRG actually defines them (verbatim, 2025 edition)

The 2025 SRG restructure categorizes information into **Impact Levels 2, 4, 5 and 6** — §3.3.3,
*Choosing the Information Impact Level*:

- **IL2** — *"Non-Controlled Unclassified Information"*
- **IL4** — *"Controlled Unclassified Information"* (CUI)
- **IL5** — *"Controlled Unclassified Information requiring additional protection, including
  Unclassified National Security Systems (NSSs)"*
- **IL6** — *"Classified Information up to Secret"*

**There is no IL3** (and no IL1 for commercial CSOs): the January 2025 Mission Owner SRG contains
zero occurrences of "IL3" — the historical level 3 was folded into today's IL4 years ago. Any
vendor material speaking of "IL3 support" is out of date. The 2025 updates also removed the old
"IL5 (non-NSS)" option: IL5 is the unclassified-NSS/CUI-plus level, full stop.

## What Olivares contributes per IL — and what is the environment's job

The pattern to internalize: **the product inherits the environment's IL; it never confers one.**
What it adds is the AI-governance control set inside that boundary
(see [dod-zero-trust-mapping.md](./dod-zero-trust-mapping.md) for the capability detail).

| IL | What the deployment looks like | What the product verifiably brings | What ONLY the environment can bring |
|---|---|---|---|
| IL2 | Self-host in any DoD-fit cloud/on-prem enclave | Full product; TLS-on defaults, audit ledger, scoping | Hosting authorization, CSSP monitoring |
| IL4 | Self-host inside an IL4-authorized environment | Everything above **plus** FIPS build variant (below), STIG-hardened image, CUI-appropriate DLP/labeling hooks (`modules/security/contentfilter.go`, doc ACL refs) | The IL4 PA of the hosting CSO/enclave, CAP connectivity, incident reporting chain |
| IL5 | Self-host inside an IL5 environment (unclassified NSS) | Same binary/controls; personnel-independent evidence (audit chain, SIEM push); credential-by-reference so secrets stay in the environment's approved store (`core/secret`) | U.S.-persons staffing rules the SRG sets for IL5 (control parameter PS-3(4): *"Users: U.S. citizens, U.S. nationals, or U.S. persons"*), NSS categorization, environment PA |
| IL6 | **Air-gapped / disconnected self-host** — the case this product was shaped for | Single static binary with **no mandatory outbound calls at runtime**; fully offline operation; updates as **signed air-gap bundles verified offline** (`cmd/olivares/cmd_upgrade_source.go:31,111-140`, trust = *"the OFFLINE signature, not the transport"* `cmd/olivares/cmd_upgrade.go:42`); offline docs; deny-by-default egress for isolated runs | SIPRNet connectivity/accreditation, classified handling, physical/personnel security — everything SECRET actually means |

## FIPS 140-3 — the exact, current claim (re-verified 2026-07-09)

IL4+ expects FIPS-validated cryptography operating in FIPS mode; Olivares pins CMVP module #5247
(`GOFIPS140=v1.0.0`). Our claim is precise and deliberately narrow
(`Dockerfile.fips`, `docs/SCP-09-FIPS-STIG.md`):

- The FIPS image builds with **`GOFIPS140=v1.0.0`** — the Go Cryptographic Module v1.0.0,
  **CMVP certificate #5247: FIPS 140-3, overall Level 1, status ACTIVE** (validated 2026-04-27,
  vendor Geomys, sunset 2031-04-26). Re-verified against the CMVP certificate page **today,
  2026-07-09** — not quoted from an older audit.
- We deliberately do **not** build with the newer `v1.26.0` module: it remains on the CMVP
  Modules-In-Process list (not validated). When its certificate issues, the build flag moves.
- Context every assessor should know: **on 2026-09-22 all FIPS 140-2 certificates move to the
  CMVP Historical List** (NIST FIPS 140-3 Transition Effort page, updated 2026-04-13, re-read
  today: *"All FIPS 140-2 certificates are placed on the Historical List"*; historical modules
  remain usable *"for existing systems"* but stop satisfying new-procurement requirements).
  Olivares' FIPS variant is already on a **140-3** certificate — this transition does not
  degrade our claim.
- Honesty notes: Level 1 (software module) — not Level 2+; the FIPS build is a **separate,
  opt-in artifact**, not the default binary; FIPS mode covers the Go crypto the binary performs,
  not crypto done by external systems it talks to.

## STIG posture — what exists and what does not

- `Dockerfile.stig` builds a hardened image variant; `oscap/` carries the OpenSCAP scan pipeline
  (`oscap/scan.sh`, `oscap/tailoring.xml`) so the hardening is **evaluated, not asserted**.
- **No product-specific STIG exists** for Olivares (DISA writes STIGs; vendors don't
  self-issue). What we provide: container/GPOS-baseline hardening plus the OpenSCAP evidence
  trail an ISSM can review. Calling this "STIG-compliant" without that nuance would overclaim;
  we don't.

## Declared out of scope (so nobody has to ask)

- **FedRAMP ATO / Marketplace** — see [fedramp-ready-posture.md](./fedramp-ready-posture.md):
  today's self-hosted product is *"entirely outside the scope of FedRAMP"* (FRR-MAS) by
  definition, and no authorization of any future cloud offering is claimed or pending.
- **Link-16 / MIL-STD tactical data links** — out of scope, permanently, as a claim: fielding
  those requires government test/certification regimes (spectrum/EMC certification via the
  cognizant program office and NTIA equipment authorization — processes that bind the *system*,
  including software, not just radios). Olivares governs IP-based AI estates; it has no MIL-STD
  data-link stack and will not pretend one via a connector.
- **"Satellite integration" (Starlink/pLEO/private 5G)** — these are **IP transport** under the
  product; there is nothing satellite-specific to integrate. What matters operationally —
  disconnected, denied, intermittent, limited bandwidth — is DDIL engineering: local-first
  operation, offline queues, reconciliation. We market DDIL behavior, not satellite logos.
- **IL6 accreditation itself** — we supply the air-gap-fit software above; the accreditation
  belongs to the deploying program.

*Primary sources, all re-read 2026-07-09: DISA Cloud Computing Mission Owner SRG Overview
(30 Jan 2025) via the DoD Cyber Exchange DCCS library; NIST CMVP certificate #5247; NIST FIPS
140-3 Transition Effort (csrc.nist.gov, updated 2026-04-13); FedRAMP FRR-MAS.*
