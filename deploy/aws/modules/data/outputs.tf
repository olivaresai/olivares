# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only

output "db_instance_id" {
  value = aws_db_instance.this.id
}

output "rds_kms_key_arn" {
  value = aws_kms_key.rds.arn
}

output "plane_bucket_id" {
  value = aws_s3_bucket.plane.id
}

output "plane_bucket_arn" {
  value = aws_s3_bucket.plane.arn
}

output "alb_conn_bucket_id" {
  value = aws_s3_bucket.alb_conn.id
}
