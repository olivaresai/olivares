# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Provider + backend pinning for the "control plane as code" estate. Versioned
# and immutable (OpenGitOps #2): pin the provider to an exact version, never a
# floating range, so a reconcile is reproducible and a rollback is a revert.

terraform {
  # Works on both Terraform and OpenTofu (the OSS default —).
  required_version = ">= 1.6"

  required_providers {
    olivares = {
      source  = "olivaresai/olivares"
      version = "0.1.0" # pin exactly; bump deliberately in a reviewed commit
    }
  }

  # State lives outside Git (it can hold sensitive computed values). Use a remote
  # backend the GitOps runner can reach; with OpenTofu, enable state encryption
  # (see the consumer-side example in ../README.md). Example (commented — wire to
  # your infra):
  #
  # backend "s3" {
  #   bucket = "acme-tfstate"
  #   key    = "olivares/olivares.tfstate"
  #   region = "eu-west-1"
  # }
}

# Endpoint + token come from OLIVARES_ENDPOINT / OLIVARES_API_TOKEN in the
# runner's environment (a least-privilege, manage-as-code token). Never commit
# the token to Git.
provider "olivares" {}
