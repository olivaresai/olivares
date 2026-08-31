# Declares DESIRED STATE only. Reconciling it to real infrastructure is a
# separate, human-in-the-loop-governed action in the engine — never triggered by
# terraform apply.
resource "olivares_deployment" "billing_prod" {
  subject_kind = "agent"
  subject_ref  = olivares_agent.billing.id
  name         = "billing-prod"
  environment  = "prod"
  runtime      = "k8s"
  target       = "k8s.namespace/prod"
  source_ref   = "git:github.com/acme/agents#main"

  spec = jsonencode({
    image     = "ghcr.io/acme/billing-agent:1.4.2"
    resources = { cpu = "500m", memory = "512Mi" }
  })
}
