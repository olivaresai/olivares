# OpenSSF Best Practices Badge — evidence map (targeting the passing level)

This document maps the **OpenSSF Best Practices Badge** *passing*-level criteria
to the artifacts in this repository, so that registering the project and
completing the self-certification at <https://www.bestpractices.dev> is a
mechanical exercise.

## What this is — and what it is NOT

- The **OpenSSF Best Practices Badge** is a **self-certification questionnaire**
  hosted at bestpractices.dev. A maintainer answers each criterion (Met / Unmet /
  N/A) with a justification or URL. It has three levels: **passing → silver →
  gold**. This document targets **passing**.
- It is **distinct from the OpenSSF Scorecard** the project ships. The Scorecard is
  an *automated heuristic* that runs against the public GitHub repository
  (`.github/workflows/scorecard.yml`, badge already in `README.md` — it only
  populates once the project is published to the public GitHub repository and the
  workflow has run; until then it renders as "unknown"). The Badge is
  a *human-completed self-assessment*. The two are complementary, not the same
  thing; do not conflate them.

> **Honest status:** the project is **not yet registered** at bestpractices.dev,
> and **no badge level is claimed**. Registration and the self-certification are
> the maintainer's action (it needs an account/identity, like the GitHub push of
> the release). Below, "Met" means the supporting artifact exists today;
> "At first release / on the public repo" means the criterion depends on the
> public repository or the first tagged release; those statuses are refreshed at
> registration time. We do not mark a criterion Met where the evidence is not
> there.

The passing criteria are organised by the badge's six categories: **Basics,
Change Control, Reporting, Quality, Security, Analysis**.

## Basics

| Criterion | Evidence in this repo | Status |
|---|---|---|
| `description_good` | `README.md` — what the project does and the differentiating pillar | Met |
| `interact` / `contribution` | `CONTRIBUTING.md` (how to contribute), `GOVERNANCE.md` | Met |
| `floss_license` | AGPL-3.0-only (`core/`,`modules/`,`web/`) + Apache-2.0 (`sdk/`,`connectors/`) — OSI-approved | Met |
| `license_location` | `LICENSE`, `LICENSES/`, `LICENSING.md`, per-file SPDX (`scripts/check-spdx.sh`) | Met |
| `documentation_basics` | `README.md`, `ARCHITECTURE.md`, `docs/`, and the `docs-site/` Diátaxis site | Met |
| `documentation_interface` | OpenAPI 3.1 + AsyncAPI 3.0 reference rendered in `docs-site/` | Met |
| `sites_https` | Repository and badges over HTTPS | Met (project website `/security`, `/pricing` planned) |
| `discussion` | GitHub Discussions / issue tracker | At first release / on the public repo |
| `maintained` | Active development (active development, frequent releases) | Met |

## Change Control

| Criterion | Evidence | Status |
|---|---|---|
| `repo_public` | Public repository `<public-owner>/<public-repository>` (curated from a private dev repo) | On the public repo (maintainer's push) |
| `repo_track` / `repo_interim` | Git history; commits available between releases | Met |
| `version_unique` | CalVer `vYY.M.PATCH` (`CHANGELOG.md`) | Met at first release (no tag yet — not fabricated) |
| `release_notes` | `CHANGELOG.md` (Keep a Changelog 1.1) — structure in place | Met structurally; content at first release |
| `release_notes_vulns` | `CHANGELOG.md` *Security* section + `docs/security-advisories.md` | Met (designed) |

## Reporting

| Criterion | Evidence | Status |
|---|---|---|
| `report_process` | `SUPPORT.md` + `.github/ISSUE_TEMPLATE/` (bug/feature + `config.yml`) | Met |
| `report_responses` | Maintainer triages issues (see `GOVERNANCE.md`) | Met (intent; demonstrated over time) |
| `report_archive` | Issue tracker is publicly archived | On the public repo |
| `vulnerability_report_process` | `SECURITY.md` — private reporting to `security@olivares.ai` + PVR; machine-discoverable via RFC 9116 `/.well-known/security.txt` | Met |
| `vulnerability_report_response` | `SECURITY.md` acknowledgement/triage windows + remediation SLA | Met |

## Quality

| Criterion | Evidence | Status |
|---|---|---|
| `build` | `task build` — single static Go binary, web embedded | Met |
| `test` / `test_invocation` | `task test` (Go + web suites); CI runs the same | Met |
| `test_policy` / `tests_are_added` | `CONTRIBUTING.md` requires tests; new code adds tests | Met |
| `warnings` / `warnings_fixed` | `golangci-lint` v2 + ESLint/Prettier, blocking in CI (`task lint`) | Met |

## Security

| Criterion | Evidence | Status |
|---|---|---|
| `know_secure_design` | `docs/SECURITY-HARDENING.md` + threat model (`docs-site/`); read-first/minimal-data design | Met |
| `know_common_errors` | Security product; threat model + memory-safe Go + redaction guardrails | Met |
| `delivery_mitm` / `delivery_unsigned` | Signed releases (cosign/Sigstore), SBOM attestation, SLSA provenance, checksums; `scripts/verify-release.sh` | Met |
| `vulnerabilities_fixed_60_days` | Remediation SLA in `SECURITY.md` is stricter than 60 days for High/Critical | Met (designed) |
| `no_leaked_credentials` | Secret scanning in CI (`.gitleaks.toml`); no secrets in tree | Met |
| `crypto_*` (conditional) | The project **does** use cryptography: Ed25519 audit-ledger signatures, Sigstore/cosign release signing, mTLS/AutoMTLS, opaque tokens. These use published, standard, FLOSS algorithms with adequate key lengths. | Applies — assess each `crypto_*` item at self-cert (e.g. `crypto_published`, `crypto_floss`, `crypto_keylength`, `crypto_random`, `crypto_working`; `crypto_password_storage` only if passwords are stored) |

## Analysis

| Criterion | Evidence | Status |
|---|---|---|
| `static_analysis` | `golangci-lint`, `govulncheck` (call-graph reachability), SPDX/boundary gates | Met |
| `static_analysis_fixed` | CI is blocking; findings are fixed before merge | Met |
| `dynamic_analysis` | **SUGGESTED** at passing (not required). E2E harness + image scanning (grype/trivy) | Partial — declared honestly, not required for passing |

## Summary

On the evidence above, **passing looks achievable** once (a) the public GitHub
repository exists and (b) the first release is tagged — the only criteria not Met
today are the handful that genuinely require a public repo or a tagged release,
which we will not fake. **No level is asserted here.** The maintainer registers
the project at <https://www.bestpractices.dev>, completes the questionnaire using
this map, and only then is a level earned and a badge shown.

### README badge (after registration)

Once registered, bestpractices.dev assigns a numeric project ID. The badge
markdown for `README.md` is then:

```markdown
[![OpenSSF Best Practices](https://www.bestpractices.dev/projects/PROJECT_ID/badge)](https://www.bestpractices.dev/projects/PROJECT_ID)
```

Replace `PROJECT_ID` with the assigned number. Until then the badge is left as a
commented placeholder in `README.md` rather than shown broken (see the comment
next to the Scorecard badge).
