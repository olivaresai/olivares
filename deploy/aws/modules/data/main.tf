# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# ⛔ NEVER APPLIED.
# RDS PostgreSQL 16 Multi-AZ, db.t4g.small + 20 GB gp3 (D10-bis §2.2).
# App role is NOT the owner (RLS FORCE). That SQL is deploy/postgres/.

resource "aws_db_subnet_group" "this" {
  name       = "${var.name}-pg"
  subnet_ids = var.private_subnet_ids
  tags       = merge(var.tags, { Name = "${var.name}-pg" })
}

resource "aws_db_parameter_group" "this" {
  name   = "${var.name}-pg16"
  family = "postgres16"
  tags   = var.tags
}

# CLOUD-DISENO §6 data module names KMS. storage_encrypted without
# kms_key_id uses the AWS-managed RDS key. This CMK is the named
# remainder. Unapplied: CLOUD-ACC does not exist.
resource "aws_kms_key" "rds" {
  description             = "${var.name} RDS storage"
  enable_key_rotation     = true
  deletion_window_in_days = 30
  tags                    = merge(var.tags, { Name = "${var.name}-rds" })

  lifecycle {
    prevent_destroy = true
  }
}

resource "aws_kms_alias" "rds" {
  name          = "alias/${var.name}-rds"
  target_key_id = aws_kms_key.rds.key_id
}

data "aws_iam_policy_document" "rds_monitoring_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["monitoring.rds.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "rds_monitoring" {
  name               = "${var.name}-rds-monitoring"
  assume_role_policy = data.aws_iam_policy_document.rds_monitoring_assume.json
  tags               = var.tags
}

resource "aws_iam_role_policy_attachment" "rds_monitoring" {
  role       = aws_iam_role.rds_monitoring.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AmazonRDSEnhancedMonitoringRole"
}

resource "aws_db_instance" "this" {
  identifier                 = "${var.name}-pg"
  engine                     = "postgres"
  engine_version             = "16"
  instance_class             = "db.t4g.small"
  allocated_storage          = 20
  # 0 is off. Must be strictly greater than allocated_storage.
  # Unapplied: CLOUD-ACC does not exist.
  max_allocated_storage      = 100
  storage_type               = "gp3"
  # gp3 baseline. Unnamed IOPS/throughput can silently drop.
  # Unapplied: CLOUD-ACC does not exist.
  iops                       = 3000
  storage_throughput         = 125
  multi_az                   = true
  db_subnet_group_name       = aws_db_subnet_group.this.name
  vpc_security_group_ids     = [var.rds_security_group_id]
  parameter_group_name       = aws_db_parameter_group.this.name
  manage_master_user_password = true
  username                    = "olivares_admin"
  # deletion_protection stops terraform destroy. Skipping the final
  # snapshot would still drop the volume with no copy if that guard
  # is lifted.
  skip_final_snapshot         = false
  publicly_accessible         = false
  deletion_protection         = true
  backup_retention_period     = 7
  # Retention without a window lets AWS pick a random hour.
  # UTC. Maintenance starts after the daily backup window.
  # Unapplied: CLOUD-ACC does not exist.
  # ⛔ `backup_window`/`maintenance_window`, NO `preferred_*`: esos son los nombres de la API
  # de AWS y del recurso `aws_rds_cluster`, pero `aws_db_instance` los llama sin prefijo.
  # Preguntado al esquema del proveedor FIJADO (hashicorp/aws 5.100.0, el que `versions.tf`
  # clava exacto): `preferred_backup_window` y `preferred_maintenance_window` NO estan entre
  # sus 81 atributos; `backup_window` y `maintenance_window` SI. Control positivo de que el
  # esquema se leyo bien: `backup_retention_period`, que esta tres lineas arriba, tambien SI.
  #
  # Llevaba invisible porque `aws-terraform` no alcanzaba nunca `tofu validate`.
  backup_window      = "03:00-04:00"
  maintenance_window = "sun:05:00-sun:06:00"
  storage_encrypted           = true
  kms_key_id                  = aws_kms_key.rds.arn
  # Token auth in addition to the master secret. Not a replacement.
  # Unapplied: CLOUD-ACC does not exist.
  iam_database_authentication_enabled = true
  # Query trail on the same CMK. Not a second key (F4 remainder
  # is one aws_kms_key named rds). Unapplied: CLOUD-ACC does not exist.
  performance_insights_enabled          = true
  performance_insights_retention_period = 7
  performance_insights_kms_key_id       = aws_kms_key.rds.arn
  # AWS default CA for new instances. Named so a later CA is a diff.
  # Unapplied: CLOUD-ACC does not exist.
  ca_cert_identifier          = "rds-ca-rsa2048-g1"
  # OS metrics (CPU steal, disk queue) are not in engine logs. Interval 0
  # is off. 60s is the coarsest enabled interval. Unapplied: CLOUD-ACC
  # does not exist.
  monitoring_interval         = 60
  monitoring_role_arn         = aws_iam_role.rds_monitoring.arn
  copy_tags_to_snapshot       = true
  auto_minor_version_upgrade  = true
  # postgresql + upgrade: the engine log and the version jump. Not
  # error/slowquery — those names are MySQL. Never applied.
  enabled_cloudwatch_logs_exports = ["postgresql", "upgrade"]
  tags                        = merge(var.tags, { Name = "${var.name}-pg" })

  lifecycle {
    prevent_destroy = true
  }
}

