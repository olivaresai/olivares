<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->
# Control plane as code — GitOps reconciliation

Manage an **Olivares AI control plane's governance estate entirely as code**, and
have a GitOps engine reconcile it continuously. The desired state — agents,
policies, NHI identity bindings, FinOps budgets, MCP-server (connector) configs,
notification routes — lives as HCL in [`terraform/`](terraform/) and is applied
through the [`terraform-provider-olivares`](../../../terraform-provider-olivares)
against the running engine's REST API.

> **Release status:** these are source-ready templates, not an executable public
> reconciliation loop yet. `terraform/versions.tf` requires provider
> `olivaresai/olivares` v0.1.0, which has not been published. In-cluster Flux/Argo
> reconciliation cannot use a workstation dev override and will fail to install the
> provider until its first registry release. For local evaluation, build the provider
> and configure the [development override](../../../terraform-provider-olivares/README.md#registry)
> before running `tofu` directly.

> **This is a different loop from the sibling [`../`](../README.md).** That
> directory reconciles the **engine's deployment** (the Helm chart / install
> manifests — *is the control plane running?*). This directory reconciles the
> **control plane's own desired state** (*are the right agents, policies and
> budgets declared in it?*). Both are GitOps; they sit at different layers.

| Engine | Files | Executor |
|---|---|---|
| **Flux** (tofu-controller) | [`flux/gitrepository.yaml`](flux/gitrepository.yaml), [`flux/terraform.yaml`](flux/terraform.yaml) | OpenTofu, in-cluster, on an interval |
| **Argo CD** | [`argocd/application.yaml`](argocd/application.yaml) | a Terraform/OpenTofu Config Management Plugin, **or** Argo syncing the Flux CR |
| **CI (no GitOps engine)** | [`terraform/`](terraform/) | `tofu plan && tofu apply` in your pipeline (see the [reusable CI artifacts](../../ci/)) |

## What is reconciled — and what is *not*

The loop reconciles **declared governance state**. Crucially, it does **not**
actuate infrastructure:

- `olivares_agent`, `olivares_policy`, `olivares_agent_identity_binding`,
  `olivares_budget`, `olivares_capability_config`, `olivares_notification_route`
  are reconciled — create / update / delete to match Git.
- `olivares_deployment` records **desired state only**. Reconciling a deployment
  to real infrastructure (the actual provision/update/retire) stays a **separate,
  human-in-the-loop-gated action in the engine** — it is *never* triggered by a
  `terraform apply`, GitOps or otherwise. Every mutation still passes the engine's
  authz + HITL; the GitOps token is a least-privilege manage-as-code credential,
  not an actuation bypass.

So the guarantee is **"declared state, continuously reconciled"** — the manage-as-
code promise — with the engine remaining the sole authority that *acts*.

## OpenGitOps 1.0 alignment

Once the provider is published, this loop is designed around the four
[OpenGitOps v1.0.0](https://opengitops.dev/) principles for the control-plane
estate:

1. **Declarative** — the whole estate is HCL in `terraform/`; nothing is mutated
   imperatively against the API by hand.
2. **Versioned and immutable** — desired state lives in Git and the provider is
   pinned to an exact version (`terraform/versions.tf`); a rollback is a revert to
   a prior commit/tag. Pin the `GitRepository`/`Application` to a tag or commit
   (not a moving branch) in production.
3. **Pulled automatically** — Flux's source-controller (or Argo CD) **pulls** the
   repo on its reconcile interval; no CI runner pushes into the estate with
   long-lived credentials.
4. **Continuously reconciled** — the in-cluster agent (tofu-controller / Argo)
   re-plans on its interval and converges the live estate toward Git. Drift the
   provider detects on `Read` (an out-of-band policy edit, a changed budget limit)
   is surfaced and corrected.

## Flux (tofu-controller)

```sh
kubectl apply -f flux/        # GitRepository + Terraform CR
```

`flux/terraform.yaml` defaults to `approvePlan: "auto"` (continuous apply of the
declarative estate). Remove that line for a **reviewed-apply gate**: the
controller then plans every interval and waits for a human to approve the pending
plan — the same posture the engine-deploy GitOps takes in [`../`](../README.md).

> **Version caveat.** tofu-controller is a Flux **community** add-on,
> not Flux core; its CRD `apiVersion` (`infra.contrib.fluxcd.io/v1alpha2` here)
> moves between releases. Re-verify against the version you install. No fabricated
> controller version is pinned.

## Argo CD

Argo CD has **no native Terraform runner**. Two honest paths
(`argocd/application.yaml` documents both):

1. Register a **Config Management Plugin** that runs `tofu`, and point the
   `Application` at `terraform/`.
2. Let Argo CD **sync the Flux tofu-controller `Terraform` CR** in `flux/` and let
   that controller execute — Argo owns the declarative sync, tofu-controller is
   the executor.

Sync is **manual by default** (a governance change is a reviewed action); opt into
auto-sync per the file.

## State & secrets

- **State** can hold sensitive computed values — keep it in a remote backend the
  runner can reach (see `terraform/versions.tf`), never in the estate's Git repo.
  With OpenTofu, enable [state & plan encryption](https://opentofu.org/docs/language/state/encryption/)
  on the consumer side:

  ```hcl
  terraform {
    encryption {
      key_provider "pbkdf2" "k" { passphrase = "…(min 16 chars, from a secret)…" }
      method "aes_gcm" "m" { keys = key_provider.pbkdf2.k }
      state { method = method.aes_gcm.m }
      plan  { method = method.aes_gcm.m }
    }
  }
  ```

- **Credentials** — the provider reads `OLIVARES_ENDPOINT` / `OLIVARES_API_TOKEN`
  from the runner environment, sourced from a Kubernetes Secret. The token is a
  least-privilege manage-as-code credential; the engine still enforces its scopes.
