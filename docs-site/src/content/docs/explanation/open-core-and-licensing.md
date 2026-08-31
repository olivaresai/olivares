---
title: Open core & licensing
description: >-
  Open core: the complete product is AGPL-3.0-only, the SDK and connectors are
  Apache-2.0, and a small additive enterprise line is commercial. The AGPL build
  is never crippled to upsell, but it is not identical to the commercial edition.
  What that means for self-hosters and connector authors.
---

Olivares AI is **open core**. The **complete product** is released under the GNU
Affero General Public License, and the AGPL build is the whole governance
platform — never crippled from within to push you toward a paid edition. On top of
it sits a small set of **additive** commercial add-ons in `enterprise/`, built only
with `-tags enterprise` and absent from the public binary. A commercial license
provides the legal exception to copyleft; the `enterprise/` capabilities are
licensed as **separate, optional add-ons** — so the open and commercial editions
are **not** identical, while nothing published open is ever moved behind the wall
(the GitLab `ee/` model, not a feature paywall on the core).

## The license boundary

Licensing follows the source tree. Every file carries an SPDX header, and the
boundary is enforced in CI (a connector may never import the engine):

| Path | License | What it is |
|---|---|---|
| `core/` | **AGPL-3.0-only** | the engine: ingest, event bus, data model, module runtime, API, authz, audit |
| `modules/` | **AGPL-3.0-only** | the 30 modules (inventory, the R/RW map, FinOps, evals, guardrails, …) |
| `web/` | **AGPL-3.0-only** | the React UI |
| `sdk/` | **Apache-2.0** | the connector/module interfaces, the gRPC contract and the shared types |
| `connectors/` | **Apache-2.0** | the connectors (Claude, OpenAI, pgAudit, eBPF, cloud, Slack, SIEM, …) |
| `enterprise/` | **commercial** | additive add-ons, build-tag gated, never in the public binary: multi-IdP federation, content firewall/DLP, hook hardening, compiled threat-intel catalog, server-tool egress, CyberArk Conjur, incident close-loop (`LicenseRef-Olivares-Commercial`) |

The documentation site you are reading is part of the AGPL product.

## What this means for you

- **Self-hosting the product (AGPL).** You can run, study, modify and redistribute
  the complete product under the AGPL. The AGPL's network-use clause applies: if you
  offer a modified version to others over a network, you must offer them your
  modified source. For internal self-hosting this is rarely an issue; if you want to
  build a product *on top of* Olivares AI without that obligation, the commercial
  license exists for exactly that.
- **Building connectors (Apache-2.0).** The SDK and connectors are **Apache-2.0** —
  permissive, no copyleft. You can write a connector, keep it proprietary, and ship
  it however you like. The architectural boundary that makes this safe is enforced:
  an Apache-2.0 connector **never imports the AGPL engine**; it depends only on the
  SDK. That keeps the connector ecosystem free of copyleft friction.
- **A commercial license.** Organizations that need to avoid the AGPL's obligations
  (for example, embedding the product in a proprietary offering) can obtain a
  commercial license — contact **enterprise@olivares.ai** (pricing on request).
  The additive `enterprise/` add-ons above are licensed separately, each as an
  optional entitlement.

## What is open vs enterprise

The open binary is the whole governance platform; the `enterprise/` line is
**additive**. Two boundaries are worth calling out because the open build answers
for them honestly rather than faking them:

- **SSO** — single-IdP login (OIDC + SAML 2.0) is **open** in the default binary:
  real login, no `-tags enterprise`. Multiple active IdPs (per-tenant / by-domain),
  SSO-enforcement and managed SCIM are the reserved enterprise line; activating a
  second active IdP returns `multi_idp_requires_enterprise`.
- **User accounts** — **unlimited in every edition**. The community build has no user
  cap, and neither has the enterprise one: no license state (valid, expired, absent)
  can limit how many accounts a deployment runs. The cap of three active accounts that
  shipped before 2026-07-27 was removed outright; the seat seam remains in the code as
  a compatibility no-op that refuses nothing, and a license lapse never caps, disables
  or deletes an account.

See [Honesty & limits](/start/honesty-and-limits/) for the full open-vs-enterprise
picture.

## The license key never gates the open product

This is important and deliberate: in the open (AGPL) binary, license validation is
**attestation only**. The engine records who holds a license and its status; it
**never disables, degrades or blocks** any request, any module, or boot on a
license check, and it runs **offline** (an Ed25519 signature, no license server),
which is why the open product works air-gapped. The one place the license is
*consumed* rather than displayed is the closed enterprise build, and only to entitle
the add-ons the commercial agreement covers, evaluated per add-on — a local decision
in the commercial edition, never a check in the open binary. It never caps users: accounts are unlimited in every edition. So the open
build is genuinely whole and uncapped-by-license; what differs in the commercial
edition is the additive `enterprise/` add-ons, not a license key flipping features
on inside the same binary.

## Why this model

Crippling the core was rejected: the open edition does the whole job — the full
governance loop on one node — so capping it would make it a worse product and erode
trust. Permissive-everything (MIT/Apache on the core) would give the core away with
no commercial footing. Source-available, non-OSS licenses (BSL, SSPL and similar)
would kill the open-source adoption that is the whole point of an extensible
connector ecosystem. So the model is **open core**: a copyleft product that is
complete and credible on its own, a permissive SDK that keeps the connector
ecosystem frictionless, and a small **additive** commercial line of new code that
was never in the open build — plus a clean commercial exception — *without ever
degrading what you can self-host*.

## Contributing

Contributions are accepted under the project's contribution terms (the repository
ships both a DCO and a CLA, plus a trademark policy). See the repository's
`CONTRIBUTING` guide for the current process.

## Related

- [Install a license](/how-to/install-a-license/) — where a purchased license goes, and
  the in-place community → enterprise swap. This page explains the model; that one is the
  steps.
- [Security model](/explanation/security/security-model/) — why attestation-only
  licensing matters for an air-gapped security product.
