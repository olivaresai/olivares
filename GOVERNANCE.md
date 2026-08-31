# Governance

This document describes how the Olivares AI project is run: who decides what,
how contributions are reviewed and accepted, and how that maps onto the
project's licensing. It is deliberately **honest about the project's current
size** rather than describing a structure that does not yet exist.

> **Status (2026):** Olivares AI is **beta** and **maintainer-led** by a
> single maintainer. There is no steering committee, no formal RFC process, and
> no elected technical board — pretending otherwise would be dishonest. This
> document sets out the model we operate today and the path by which it grows as
> the contributor base does. It follows the spirit of the
> [CHAOSS](https://chaoss.community/) (Community Health Analytics for Open Source
> Software) project's guidance on documenting open-source governance and
> community health.

## Model: maintainer-led (BDFL)

Decisions are made by the project maintainer(s). In practice today that is a
single maintainer who acts as a [BDFL](https://en.wikipedia.org/wiki/Benevolent_dictator_for_life)
("benevolent dictator for life"): the maintainer sets technical direction,
arbitrates disagreements, and has the final say on what is merged.

This is consistent with how the repository operates: maintainers commit
directly to `main`, while external contributors open pull requests that go
through review (see [`CONTRIBUTING.md`](CONTRIBUTING.md)).

## Roles

| Role | Who | What they can do |
|---|---|---|
| **Maintainer** | The project lead (today: a single person). | Set direction, review and merge contributions, cut releases, administer the repository, and decide on the items below. Has commit access to `main`. |
| **Contributor** | Anyone who opens an issue or pull request. | Propose changes, report bugs, write connectors, improve docs. Contributions are merged after maintainer review and once the CLA/DCO requirements are met. |

There is currently **no intermediate "committer" tier**. If and when the
contributor base grows enough to need one, this document will be updated to
describe it — not before.

### Becoming a maintainer

Maintainership is earned, not requested. The path is sustained, high-quality
contribution over time — code, review, and good judgement — after which an
existing maintainer may invite a contributor to join. There is no fixed quota,
election, or schedule; this reflects the project's stage, and the rule will be
formalised when there is a real need for it.

## How decisions are made

- **Routine changes** (bug fixes, connectors, docs, dependency bumps) are
  decided in the pull request by maintainer review.
- **Non-trivial or directional changes** should start as an issue for
  discussion *before* a pull request, so the approach can be agreed early (see
  [`CONTRIBUTING.md`](CONTRIBUTING.md)).
- **Architectural decisions** are recorded as Architecture Decision Records
  (MADR) under [`docs/adr/`](docs/adr/). The ADR log is the durable record of
  *why* a decision was made; new directional decisions should land as an ADR.
- **Disagreements** are resolved by discussion first; where consensus is not
  reached, the maintainer decides and records the rationale (in the PR or an
  ADR).

## Contribution requirements

Every contribution is subject to the requirements already documented elsewhere —
this section points to them, it does not restate them:

- **DCO sign-off** on every commit (`git commit -s`) — see [`DCO`](DCO) and
  [`CONTRIBUTING.md`](CONTRIBUTING.md).
- **Contributor License Agreement (CLA)** for external contributors before a
  first contribution is merged — see [`CLA.md`](CLA.md).
- **Conventional Commits**, passing CI (`task lint:spdx lint:boundary && task
  build:go && task test` — see [`CONTRIBUTING.md`](CONTRIBUTING.md), *The
  gate*), and a correct
  per-directory SPDX header — see [`CONTRIBUTING.md`](CONTRIBUTING.md).
- **Code of Conduct** — participation is governed by
  [`CODE_OF_CONDUCT.md`](CODE_OF_CONDUCT.md); enforcement reports go to the
  dedicated conduct alias listed there.

## Licensing, the CLA, and the commercial tier

Governance and licensing are linked, so the model must be explicit about both.
Olivares AI is **dual-licensed**: the complete product is offered publicly under
copyleft, and a separate commercial exception is offered to organisations that
cannot operate under it (see [`LICENSING.md`](LICENSING.md)).

The **CLA** is what makes that dual-licensing possible: it secures an unbroken
chain of copyright so the maintainer can keep offering the commercial exception.
The CLA does **not** transfer ownership — contributors retain copyright in their
work (see [`CLA.md`](CLA.md) for the actual terms).

The license frontier is enforced in CI and is reflected in
[`.github/CODEOWNERS`](.github/CODEOWNERS):

| Subtree | License | Notes |
|---|---|---|
| `core/`, `modules/`, `web/`, `cmd/`, `operator/`, `terraform-provider-olivares/`, `docs-site/` | `AGPL-3.0-only` | The product, the first-party binaries/operator/Terraform provider, and the docs site. Community contributions welcome under the CLA/DCO. |
| `sdk/`, `connectors/` | `Apache-2.0` | The permissive ecosystem boundary — the breadth moat. A connector must never import from `core/`. |
| `enterprise/` (separate private repository — not in this repo) | `LicenseRef-Olivares-Commercial` | Additive commercial features, maintained by Olivares.AI. Not a destination for community contributions. |

This is the same frontier enforced by `scripts/check-spdx.sh` and listed in
[`.github/CODEOWNERS`](.github/CODEOWNERS); see [`LICENSING.md`](LICENSING.md) for
the authoritative definition.

The commercial tier is governed by Olivares.AI, not by community process: it is
the project's commercial sustainability model, and it follows the open-core
(GitLab `ee/`) shape — the AGPL build is the **complete governance platform**,
never crippled-from-within to upsell, and the commercial tier additionally
provides the **additive `enterprise/` add-ons** (new code that was never in the
open build, so no rug-pull) together with the legal AGPL exception.

## Where work happens

The public repository on GitHub (`<public-owner>/<public-repository>`) is canonical. Maintainers
commit to `main`; external contributors open pull requests that go through review
and the CI gate. Signed releases, SLSA provenance, the SBOM/VEX attestations and
the OpenSSF Scorecard all run there, and external pull requests and security
advisories are handled there (see [`SECURITY.md`](SECURITY.md)).

## Security and disclosure

Security is governed separately from feature work: vulnerabilities are reported
privately and handled under a coordinated-disclosure process — never in a public
issue or pull request. See [`SECURITY.md`](SECURITY.md) for the contact, the
disclosure timeline, the remediation SLA, and the advisory (GHSA → OSV) flow.

## Changing this document

This governance model will evolve with the project. Changes to it are themselves
a directional decision: propose them as an issue (and, where they change how
decisions are made, an ADR), and the maintainer decides. The guiding principle
is that this file should always describe **what the project actually does**, not
an aspirational structure.
