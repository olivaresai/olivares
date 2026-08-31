---
title: Manage the control plane as code (Terraform)
description: Declare and reconcile control-plane objects — agents, policies,
  identity bindings and deployments — with the Olivares AI Terraform/OpenTofu
  provider, authenticated by an opaque API token against the engine's REST API.
slug: 2026-06/how-to/manage-as-code
---

Olivares AI exposes a **Terraform provider** so you can manage the control plane *as
code* — agents, governance policies, agent↔identity bindings and deployment definitions
declared in HCL and reconciled against the running engine over its REST API. This is
module XIX (own API + manage-as-code); the provider is a thin client over the same REST
surface the [API reference](/reference/api/) documents, so anything you can do in HCL you
can do over REST.

The provider and the CLI are Apache-2.0 and never import the engine internals; HCL is
just another front-end to the governed API.

## Configure the provider

```hcl
terraform {
  required_providers {
    olivares = {
      source = "olivaresai/olivares"
    }
  }
}

provider "olivares" {
  endpoint = "https://olivares.internal:8443" # or OLIVARES_ENDPOINT
  api_token = var.olivares_token                  # or OLIVARES_API_TOKEN (sensitive)
  # tenant   = "…"                                # optional; or OLIVARES_TENANT (sent as X-Olivares-Tenant)
  # insecure_skip_verify = true                   # dev self-signed cert only
}
```

| Setting | Required | Env fallback | Notes |
|---|---|---|---|
| `endpoint` | yes | `OLIVARES_ENDPOINT` | Base URL of the control-plane API |
| `api_token` | yes | `OLIVARES_API_TOKEN` | **Opaque bearer token** (the product uses opaque, revocable tokens, not JWTs) |
| `tenant` | no | `OLIVARES_TENANT` | Tenant UUID; omit when the token is tenant-bound |
| `insecure_skip_verify` | no | — | Skip TLS verification for the dev self-signed cert; never in production |

Authentication is a bearer token sent on every request, with the tenant carried in the
`X-Olivares-Tenant` header — the same deny-by-default RBAC, tenant scoping and per-action
auditing as the rest of the API. Mint a token for a least-privilege service identity, and
keep it out of state (use a variable and a secret backend).

## Resources

| Resource | Manages | Key attributes |
|---|---|---|
| `olivares_agent` | An agent entity in the inventory | `name` (required), `kind` (required), `external_id` (optional); computed `id`, `status`, `version` |
| `olivares_policy` | A governance policy | `name` (required), `kind` (`abac` or `approval`, required, immutable), `enabled`, `spec` (required, JSON); computed `spec_canonical` |
| `olivares_agent_identity_binding` | Bind an agent to a non-human identity (the bridge that sharpens R/RW attribution) | `agent_id`, `identity_id`/`identity_ref`, `mint`, `allow_unknown`; computed `minted`, `shared`, `agent_count` |
| `olivares_deployment` | A deployment **definition** (declarative desired state) | `subject_kind`, `subject_ref`, `name`, `environment`, `runtime`, `target`, `source_ref`, `spec`, `desired_status`; computed `current_version`, `applied_version`, `spec_hash` |

## Data sources

Read-only views so a module can reference governed state without reimplementing REST
calls: `olivares_policies`, `olivares_identities`, `olivares_deployment`,
`olivares_server_info`, and `olivares_access_edges` — the latter exposes the R/RW edges
and, with `include_drift = true`, the Permitted-vs-Observed drift (including the honest
`reconciliation_pending` flag for an access not yet firmly attributable).

## A minimal example

```hcl
resource "olivares_agent" "billing_bot" {
  name = "billing-reconciler"
  kind = "service"
}

resource "olivares_policy" "require_approval_for_prod" {
  name    = "prod-deploys-need-approval"
  kind    = "approval"
  enabled = true
  spec    = jsonencode({
    # policy body — see the API reference for the schema of each kind
  })
}

# Read the current Permitted-vs-Observed drift as data:
data "olivares_access_edges" "estate" {
  include_drift = true
}
```

`terraform plan` reconciles your HCL against the engine; `terraform apply` creates or
updates the objects through the governed API. Because policies and bindings change the
authorization surface, treat the plan as a reviewable change — the engine audits every
mutation with the real actor.

:::caution[`olivares_deployment` declares desired state; live apply is gated]
`olivares_deployment` manages a deployment **definition** — declarative, versioned
desired state. It maps to module VII (deploy), whose live actuation is a **deny-closed
seam**: until an executor is provisioned, the engine *plans and governs* a deployment but
**`apply`/`retire` return `503`** rather than acting on infrastructure. So a
`olivares_deployment` resource records and governs intent today; it does not by itself
reconcile real infrastructure. See [module VII](/2026-06/reference/modules/vii-deploy/) and
[Honesty & limits](/2026-06/start/honesty-and-limits/).
:::

:::note[The provider is a subset of the API, on purpose]
The provider covers the manage-as-code objects above. The full governed surface — and
the field-level schema of each `spec` — is the REST API; some module routes are reachable
but deliberately outside the served OpenAPI document. Verify a resource's attributes
against `terraform providers schema -json` and the [API reference](/reference/api/) before
relying on them; this page does not reproduce schema it cannot keep in lockstep with code.
:::

## Related

* [API reference](/reference/api/) — the REST surface the provider drives.
* [API stability policy](/2026-06/reference/api-stability/) — the versioning/deprecation commitment the provider relies on (it warns once per run when a response carries a deprecation signal).
* [Module XIX — own API + manage-as-code](/2026-06/reference/modules/xix-api-manage-as-code/).
* [Module VII — deployment & integration](/2026-06/reference/modules/vii-deploy/) — the 503 seam caveat above.
* [Govern and approve](/2026-06/how-to/govern-and-approve/) — how policy and approvals govern what you declare.
