# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only

output "vpc_id" {
  value = aws_vpc.this.id
}

output "public_subnet_ids" {
  value = aws_subnet.public[*].id
}

output "private_subnet_ids" {
  value = aws_subnet.private[*].id
}

output "task_security_group_id" {
  value = aws_security_group.tasks.id
}

output "alb_security_group_id" {
  value = aws_security_group.alb.id
}

output "nlb_security_group_id" {
  value = aws_security_group.nlb.id
}

output "rds_security_group_id" {
  value = aws_security_group.rds.id
}

output "s3_endpoint_id" {
  value = aws_vpc_endpoint.s3.id
}

output "interface_endpoint_ids" {
  value = { for k, ep in aws_vpc_endpoint.aws_interface : k => ep.id }
}
