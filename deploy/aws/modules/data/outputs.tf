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

# ⛔ EL ARN DEL SECRETO MAESTRO, Y ES PRECONDICION DE QUE EL ESTATE SE PUEDA USAR. Con
# `manage_master_user_password = true` la contraseña del master la guarda AWS en un secreto
# que ELLA crea: no es ninguna de nuestras ocho ranuras y su ARN sólo se conoce despues del
# apply. Sin exportarlo, el unico camino para leerlo es la consola — es decir, a mano.
#
# ⛔ POR QUE HACE FALTA, medido el 2026-09-02: `cloud/control-plane/deploy/cloud-control-roles.sql`
# TIENE QUE CORRER COMO SUPERUSUARIO ANTES del primer arranque del plano de control —la
# migracion 009 se niega si el owner no existe (`migrations/009_cloud_control_rls.up.sql`)— y
# el plano de control migra al arrancar con la identidad MIGRATOR, que no es superusuario
# (`cmd/cloud-cp/main.go:111`). Y no habia NINGUN camino para ese paso: la RDS es
# `publicly_accessible = false`, `enable_execute_command` es **false** en los dos servicios, y
# no hay bastion ni endpoints de SSM. **Cortaba la secuencia entre APPLY 1 y APPLY 2.**
#
# Exportarlo NO da acceso: es un ARN, y leerlo exige una policy que lo nombre. Lo que hace es
# que la tarea de un solo uso pueda referenciarlo sin que nadie copie un secreto a mano.
output "master_user_secret_arn" {
  description = "ARN del secreto que AWS gestiona con la contraseña del master de RDS."
  value       = try(aws_db_instance.this.master_user_secret[0].secret_arn, "")
}
