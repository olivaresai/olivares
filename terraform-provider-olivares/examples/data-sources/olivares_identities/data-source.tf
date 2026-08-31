data "olivares_identities" "all" {}

output "nhi_count" {
  value = length([for i in data.olivares_identities.all.identities : i if i.principal_type == "nhi"])
}
