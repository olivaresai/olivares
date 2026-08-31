resource "olivares_agent" "billing" {
  name        = "billing-agent"
  kind        = "worker"
  external_id = "svc-billing-42"
  status      = "active"
}
