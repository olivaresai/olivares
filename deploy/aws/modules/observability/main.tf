# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# ⛔ NEVER APPLIED.
# Log group + CPU/memory alarms + dashboard. No fabricated SLOs.

variable "name" { type = string }
variable "cluster_name" { type = string }
variable "vpc_id" { type = string }
variable "tags" {
  type    = map(string)
  default = {}
}

data "aws_caller_identity" "current" {}
data "aws_region" "current" {}

# Without kms_key_id the log groups use the AWS-managed key.
# Unapplied: CLOUD-ACC does not exist.
data "aws_iam_policy_document" "logs_kms" {
  statement {
    sid     = "EnableRoot"
    actions = ["kms:*"]
    principals {
      type        = "AWS"
      identifiers = ["arn:aws:iam::${data.aws_caller_identity.current.account_id}:root"]
    }
    resources = ["*"]
  }
  statement {
    sid = "AllowCloudWatchLogs"
    principals {
      type        = "Service"
      identifiers = ["logs.${data.aws_region.current.name}.amazonaws.com"]
    }
    actions = [
      "kms:Encrypt",
      "kms:Decrypt",
      "kms:ReEncrypt*",
      "kms:GenerateDataKey*",
      "kms:DescribeKey",
    ]
    resources = ["*"]
  }
}

resource "aws_kms_key" "logs" {
  description             = "${var.name} logs"
  enable_key_rotation     = true
  deletion_window_in_days = 30
  policy                  = data.aws_iam_policy_document.logs_kms.json
  tags                    = merge(var.tags, { Name = "${var.name}-logs" })

  lifecycle {
    prevent_destroy = true
  }
}

resource "aws_kms_alias" "logs" {
  name          = "alias/${var.name}-logs"
  target_key_id = aws_kms_key.logs.key_id
}

resource "aws_cloudwatch_log_group" "cp" {
  name              = "/olivares/${var.name}/obs"
  retention_in_days = 30
  kms_key_id        = aws_kms_key.logs.arn
  tags              = var.tags
}

# Alarms without a destination fire into the void. The topic is the
# remainder. No email subscription: that endpoint is apply-time and
# is not invented here. Unapplied: CLOUD-ACC does not exist.
resource "aws_sns_topic" "alarms" {
  name = "${var.name}-alarms"
  tags = var.tags
}

resource "aws_cloudwatch_metric_alarm" "cpu" {
  alarm_name          = "${var.name}-cpu"
  comparison_operator = "GreaterThanThreshold"
  evaluation_periods  = 3
  metric_name         = "CPUUtilization"
  namespace           = "AWS/ECS"
  period              = 60
  statistic           = "Average"
  threshold           = 80
  alarm_actions       = [aws_sns_topic.alarms.arn]
  ok_actions          = [aws_sns_topic.alarms.arn]
  dimensions = {
    ClusterName = var.cluster_name
  }
  tags = var.tags
}

resource "aws_cloudwatch_metric_alarm" "memory" {
  alarm_name          = "${var.name}-memory"
  comparison_operator = "GreaterThanThreshold"
  evaluation_periods  = 3
  metric_name         = "MemoryUtilization"
  namespace           = "AWS/ECS"
  period              = 60
  statistic           = "Average"
  threshold           = 80
  alarm_actions       = [aws_sns_topic.alarms.arn]
  ok_actions          = [aws_sns_topic.alarms.arn]
  dimensions = {
    ClusterName = var.cluster_name
  }
  tags = var.tags
}

# ⛔ AQUI HABIA UN SEGUNDO `data "aws_region" "current" {}` Y TERRAFORM LO RECHAZA:
#   Error: Duplicate data "aws_region" configuration
#   A aws_region data resource named "current" was already declared at main.tf:16
# El primero, en :16, ya sirve a los tres usos del modulo. Este era una redeclaracion
# del mismo dato, no otro dato.
#
# Llevaba invisible porque el workflow `aws-terraform` no llegaba nunca a `tofu validate`:
# moria antes descargando OpenTofu a un /tmp no escribible — CERO exitos en 40 corridas.
# Un paso que falla temprano no protege lo de abajo: lo ESCONDE.

# CLOUD-DISENO §6 observability names a panel. The widgets plot the
# same ECS metrics the alarms already watch. Not an SLO. Unapplied.
resource "aws_cloudwatch_dashboard" "cp" {
  dashboard_name = var.name
  dashboard_body = jsonencode({
    widgets = [
      {
        type   = "metric"
        x      = 0
        y      = 0
        width  = 12
        height = 6
        properties = {
          title   = "ECS CPU"
          region  = data.aws_region.current.name
          metrics = [["AWS/ECS", "CPUUtilization", "ClusterName", var.cluster_name]]
          stat    = "Average"
          period  = 60
        }
      },
      {
        type   = "metric"
        x      = 12
        y      = 0
        width  = 12
        height = 6
        properties = {
          title   = "ECS Memory"
          region  = data.aws_region.current.name
          metrics = [["AWS/ECS", "MemoryUtilization", "ClusterName", var.cluster_name]]
          stat    = "Average"
          period  = 60
        }
      },
    ]
  })
}

# VPC flow logs. Without them a deny-closed control has no packet trail.
# Unapplied: CLOUD-ACC does not exist.
resource "aws_cloudwatch_log_group" "vpc_flow" {
  name              = "/olivares/${var.name}/vpc-flow"
  retention_in_days = 30
  kms_key_id        = aws_kms_key.logs.arn
  tags              = var.tags
}

data "aws_iam_policy_document" "flow_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["vpc-flow-logs.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "flow" {
  name               = "${var.name}-vpc-flow"
  assume_role_policy = data.aws_iam_policy_document.flow_assume.json
  tags               = var.tags
}

data "aws_iam_policy_document" "flow" {
  statement {
    actions = [
      "logs:CreateLogStream",
      "logs:PutLogEvents",
      "logs:DescribeLogGroups",
      "logs:DescribeLogStreams",
    ]
    resources = ["${aws_cloudwatch_log_group.vpc_flow.arn}:*"]
  }
}

resource "aws_iam_role_policy" "flow" {
  name   = "${var.name}-vpc-flow"
  role   = aws_iam_role.flow.id
  policy = data.aws_iam_policy_document.flow.json
}

resource "aws_flow_log" "vpc" {
  vpc_id                   = var.vpc_id
  traffic_type             = "ALL"
  log_destination_type     = "cloud-watch-logs"
  log_destination          = aws_cloudwatch_log_group.vpc_flow.arn
  iam_role_arn             = aws_iam_role.flow.arn
  max_aggregation_interval = 60
  tags                     = merge(var.tags, { Name = "${var.name}-vpc-flow" })
}
