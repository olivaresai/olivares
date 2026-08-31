<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# Terraform Provider for Olivares AI

A Terraform provider that manages Olivares AI control-plane resources as infrastructure: workspaces, agents and their non-human identities, RBAC grants, governance policies, model access rules and model groups, FinOps budgets, MCP capability configs, deployments, and notification routes.

**License:** AGPL-3.0-only.

## Resources

The table below matches the resources implemented in [`internal/provider/`](internal/provider/); the authoritative per-resource schema is generated into [`docs/resources/`](docs/resources/).

| Resource | Purpose |
|---|---|
| `olivares_agent` | An agent registered in the control plane |
| `olivares_agent_identity_binding` | Binds an agent to its non-human (NHI) identity (module VI) |
| `olivares_budget` | A FinOps spend budget — a named, enabled spend cap (module XI) |
| `olivares_capability_config` | An MCP server connection configuration (capabilities module) |
| `olivares_deployment` | Desired state of an agent/MCP deployment (module VII) |
| `olivares_model_access` | A subject-scoped model access rule (models module) |
| `olivares_model_group` | A named set of model identifiers (models module) |
| `olivares_notification_route` | A rule that fans matching events to a channel (notify module) |
| `olivares_policy` | A governance policy (module VI) |
| `olivares_rbac_grant` | An immutable RBAC binding of subject → role → scope (module I) |
| `olivares_workspace` | A named isolation boundary (sessions module) |

## Example

```hcl
provider "olivares" {
  endpoint  = "https://control-plane.example.com"
  api_token = var.olivares_api_token
  # tenant is optional at the provider level; any resource may override it with its own `tenant`.
}

resource "olivares_workspace" "dev" {
  name        = "development"
  description = "Development workspace for experimentation"
}

resource "olivares_model_group" "production" {
  name        = "production-models"
  description = "Models approved for production use"
  models      = ["gpt-4o", "claude-sonnet-4-5", "claude-haiku-3-5"]
}
```

## Development

```sh
go build ./...                    # build the provider
go test ./internal/provider/...   # run provider unit/resource tests
go generate ./...                 # regenerate docs from schema
```

## Usage

See [`docs/`](docs/) for the generated Terraform documentation and [`examples/`](examples/) for usage patterns.

## Registry

The provider will be published to the [Terraform Registry](https://registry.terraform.io/) with the first tagged release. Until then, build from source and configure as a [dev override](https://developer.hashicorp.com/terraform/cli/config/config-file#development-overrides-for-provider-developers).
