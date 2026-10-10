# Licensing

Olivares AI is **open core**. The complete product is available for free under
the **GNU Affero General Public License, version 3 (`AGPL-3.0-only`)** — the AGPL
build is the whole governance platform, never crippled from within to push you
toward a paid edition. On top of it sits a small, **additive** commercial line in
`enterprise/` (built only with `-tags enterprise`, never in the public binary). It
never caps your users: self-hosted user accounts are unlimited in every edition.

**Which capability is in which edition is written in one place:
[`docs/editions.md`](docs/editions.md).** This file does not repeat it, so the two
cannot disagree. That page also states what no edition does: move a Community
feature to a paid edition, count users, or make the Community binary read a
license to turn a capability on or off.

The self-hosted editions are **Community**, **Business** and **Enterprise**.
Community is free under AGPL-3.0-only, with unlimited users and one active identity
provider (IdP). Business costs **USD 129/month or USD 1,290/year**, with unlimited
users, one legal entity, and one active instance at a time: you release the license
from one instance and activate it on another as often as you need. Enterprise covers
terms agreed in a contract and the capabilities that depend on them: additional entities,
deployments or IdPs, air-gap mirrors, custom LTS, and scoped upstream credentials
minted by OAuth 2.0 token exchange (RFC 8693). Contact **enterprise@olivares.ai**
for Enterprise.

Business includes four capability families in one subscription: **Regulated
Operations**, **AI Runtime Security**, **Compliance Packs** and **Identity & Scale**.
You can enable or disable each family; none is sold separately. The families retain
separate code, repository and license-grant boundaries. What each family holds is
in [`docs/editions.md`](docs/editions.md).

The reasoning behind each cut is
[Open core & licensing](docs-site/src/content/docs/explanation/open-core-and-licensing.md)
(*Why this model*). In every case the open substrate stays open and the module is
new code layered on top — the open build answers honestly instead of degrading (an
absent subcommand, an unknown sink kind, or a `501` that names the module). The
Business threat-intel catalog is compiled into the binary; optional signed, versioned feed
artifacts are pinned and applied by the operator, and Olivares operates no curated
feed distribution and publishes no release cadence.

We offer a **commercial license** that provides a private *exception* to the
AGPL's obligations (for organizations that cannot comply with them). The
`enterprise/` capabilities are included in Business (the base line and the four
families above), under commercial terms, except the few that
[`docs/editions.md`](docs/editions.md) places in Enterprise. Enterprise scope is
negotiated. The open and
commercial editions are **not** identical — the modules are new code that was
never in the open build (the GitLab `ee/` model) — but nothing is taken away
from what ships open: no published feature is moved behind the wall. The
AGPL/Apache split itself is the classic dual-licensing frontier (MySQL, Qt,
MinIO, Grafana).

## What is open, what is commercial

Where each capability ships (the open AGPL build, a Business family or Enterprise) and why is the placement table in [`docs/editions.md`](docs/editions.md). That table is the only one. Maturity per capability is stated in [Honesty & limits](docs-site/src/content/docs/start/honesty-and-limits.md). The full list of reserved seams is declared in the public tree itself ([`cmd/olivares/wire_noenterprise.go`](cmd/olivares/wire_noenterprise.go)): a capability the open binary reserves answers `501` or no-ops, and its comment says so — nothing is hidden and nothing open is removed.

The AGPL build is the whole platform and is never feature-capped from within. The commercial modules are additive new code, never features removed from the open product. A subscription is the credential you download signed module packs with — a distribution-style model of signed module packs — not a key that unlocks code already sitting on your disk. User accounts are unlimited in the self-hosted engine: no edition of it enforces a seat cap, and the binary's seat seam is an unconditional no-op. The hosted Cloud tier is the one exception — its control plane admits seats per tenant, which is a property of that service and not of this binary.

## License by directory (the frontier)

