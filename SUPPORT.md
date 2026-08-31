# Support

Thanks for using Olivares AI. This page explains **where to get help** — and,
just as importantly, **where not to report security problems**.

> Olivares AI is **beta** and in active development. There is no released,
> supported version yet, and no commercial support offering exists yet; when paid
> support activates, it carries best-effort first-response targets, not an SLA
> (see [`SECURITY.md`](SECURITY.md) for the supported-versions statement).
> Set your expectations accordingly: this is a developer build.

## Do NOT use these channels for security issues

**Never report a suspected vulnerability in a public issue, pull request, or
discussion.** Olivares AI is a security product, so a flaw in it can become a
flaw in the systems it observes. Report vulnerabilities **privately** through the
process in [`SECURITY.md`](SECURITY.md) (private email to `security@olivares.ai`,
and GitHub Private Vulnerability Reporting, enabled with the public repository (Security → Report
a vulnerability)). Public disclosure before a fix puts users at risk.

## Where to get help

| You want to… | Use |
|---|---|
| **Report a bug** | Open a **Bug report** issue (use the template). Include the `olivares version`, your platform, and steps to reproduce. |
| **Request a feature or change** | Open a **Feature request** issue (use the template). For anything non-trivial, please open it as a discussion first so the approach can be agreed before code is written — see [`CONTRIBUTING.md`](CONTRIBUTING.md). |
| **Ask a question / discuss usage** | Use GitHub Discussions on this repository (when enabled). Questions are not bugs — please don't open issues for them. |
| **Read the documentation** | Start with [`README.md`](README.md) and [`ARCHITECTURE.md`](ARCHITECTURE.md). The full product documentation site lives in [`docs-site/`](docs-site/); it is not yet published to a public URL (that is a later step), so build it locally or browse the source for now. |
| **Contribute** | See [`CONTRIBUTING.md`](CONTRIBUTING.md) (setup, DCO/CLA, SPDX, the connector boundary) and [`GOVERNANCE.md`](GOVERNANCE.md) (how decisions are made). |
| **Report conduct concerns** | Email the dedicated conduct alias in [`CODE_OF_CONDUCT.md`](CODE_OF_CONDUCT.md) (`conduct@olivares.ai`). |

## Commercial support and licensing

The open (AGPL) build is the complete **core governance platform** — nothing is
capped or held back from within it to upsell you. This is open core: on top of
that core, the commercial offering adds a small, separate line of additive
`enterprise/` add-ons that were never part of the open build (multi-IdP
federation, content firewall/DLP, the compiled threat-intel catalog, and
server-tool egress
governance, among others). It never caps your users — self-hosted user accounts
are unlimited in every edition. If you need a **commercial license** (a private
exception to the AGPL), any of the separate `enterprise/` add-ons (packaging and
pricing on request), or you want **larger-deal /
custom terms**, see [`LICENSING.md`](LICENSING.md) or contact
`enterprise@olivares.ai`. Commercial and licensing enquiries go to
`enterprise@`, **not** to `security@` or the public issue tracker.

## Support tiers and response targets (commercial — defined, not yet purchasable)

> **Honesty first.** No commercial support contract can be purchased today
> (beta; commercial activation and pricing are a pending business decision —
> the self-serve commerce flow ships dark until then). This section **describes**
> the support model intended to take effect with the first commercial contract,
> published now so procurement can evaluate it. It is sized for what
> the vendor actually is — see the single-maintainer disclosure in
> [`docs/trust/vendor-viability.md`](docs/trust/vendor-viability.md) — not for a
> support floor we don't operate.
>
> **This page is not a contract document and is not an offer.** Every target
> below is a non-binding aim point, not an SLA and not a warranty: no credit,
> penalty or other remedy attaches to missing one. What binds, once a commercial
> agreement exists, are that agreement's support terms — and nothing on this page
> creates, extends or reduces an obligation under it.

### Tiers

