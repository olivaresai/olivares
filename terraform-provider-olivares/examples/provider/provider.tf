terraform {
  required_providers {
    olivares = {
      source = "olivaresai/olivares"
    }
  }
}

# Endpoint and api_token may also be supplied via the OLIVARES_ENDPOINT and
# OLIVARES_API_TOKEN environment variables (the api_token is sensitive — prefer
# the env var or a secret manager over a literal in source).
provider "olivares" {
  endpoint  = "https://127.0.0.1:8443"
  api_token = var.olivares_api_token

  # insecure_skip_verify = true  # only for the self-signed dev cert
}
