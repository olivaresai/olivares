# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only

output "cluster_arn" {
  value = aws_ecs_cluster.this.arn
}

output "repository_url" {
  value = aws_ecr_repository.cp.repository_url
}