| | **Community** (AGPL, free) | **Standard** (commercial) | **Enterprise** |
|---|---|---|---|
| Channels | Public issues / discussions | Email (dedicated support address) | Email + scheduled video calls; named technical contact (the maintainer) |
| Hours | Best effort | Business hours, Mon–Fri 09:00–18:00 **Europe/Madrid** | Business hours + reasonable-efforts out-of-hours response for SEV1 |
| Scope | The software as released | Product defects, upgrade/config guidance for supported versions | Standard + deployment review, upgrade planning, priority feature triage |
| Response targets | None (best effort) | Targets below (non-binding) | Targets below (non-binding) |

Support covers the **product**; it does not operate the customer's deployment
(self-hosted). Security vulnerabilities always follow [`SECURITY.md`](SECURITY.md)
regardless of tier (private channel, published remediation targets).

**How the tiers map to the commercial offering.** *Standard* support accompanies
commercial purchases; *Enterprise* support is the top commercial tier — the
named-contact relationship plus the commercial/legal terms below. The first-response
targets are **best-efforts aim points sized for a single-maintainer vendor, not
penalty-backed contractual SLAs** (see [`docs/trust/vendor-viability.md`](docs/trust/vendor-viability.md)
§6); the precise binding of price tier → support tier is a pending business decision.

**The tier is attested in the license, for display only.** A commercial license carries
an attested `support_tier` label (e.g. `standard` / `enterprise`) that the deployment's
own console (**Edition & license**) and `GET /v1/server-info` surface, so an operator can
see which tier their own license records. Like every license claim in the **open (AGPL)
binary** it is **display/record only — it gates nothing there**
([`LICENSING.md`](LICENSING.md)); commercial
add-ons are a paid-term entitlement under the commercial agreement. It is **not** how a support entitlement is decided: per the key-custody rule, no
Olivares-side support or billing decision trusts the engine's self-report — the actual
entitlement is the commercial agreement / billing record, never the self-attested label.

### Severity and first-response targets

Severity is the **single, shared** scale from the published incident model — the
authoritative definitions and examples live in
[`docs/STATUS-AND-INCIDENT-COMMS.md`](docs/STATUS-AND-INCIDENT-COMMS.md) §2, the same
scale the error-budget policy in
[`docs/17-PRODUCTION-READINESS-SLO.md`](docs/17-PRODUCTION-READINESS-SLO.md) §3 keys its
postmortems and `P0`/`P1` incident triggers off. The rows below add a **commercial
first-response** layer over that one scale; they do not redefine it (the glosses are a
quick reference only):

| Severity | Definition | Standard — first response | Enterprise — first response |
|---|---|---|---|
| **SEV1** | Control plane down or governance unsafe (enforcement/audit integrity broken) in a production deployment | 8 business hours | **4 business hours** (reasonable efforts out-of-hours) |
| **SEV2** | Degraded: SLO breached or at risk; plane serving | 2 business days | 1 business day |
| **SEV3** | Minor: no SLO impact, questions, cosmetic | 5 business days | 3 business days |

These are **response** targets, not resolution promises, and they are
**non-binding**: no result, resolution, availability or timeframe is guaranteed,
and missing a target carries no credit, penalty or other remedy. Resolution
effort is prioritized by severity, with workarounds first for SEV1/SEV2; fixes
ship as signed releases (out-of-band for SEV1 when warranted, mirroring the
KEV/Critical practice in `SECURITY.md`).

### Escalation path

1. Support channel (per tier) — include `olivares version`, platform, and
   redacted config/logs.
2. Escalation: the founder/maintainer directly (the escalation chain is honestly
   one level deep; Enterprise contracts get the direct contact from day one).
3. SEV1/SEV2 incidents are communicated through the channel of your tier. The
   update cadence in [`docs/STATUS-AND-INCIDENT-COMMS.md`](docs/STATUS-AND-INCIDENT-COMMS.md)
   is an operating target for surfaces Olivares AI itself operates: the status
   page shipped with the product is **self-hostable** (`deploy/monitoring/status-page.gatus.yaml`)
   and a self-hosted deployment runs its own — Olivares AI neither operates nor
   updates it, and gives no timing commitment for it. Postmortems follow the
   error-budget policy in `docs/17-PRODUCTION-READINESS-SLO.md`, which states
   engineering objectives, not contractual commitments.

