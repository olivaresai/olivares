data "olivares_deployment" "billing_prod" {
  id = olivares_deployment.billing_prod.id
}

output "applied_version" {
  value = data.olivares_deployment.billing_prod.applied_version
}