| Path           | License                        | SPDX identifier                  |
|----------------|--------------------------------|----------------------------------|
| `/core`        | GNU AGPL v3.0                  | `AGPL-3.0-only`                  |
| `/modules`     | GNU AGPL v3.0                  | `AGPL-3.0-only`                  |
| `/web`         | GNU AGPL v3.0                  | `AGPL-3.0-only`                  |
| `/sdk`         | Apache License 2.0             | `Apache-2.0`                    |
| `/connectors`  | Apache License 2.0             | `Apache-2.0`                    |
| `/clients`     | Apache License 2.0             | `Apache-2.0`                    |
| `/enterprise` (separate private repository — not in this repo) | Olivares.AI Commercial License | `LicenseRef-Olivares-Commercial`|

Why two open-source licenses:

- **Core, modules and web are AGPL** so the network-use copyleft (AGPL §13)
  closes the SaaS free-rider gap: anyone who modifies Olivares AI and offers it
  as a network service must publish their modified source.
- **The SDK and connector interfaces are Apache-2.0** so the community can build
  and ship connectors and integrations with zero copyleft friction. The moat is
  the breadth of the connector ecosystem; a copyleft SDK would suppress it.

The full license texts live in the repository:

- `LICENSE` (repo root), `core/LICENSE`, `modules/LICENSE`, `web/LICENSE`
  — GNU AGPL v3.0 text.
- `sdk/LICENSE`, `connectors/LICENSE` — Apache License 2.0 text, plus the repository-root `NOTICE`.
- `LICENSES/LicenseRef-Olivares-Commercial.txt` — the commercial terms notice/stub
  (the operative contract is the agreement executed at purchase; the `enterprise/`
  tree itself is maintained in a separate private repository).
- `LICENSES/` — canonical copies of every license text used in the repo, as
  required by the [REUSE specification](https://reuse.software/).

Every source file carries an SPDX header from the first commit:

```go
// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
```

CI verifies the header on every source file (`scripts/check-spdx.sh`); a missing
or wrong header fails the build.

### Why `AGPL-3.0-only` (not `-or-later`)

All contributions are licensed to Olivares.AI under the CLA (see *Contributing*),
whose outbound-license clause — Harmony outbound option five, “Any License”, the
selection this project requires on every signed form — lets Olivares.AI relicense
them, including adopting a future AGPL version if one is published. That
relicensing authority comes from the CLA's license grant (contributors retain
copyright in their work — see [`CLA.md`](CLA.md)), **not** from the public grant's
version clause. `-only`
therefore costs us nothing in flexibility while giving the dual-licensor more
control: downstream users are bound to AGPLv3 exactly and cannot unilaterally
move the public grant to an unvetted future AGPL version. It is also the literal
reading of the project's stated choice ("AGPLv3") and matches the dual-licensed
projects we model on (e.g. Grafana ships `AGPL-3.0-only`). The commercial
exception is a separate private contract and is unaffected by this choice.

## Warranty and liability

**The free software comes with no warranty of any kind, and nobody accepts liability for what
happens when you run it — including loss of your data.** That is not a formality on a control
plane: a misconfiguration can block legitimate work and interrupt production, or let through
exactly what you meant to stop.

