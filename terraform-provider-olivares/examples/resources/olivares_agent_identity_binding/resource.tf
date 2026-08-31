# Bind an agent to its non-human (NHI) identity. Exactly one of identity_id,
# identity_ref or mint selects the target.
resource "olivares_agent_identity_binding" "billing" {
  agent_id     = olivares_agent.billing.id
  identity_ref = "spiffe://acme/billing"
}