### Enterprise commercial terms: data residency, dedicated deployment, indemnification

These are commercial/legal facets of the **Enterprise** relationship — not features of
the binary and not gated by the license key. They monetize the *operated and contractual*
layer (which a public clone cannot replicate), never anything carved out of the open
product.

- **Data residency.** Olivares AI is self-hosted: there is no vendor-side service in the
  request path, **no mandatory telemetry and no control-plane egress by default**, and no
  remote kill ([`docs/trust/vendor-viability.md`](docs/trust/vendor-viability.md) §4).
  What crosses your perimeter is what **you** configure to cross it — calls to your model
  APIs, the SIEM/webhook outputs you wire, an external embedding provider if you provision
  one ([`README.md`](README.md)). That is a property of the architecture and of your
  configuration; it is a description, **not a guarantee** — Olivares AI does not operate
  your deployment and warrants no outcome for it. For a multinational
  splitting EU/US data, the product also pins residency **per tenant**: a first-class
  `orgs.data_region` region pin with a deny-closed cross-region guard — the control plane
  physically refuses to serve a tenant outside its home region (`core/residency`;
  [`docs/MULTI-REGION-RESIDENCY.md`](docs/MULTI-REGION-RESIDENCY.md)). That mechanism is
  part of the **open** product, not a paid lock; what the Enterprise relationship adds is
  the **architecture and deployment support** to design the topology, not the capability.
  (Honesty: control-plane residency is enforced deny-closed; inference-region coherence is
  *observe-and-flag*, not routing enforcement — it makes a crossing visible, it does not
  block inference. We do not offer active/active multi-region DR.)
- **Single-tenant / dedicated deployment.** A self-hosted control plane is **single-tenant
  by construction**: you run your own instance in your own estate, and row-level security
  isolates tenants within it. There is no shared multi-tenant SaaS to carve a private
  tenant out of. The Enterprise "dedicated deployment" lever is therefore an **operational
  engagement** — dedicated-deployment review, a pinned region, upgrade planning — over that
  reality, not new code and not a license gate.
- **Indemnification** *(pending — not offered today)*. An IP / AGPL-compliance indemnity is
  a purely **legal/contractual** term attached to the Enterprise agreement; it touches no
  code and does not depend on the license key. Its terms — and the related verified
  source/SaaS escrow and the ISO 27001 (EU) → SOC 2 path — are a pending business/legal
  decision (see below); **none is in place yet.** Until they land, the honest continuity
  answer procurement gets is the one already in
  [`docs/trust/vendor-viability.md`](docs/trust/vendor-viability.md): **AGPL source on day
  one**, **verified escrow of the `enterprise/` source offered on request**, and the planned
  written discontinuation / relicense pledge — **not** an indemnity.

### What requires a business decision before activation

Pricing per tier, the purchase flow, and any out-of-hours commitment beyond
"reasonable efforts" (a hard 24×7 SLA will **not** be offered while the team is
one person — see `docs/trust/vendor-viability.md` §6; saying otherwise would be
fiction).

Also pending, and **owned by the founder** (legal/business, not buildable in software):
the **indemnification terms**, the **verified escrow** agent and contract (e.g. NCC Group
/ Escode), and the **ISO 27001 (EU) → SOC 2** certification path. These back the Enterprise
commercial terms above; none is in place yet, so that copy is a description of the intended
relationship, **not an offer**, until they are signed.

## Before you open an issue

- Search existing issues first — yours may already be reported.
- Give enough to reproduce: version/commit, platform, configuration (with
  **secrets and personal data redacted** — see [`SECURITY.md`](SECURITY.md)),
  and what you expected versus what happened.
- One issue per problem. Keep it focused; it gets resolved faster.
