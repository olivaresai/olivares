---
title: Open core & licensing
description: >-
  Open core: the complete product is AGPL-3.0-only, the SDK and connectors are
  Apache-2.0, and a small additive line, the Business and Enterprise editions, is commercial. The AGPL build
  is never crippled to upsell, but it is not identical to the commercial edition.
  What that means for self-hosters and connector authors.
---

Olivares AI is **open core**. The complete Community product is released under the GNU
Affero General Public License. Its AGPL build is the whole governance platform,
with unlimited users and one active identity provider (IdP). The commercial
`enterprise/` line adds new code, built only with `-tags enterprise` and absent
from the public binary. Business includes four capability families in one
subscription. Enterprise covers negotiated scope. A commercial license provides
the legal exception to copyleft; nothing published open moves behind a paywall.

## The license boundary

Licensing follows the source tree. Every file carries an SPDX header, and the
boundary is enforced in CI (a connector may never import the engine):

| Path | License | What it is |
|---|---|---|
| `core/` | **AGPL-3.0-only** | the engine: ingest, event bus, data model, module runtime, API, authz, audit |
| `modules/` | **AGPL-3.0-only** | the 32 modules (inventory, the R/RW map, FinOps, evals, guardrails, …) |
| `web/` | **AGPL-3.0-only** | the React UI |
| `sdk/` | **Apache-2.0** | the connector/module interfaces, the gRPC contract and the shared types |
| `connectors/` | **Apache-2.0** | the connectors (Claude, OpenAI, pgAudit, eBPF, cloud, Slack, SIEM, …) |
| `enterprise/` | **commercial** | additive modules, build-tag gated, never in the public binary (`LicenseRef-Olivares-Commercial`) |

The documentation site you are reading is part of the AGPL product. Which capability is
in which edition is written in one place, `docs/editions.md` in the repository; this page does
not repeat it.

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
- **A commercial license.** Organizations that need an exception to the AGPL
  obligations can contact **enterprise@olivares.ai** for a commercial agreement.
  Business includes the four capability families below in one subscription.
  Enterprise terms are negotiated; families are not sold separately.

## Editions and pricing

| Edition | Price | Scope |
| --- | --- | --- |
| Community | Free, AGPL-3.0-only | Unlimited users; one active identity provider (IdP). |
| Business | USD 129/month or USD 1,290/year | Unlimited users; one legal entity; one active instance at a time. |
| Enterprise | Contact us | Terms agreed in a contract and the capabilities that depend on them: multi-entity scope, additional deployments or IdPs, air-gap mirrors, custom LTS, and scoped upstream credentials minted by OAuth 2.0 token exchange (RFC 8693). |

Business includes **Regulated Operations**, **AI Runtime Security**,
**Compliance Packs**, and **Identity & Scale**. Each family keeps its own code and
license-grant boundary. You can enable or disable each family; none is sold separately.
Private implementation is distributed as commercial binaries, outside the public repository.

### Can I buy a capability family separately?

No. The four named families are included in the Business subscription. Choose
monthly or annual billing at [Pricing](https://olivares.ai/pricing).

### What if I need more than one active instance?

Contact **enterprise@olivares.ai** for Enterprise scope. A Business license is
active on one instance at a time; you release it and activate it on another
instance as often as you need. Enterprise covers several active instances.


## What is open vs commercial

The open binary is the whole governance platform; the commercial (`enterprise/`) line is
**additive**. Two boundaries are worth calling out because the open build answers
for them honestly rather than faking them:

- **SSO** — single-IdP login (OIDC + SAML 2.0) and inbound SCIM are **open** in the
  default binary: real login, no `-tags enterprise`. Several active IdPs (per-tenant /
  by-domain), login-time group mapping and require-SSO are Business (Identity & Scale);
  activating a second active IdP returns `multi_idp_requires_enterprise`.
- **User accounts** — **unlimited in every edition**. The community build has no user
  cap, and neither has the commercial one: no license state (valid, expired, absent)
  can limit how many accounts a deployment runs. The cap of three active accounts that
  shipped before 2026-07-27 was removed outright; the seat seam remains in the code as
  a compatibility no-op that refuses nothing, and a license lapse never caps, disables
  or deletes an account.

See [Honesty & limits](/start/honesty-and-limits/) for the full open-vs-commercial
picture.

## The license key never gates the open product

This is important and deliberate: in the open (AGPL) binary, license validation is
**attestation only**. The engine records who holds a license and its status; it
**never disables, degrades or blocks** any request, any module, or boot on a
license check, and it runs **offline** (an Ed25519 signature, no license server),
which is why the open product works air-gapped. The one place the license is
*consumed* rather than displayed is the closed commercial build, and only to entitle
the modules the commercial agreement covers, evaluated per module — a local decision
in the commercial edition, never a check in the open binary. It never caps users: accounts are unlimited in every edition. So the open
build is genuinely whole and uncapped-by-license; what differs in the commercial
edition is the additive commercial (`enterprise/`) modules, not a license key flipping features
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
  the in-place Community → Business swap. This page explains the model; that one is the
  steps.
- [Security model](/explanation/security/security-model/) — why attestation-only
  licensing matters for an air-gapped security product.
