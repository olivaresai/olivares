<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# Client-side policy gate pack

A drop-in pack of **client-side** policy rules that validate a customer's
IaC / Kubernetes manifests against the Olivares AI governance posture **before**
they merge (and, optionally, at admission). It is shipped in two interchangeable
engines so a team can adopt whichever it already runs:

- **OPA / Conftest** (`conftest/`) — Rego rules for a pre-merge CI gate over
  rendered manifests.
- **Kyverno** (`kyverno/`) — equivalent `ClusterPolicy` resources for an
  in-cluster admission gate (or a pre-merge `kyverno apply` check).

> **This pack is NOT the policy decision point.** The control plane's PDP is
> Cedar + OPA and remains the runtime authority. This pack is the
> **client-side binding** of the same **deny-by-default** stance: it mirrors the
> posture onto the customer's own manifests so a violation is caught at the
> source, before it ever reaches a cluster. It never calls the PDP, never sends a
> manifest anywhere, and reimplements no part of the decision engine.

## What the rules enforce

All three rules express the Olivares governance posture — **deny-by-default,
minimal-data, identity-bound, no inline secrets** — as a *refusal unless the
manifest proves it is safe* (the same shape the runtime ABAC applies: deny unless
the actor and action are known-good).

| Rule | Conftest (Rego) | Kyverno (ClusterPolicy) | Posture |
| --- | --- | --- | --- |
| No inline secrets | `deny_inline_secret.rego` | `disallow-inline-secrets.yaml` | minimal-data |
| Drop root | `deny_runs_as_root.rego` | `require-run-as-non-root.yaml` | least privilege |
| Identity-bound | `require_governed_labels.rego` | `require-governance-labels.yaml` | identity-bound |

### No inline secrets

A credential must live in a Kubernetes `Secret` and be **referenced**
(`valueFrom.secretKeyRef`, `secretRef`, a mounted Secret volume) — never pasted
as a literal value into a manifest. The Rego rule denies (a) any container env
var whose name matches `(?i)(password|secret|token|api[_-]?key)` that sets an
inline `value`, and (b) any other field whose key is sensitive and whose value is
a plain literal string (catching credentials embedded in a CRD, ConfigMap, or
annotation). A value sourced from a Secret is a reference object, not a literal,
so it is exempt. An inline credential is committed to Git and survives in history
forever — a standing exfiltration path; the gate stops it at merge.

### Drop root (`runAsNonRoot`)

A workload must declare `runAsNonRoot: true`, either on the pod `securityContext`
(covers every container) or on **every** container's `securityContext`. Anything
else — absent, or an explicit `false` — is treated as *not proven non-root* and
denied. This is the same control the upstream Pod Security **restricted** profile
applies; here it is bound client-side so the customer catches it before merge
rather than at admission.

### Identity-bound governance labels

A `Deployment` must carry both governance labels with non-empty values:

- `olivares.ai/tenant` — the tenant the workload belongs to;
- `olivares.ai/identity` — the workload identity it runs as.

Without them an observed access flow cannot be bound to a subject, so the runtime
ABAC stance (deny unless the actor is known) has nothing to key on. The gate
refuses an unattributable Deployment. This binds the manifest to the identity the
PDP later evaluates; it does **not** itself decide access.

## How to run

### Conftest (OPA / Rego)

```sh
# Gate a rendered manifest (exit 2 on any deny; --fail-on-warn makes warn exit 1):
conftest test --policy conftest/policy testdata/violating.yaml   # FAILS (3 findings)
conftest test --policy conftest/policy testdata/conformant.yaml  # passes

# Unit-test the rules themselves (OPA test_ convention):
conftest verify --policy conftest/policy
```

Pipe rendered output straight in (the usual CI shape):

```sh
helm template ./chart | conftest test --policy conftest/policy -
kustomize build ./overlay | conftest test --policy conftest/policy -
```

### Kyverno

```sh
# Pre-merge / CI: apply the policies to a directory of manifests
# (exit 1 on failure):
kyverno apply kyverno/ --resource kyverno/testdata/bad.yaml    # FAILS
kyverno apply kyverno/ --resource kyverno/testdata/good.yaml   # passes

# Unit-test the policies against the good/bad fixtures:
kyverno test kyverno/
```

In-cluster, the `ClusterPolicy` resources install with `kubectl apply -f kyverno/`
(omitting `kyverno-test.yaml`). They use `validationFailureAction: Enforce`, so a
non-conforming admission request is **denied** — the in-cluster expression of the
same deny-by-default gate. Switch a policy to `Audit` to roll it out in
report-only mode first.

## OCI distribution

Conftest policy bundles are OCI artifacts, so this pack can be pushed to any OCI
registry and pulled by a customer's CI without vendoring the Rego:

```sh
# Publish the bundle (the directory of *.rego):
conftest push <oci-registry>/olivares/policy:<tag> conftest/policy

# Consume it in a gate:
conftest pull <oci-registry>/olivares/policy:<tag>
conftest test --update <oci-registry>/olivares/policy:<tag> <manifest>
```

Pin a concrete `<tag>` (or the artifact digest) in CI so the gate is
reproducible. Kyverno policies are plain Kubernetes resources and are distributed
as the YAML in `kyverno/` (e.g. via the customer's GitOps repo).

## Layout

```
deploy/policy/
├── conftest/
│   └── policy/
│       ├── deny_inline_secret.rego        + _test.rego
│       ├── deny_runs_as_root.rego         + _test.rego
│       └── require_governed_labels.rego   + _test.rego
├── kyverno/
│   ├── disallow-inline-secrets.yaml
│   ├── require-run-as-non-root.yaml
│   ├── require-governance-labels.yaml
│   ├── kyverno-test.yaml
│   └── testdata/{good,bad}.yaml
├── testdata/{conformant,violating}.yaml   # sample manifests for conftest
└── README.md
```
