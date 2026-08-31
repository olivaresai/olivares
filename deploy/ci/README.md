<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# Olivares policy gate — reusable CI artifacts

Two ready-to-consume CI artifacts let a **customer** run the Olivares
[policy pack](../policy/) (OPA/Conftest + Kyverno) against their own manifests
in their own pipeline, **before** they merge or apply. The GitHub action also
adds a best-effort Terraform `fmt`/`validate` leg for IaC:

| Platform | Artifact | Consumed as |
| --- | --- | --- |
| GitHub Actions | [`.github/actions/olivares-policy-gate`](../../.github/actions/olivares-policy-gate/action.yml) | a composite `uses:` step |
| GitLab CI/CD | [`deploy/ci/gitlab/templates/olivares-policy-gate`](gitlab/templates/olivares-policy-gate/template.yml) | an `include: component:` entry |

Both run the same OPA/Kyverno policy checks and **exit non-zero on any policy
violation**, failing the job. The GitHub action uses
[`run-gate.sh`](../../.github/actions/olivares-policy-gate/scripts/run-gate.sh);
the GitLab component inlines the equivalent policy sequence but does not run the
Terraform leg. This is the
*client-side* gate — distinct from the release pipeline
(`.github/workflows/release.yml`), which signs and publishes Olivares' own
artifacts.

CLI versions are **pinned via inputs** (never `latest`), so a run is reproducible
and a tool bump is an explicit, reviewable change. Confirm the current stable
release of [conftest](https://github.com/open-policy-agent/conftest/releases) and
the [Kyverno CLI](https://github.com/kyverno/kyverno/releases) and set the inputs
accordingly — the defaults baked into the artifacts are a known-good pin at
authoring time, not a guarantee of "latest".

## What the gate runs

Against the `target` you point it at, using the policy pack at `policy_dir`
(default `deploy/policy`):

1. `conftest test "$TARGET" -p "$POLICY_DIR/conftest/policy"` — OPA/Rego rules.
   With `fail_on_warn: true`, `--fail-on-warn` is added so warn-level rules also
   fail the gate.
2. `kyverno apply "$POLICY_DIR/kyverno" --resource "$TARGET"` — Kyverno policies;
   an `Enforce` violation makes the CLI exit non-zero.
3. *(GitHub action only, best-effort)* when the target tree contains `*.tf` **and**
   a `terraform` binary is on `PATH`: `terraform fmt -check` + `terraform validate`.
   If terraform is absent the leg is **skipped** (we never report a check we did
   not run); if terraform is present and the config is unformatted/invalid the gate
   **fails**.

## GitHub Actions

Add a step to any workflow. The gate fails the job on a violation:

```yaml
# .github/workflows/policy.yml (in the CUSTOMER's repo)
name: policy
on: [pull_request]
jobs:
  policy-gate:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: <public-owner>/<public-repository>/.github/actions/olivares-policy-gate@<tag>
        with:
          target: deploy/            # required: dir/file to scan
          policy_dir: deploy/policy   # default; the policy pack
          conftest_version: "0.56.0"
          kyverno_version: "1.13.2"
          fail_on_warn: "true"
```

> If you vendor the action into your own repo instead of referencing the Olivares
> repo, the `uses:` becomes a local path, e.g.
> `uses: ./.github/actions/olivares-policy-gate`. Either way the policy pack
> (`policy_dir`) and the scan `target` must be present in the checked-out tree.

The action exposes one output, `result` (`pass`), reached only on success — a
violation exits the job non-zero before any downstream step runs.

## GitLab CI/CD

Include the component and let its job run in your pipeline. The job fails on a
violation:

```yaml
# .gitlab-ci.yml (in the CUSTOMER's repo)
include:
  - component: $CI_SERVER_FQDN/<group>/<olivares-ci-project>/olivares-policy-gate@<version>
    inputs:
      target: deploy/
      policy_dir: deploy/policy
      conftest_version: "0.56.0"
      kyverno_version: "1.13.2"
      fail_on_warn: "true"
```

`<version>` is a released ref of the component project (a semver git tag, a
branch, or a commit SHA). For Catalog distribution the component project needs a
**semver tag** and a job using the `release:` keyword in its own pipeline — that
publishing pipeline is the project's concern, not this template.

The component adds a single job, `olivares-policy-gate`, in the `test` stage by
default (override with the `stage` input). It installs the pinned CLIs into the
`image` (default `debian:stable-slim`) and runs the conftest + kyverno gate.

## Exit-code contract

Both artifacts are gates, not reports: **a policy violation is a non-zero exit**
that fails the CI job. Wire the gate as a *required* check so a violating change
cannot merge. The terraform leg is the only advisory part, and only when
terraform is absent — a terraform that **is** present treats unformatted/invalid
config as a hard failure like the rest.
