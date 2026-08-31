# An ABAC deny policy can only further-restrict the RBAC grant (it never widens).
resource "olivares_policy" "deny_agent_write" {
  name = "deny-agent-write"
  kind = "abac"

  spec = jsonencode({
    rules = [{ deny = true, verb = "write", resource = "agent" }]
  })
}

# An approval policy declares the human-in-the-loop chain for matching actions.
resource "olivares_policy" "approve_prod_deploys" {
  name = "approve-prod-deploys"
  kind = "approval"

  spec = jsonencode({
    required_approvals = 2
    expires_in_seconds = 3600
    match              = { action = "deploy.apply", subject_kind = "agent" }
  })
}
