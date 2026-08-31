data "olivares_policies" "abac" {
  kind = "abac"
}

output "abac_policy_names" {
  value = [for p in data.olivares_policies.abac.policies : p.name]
}
