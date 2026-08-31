# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# The DESIRED STATE of an Olivares AI control plane, expressed entirely as code.
# A GitOps engine (Flux's tofu-controller or an Argo CD Terraform plugin — see
# ../flux and ../argocd) continuously reconciles this against the running control
# plane via the REST API, so the estate is declarative, versioned, pulled and
# continuously reconciled (the four OpenGitOps 1.0 principles).
#
# WHAT THIS RECONCILES vs WHAT IT DOES NOT:
#   - It reconciles DECLARED governance state: agents, the policies that bound
#     them, their NHI identity bindings, FinOps budgets, MCP-server (connector)
#     configs and notification routes.
#   - olivares_deployment records DESIRED state only. Actuating a deployment to
#     real infrastructure stays a separate, human-in-the-loop-gated action in the
#     engine — it is NEVER triggered by a terraform apply, GitOps or otherwise.
#   So the loop is "declared state, continuously reconciled"; it never silently
#   actuates infrastructure or bypasses the engine's authz/HITL.

# --- Agents -----------------------------------------------------------------
resource "olivares_agent" "billing" {
  name        = "billing-agent"
  kind        = "worker"
  external_id = "svc-billing"
}

# --- Per-agent NHI identity binding (access-map attribution) ----------------
resource "olivares_agent_identity_binding" "billing" {
  agent_id     = olivares_agent.billing.id
  identity_ref = "spiffe://acme/billing"
}

# --- Governance policy: an ABAC deny that can only narrow the RBAC grant -----
resource "olivares_policy" "deny_agent_admin" {
  name = "deny-agent-admin"
  kind = "abac"
  spec = jsonencode({
    rules = [{ deny = true, verb = "admin", resource = "agent" }]
  })
}

# --- FinOps budgets: cost guardrails as code --------------------------------
resource "olivares_budget" "monthly_global" {
  name            = "monthly-global-cap"
  dimension       = "global"
  period          = "monthly"
  limit_micro_usd = 10000 * 1000000 # $10,000
  thresholds      = [0.8, 0.9, 1.0]
}

# --- MCP server (connector) config: secrets by locator, never cleartext ------
resource "olivares_capability_config" "github_mcp" {
  server_ref = "github-mcp"
  transport  = "http"
  endpoint   = "https://mcp.internal/github"
  secret_refs = [
    {
      name     = "token"
      ref_kind = "vault"
      ref      = "secret/data/mcp/github#token"
    }
  ]
}

# --- Notification route: critical findings to on-call -----------------------
resource "olivares_notification_route" "crit_to_pager" {
  name                 = "crit-to-pager"
  destination          = "pagerduty-oncall"
  match_types          = ["finding"]
  min_severity         = "critical"
  dedup_window_seconds = 300
}

# --- Read-only references into the live estate ------------------------------
# Capability detection: branch honestly on the engine you target.
data "olivares_server_info" "this" {}

# The reconciled least-privilege drift, surfaced as an output a platform team can
# alert on (an unexpected access observed but never permitted).
data "olivares_access_edges" "map" {
  include_drift = true
}

output "control_plane_version" {
  value = data.olivares_server_info.this.version
}

output "unexpected_accesses" {
  description = "Reconciled observed-but-not-permitted access edges (least-privilege drift)."
  value       = [for d in data.olivares_access_edges.map.drift : d.id if d.kind == "unexpected_access"]
}