# Plane object store. ALC-02-F1 names versioned S3. Public access
# stays blocked. AES256: the RDS CMK is for the volume. Unapplied.
resource "aws_s3_bucket" "plane" {
  bucket_prefix = "${var.name}-plane-"
  tags          = merge(var.tags, { Name = "${var.name}-plane" })

  lifecycle {
    prevent_destroy = true
  }
}

resource "aws_s3_bucket_versioning" "plane" {
  bucket = aws_s3_bucket.plane.id
  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_bucket_public_access_block" "plane" {
  bucket                  = aws_s3_bucket.plane.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_server_side_encryption_configuration" "plane" {
  bucket = aws_s3_bucket.plane.id
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

# Server-access logs of the plane bucket. Not ALB access logs
# (#1225): those are ELB lines written INTO the plane bucket.
# Logging onto the source bucket loops. AWS requires a sibling
# destination. Unapplied: CLOUD-ACC does not exist.
data "aws_caller_identity" "s3_logs" {}

resource "aws_s3_bucket" "plane_logs" {
  bucket_prefix = "${var.name}-planelogs-"
  tags          = merge(var.tags, { Name = "${var.name}-planelogs" })

  lifecycle {
    prevent_destroy = true
  }
}

resource "aws_s3_bucket_public_access_block" "plane_logs" {
  bucket                  = aws_s3_bucket.plane_logs.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_server_side_encryption_configuration" "plane_logs" {
  bucket = aws_s3_bucket.plane_logs.id
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

resource "aws_s3_bucket_policy" "plane_logs" {
  bucket = aws_s3_bucket.plane_logs.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid    = "S3ServerAccessLogsPolicy"
        Effect = "Allow"
        Principal = {
          Service = "logging.s3.amazonaws.com"
        }
        Action   = "s3:PutObject"
        Resource = "${aws_s3_bucket.plane_logs.arn}/s3/*"
        Condition = {
          ArnLike = {
            "aws:SourceArn" = aws_s3_bucket.plane.arn
          }
          StringEquals = {
            "aws:SourceAccount" = data.aws_caller_identity.s3_logs.account_id
          }
        }
      },
    ]
  })
}

resource "aws_s3_bucket_logging" "plane" {
  bucket        = aws_s3_bucket.plane.id
  target_bucket = aws_s3_bucket.plane_logs.id
  target_prefix = "s3/"
}

# ALB connection logs (TLS handshakes). Not #1225 access logs:
# those go INTO the plane bucket with the regional ELB account.
# ELB connection logs require SSE-S3 and reject a prefix that
# contains AWSLogs. The current delivery principal is
# logdelivery.elasticloadbalancing.amazonaws.com.
# Unapplied: CLOUD-ACC does not exist.
data "aws_caller_identity" "alb_conn" {}

resource "aws_s3_bucket" "alb_conn" {
  bucket_prefix = "${var.name}-albconn-"
  tags          = merge(var.tags, { Name = "${var.name}-albconn" })

  lifecycle {
    prevent_destroy = true
  }
}

resource "aws_s3_bucket_public_access_block" "alb_conn" {
  bucket                  = aws_s3_bucket.alb_conn.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_server_side_encryption_configuration" "alb_conn" {
  bucket = aws_s3_bucket.alb_conn.id
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

resource "aws_s3_bucket_policy" "alb_conn" {
  bucket = aws_s3_bucket.alb_conn.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid    = "ALBConnectionLogsWrite"
        Effect = "Allow"
        Principal = {
          Service = "logdelivery.elasticloadbalancing.amazonaws.com"
        }
        Action   = "s3:PutObject"
        Resource = "${aws_s3_bucket.alb_conn.arn}/alb-conn/AWSLogs/${data.aws_caller_identity.alb_conn.account_id}/*"
      },
    ]
  })
}

# ALB access logs. Regional ELB account PutObject. Unapplied.
data "aws_caller_identity" "current" {}
data "aws_elb_service_account" "current" {}

resource "aws_s3_bucket_policy" "plane_alb_logs" {
  bucket = aws_s3_bucket.plane.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid    = "AWSLogDeliveryWrite"
        Effect = "Allow"
        Principal = {
          AWS = data.aws_elb_service_account.current.arn
        }
        Action   = "s3:PutObject"
        Resource = "${aws_s3_bucket.plane.arn}/alb/AWSLogs/${data.aws_caller_identity.current.account_id}/*"
      },
      {
        Sid    = "AWSLogDeliveryAclCheck"
        Effect = "Allow"
        Principal = {
          AWS = data.aws_elb_service_account.current.arn
        }
        Action   = "s3:GetBucketAcl"
        Resource = aws_s3_bucket.plane.arn
      },
    ]
  })
}

# ACLs off. Incomplete multipart uploads otherwise live forever.
# Does not expire current objects (F1 restore stays HOLD).
# Unapplied: CLOUD-ACC does not exist.
resource "aws_s3_bucket_ownership_controls" "plane" {
  bucket = aws_s3_bucket.plane.id
  rule {
    object_ownership = "BucketOwnerEnforced"
  }
}

resource "aws_s3_bucket_lifecycle_configuration" "plane" {
  bucket = aws_s3_bucket.plane.id
  rule {
    id     = "abort-incomplete-mpu"
    status = "Enabled"
    filter {}
    abort_incomplete_multipart_upload {
      days_after_initiation = 7
    }
  }
}
