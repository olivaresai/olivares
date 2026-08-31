# Read the reconciled estate inventory (read-only).
data "olivares_inventory" "agents" {
  kind = "agent"
}

output "agent_count" {
  value = data.olivares_inventory.agents.summary.total
}