- **AGPL-3.0-only material** is disclaimed by sections 15 and 16 of the licence
  (`LICENSES/AGPL-3.0-only.txt`, lines 587–596 and 598–608 — section 16 names *"LOSS OF DATA OR
  DATA BEING RENDERED INACCURATE"* at lines 603–605).
- **Apache-2.0 material** is disclaimed by sections 7 and 8
  (`LICENSES/Apache-2.0.txt`, lines 144–152 and 154–164).
- On top of both, the project states its own supplemental disclaimer — the term AGPL-3.0-only
  section 7(a) expressly permits (`LICENSES/AGPL-3.0-only.txt`, lines 349–354) — covering data
  loss and corruption, business interruption, lost profits and revenue, substitute-procurement
  costs, high-risk uses, compliance outcomes and third-party components.

**Read [`DISCLAIMER.md`](DISCLAIMER.md) before you deploy.** It is the full text, it protects
every contributor as well as Olivares AI, and it expressly does **not** restrict any right the
open licenses grant you.

**Commercial subscriptions are a separate contract.** Whatever warranty, support and liability
terms a paid offering carries are those of the agreement executed at purchase. They never attach
to code you obtained under the open licenses, and no commercial agreement modifies the open
licenses' own disclaimers or any right you hold under them.

## Buying the commercial exception

You need the commercial license if you cannot or do not want to meet
the AGPL obligations — e.g. an internal policy that forbids AGPL, embedding in a
closed-source product, or running a modified network service without publishing
your changes. Using any of the `enterprise/` modules also requires the
Business entitlement that includes it (the base line or the family), or an Enterprise
agreement for the capabilities placed there, independently of AGPL compliance. Otherwise,
use the free AGPL build.

- **Commercial license, modules, custom terms, support — dedicated contact:**
  **enterprise@olivares.ai**

Support per edition and the Enterprise first-response targets — non-binding, not
penalty-backed SLAs — are published in [`SUPPORT.md`](SUPPORT.md).
The **Enterprise** relationship also covers commercial/legal terms — data-residency
*architecture support*, dedicated deployment, and **indemnification** — which are
contractual, never features of the binary (residency itself ships in the open product;
indemnification terms are pending a legal decision). None of these contractual terms
is a feature of the binary or gated by the license key. The **AGPL core** is never
gated by any license key; the terms that govern commercial modules are those of the
commercial agreement.

See `LICENSES/LicenseRef-Olivares-Commercial.txt` for the commercial terms summary.

### What the subscription does and does not call home for

Two different things get confused under "phone home", so this file states both. **Verifying a
licence never calls anyone. Downloading what you paid for does.**

- **Community (AGPL).** Licence validation is offline Ed25519. There is no remote kill switch.
  The AGPL kernel does not make a licence call — and no licence key gates it, ever.
- **Commercial.** The subscription is the credential with which modules, updates and patches are
  downloaded. Phone-home is approved for licence issuance and updates. There is no mandatory
  telemetry and no control-plane egress by default.

That is the shape of a subscription that grants access to the enterprise repositories — the model
this line was designed against — and not a licence that checks in on you while you work. What you
buy is the right to fetch and keep receiving the modules included in your subscription; what you run answers to
nobody at runtime.

## Trademarks

"Olivares AI"™, "Olivares.AI" and the project logos are trade marks of **Francisco
Olivares**. A registration has been applied for and is pending grant; until it is
granted the marks are used with **™** and never with **®**.

The software licenses govern the code, not the marks — with one precision stated
rather than hidden: **Apache-2.0 section 6** (`LICENSES/Apache-2.0.txt`, lines
139–142) grants no trade mark permission *"except as required for reasonable and
customary use in describing the origin of the Work and reproducing the content of
the NOTICE file"*, and that excepted use is granted by the licence itself — no
Olivares AI policy restricts it. AGPL-3.0-only grants no trade mark rights.
Beyond that, you may use, modify and redistribute the code under its license, but
you may not use the Olivares AI name or logos to brand a fork or a competing
service in a way that implies endorsement. (Contact `enterprise@olivares.ai` for
permitted uses.)

## Contributing

Contributions are accepted under the **Developer Certificate of Origin**
(`DCO`): sign off every commit with `git commit -s`. The DCO check on pull
requests rejects commits without a `Signed-off-by` trailer (a required status
check, provisioned with the public repository).

Because Olivares AI is dual-licensed, external contributors are additionally
asked to sign the **Contributor License Agreement** (`CLA.md`, based on the
Harmony Agreements) before their first contribution is merged. This keeps the
chain of licensing authority clean so the commercial exception can continue to be
offered. See `CLA.md` for the individual and entity terms and how to sign.
