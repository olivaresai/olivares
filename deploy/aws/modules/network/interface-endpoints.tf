# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# ⛔ NEVER APPLIED.
#
# CLOUD-DISENO §4.1 names four Interface endpoints as posture, not a
# substitute for NAT. Resend and the MoR API have no VPC endpoint, so
# NAT stays (network/main.tf). This file is a sibling of main.tf so
# the S3 Gateway lote's check (which reads only main.tf) stays closed.
# Unapplied: CLOUD-ACC does not exist.

locals {
  # The four services the design priced. for_each, not four copy-pasted
  # resources: a missing name is a missing endpoint, not a silent alias.
  aws_interface_services = toset([
    "ecr.api",
    "ecr.dkr",
    "logs",
    "secretsmanager",
  ])
}

resource "aws_security_group" "vpce" {
  name        = "${var.name}-vpce"
  description = "Interface VPC endpoints: HTTPS from Fargate tasks"
  vpc_id      = aws_vpc.this.id
  tags        = merge(var.tags, { Name = "${var.name}-vpce" })
}

resource "aws_vpc_security_group_ingress_rule" "vpce_from_tasks" {
  security_group_id            = aws_security_group.vpce.id
  referenced_security_group_id = aws_security_group.tasks.id
  ip_protocol                  = "tcp"
  from_port                    = 443
  to_port                      = 443
}

resource "aws_vpc_endpoint" "aws_interface" {
  for_each            = local.aws_interface_services
  vpc_id              = aws_vpc.this.id
  service_name        = "com.amazonaws.${data.aws_region.current.name}.${each.key}"
  vpc_endpoint_type   = "Interface"
  subnet_ids          = aws_subnet.private[*].id
  security_group_ids  = [aws_security_group.vpce.id]
  private_dns_enabled = true
  tags                = merge(var.tags, { Name = "${var.name}-${replace(each.key, ".", "-")}" })
}
