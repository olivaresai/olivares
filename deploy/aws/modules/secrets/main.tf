# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# ⛔ NEVER APPLIED.
# Eight named secret slots. Values
# are never in git. Recovery window keeps a delete recoverable.

variable "name" { type = string }
variable "tags" {
  type    = map(string)
  default = {}
}

locals {
  # ⛔ LAS RANURAS, Y POR QUÉ SON POCAS Y NO VEINTE.
  #
  # El plano de control **se niega a arrancar** si le falta un valor
  # (`cloud/control-plane/internal/config/config.go`, `Load()` acumula los que faltan y
  # devuelve error), y necesita **diez DSN separados por rol** más sus claves. Una ranura
  # por variable serían veinte recursos, veinte ARNs que enumerar en la policy y veinte
  # sitios donde derivar.
  #
  # ECS resuelve esto sin inventar nada: `valueFrom` admite **una clave JSON dentro del
  # secreto** (`arn:…:secret:nombre:clave::`), así que UN secreto con los diez DSN como
  # claves se referencia diez veces por separado y cada contenedor recibe sólo lo suyo.
  # El permiso sigue siendo por ARN, que es la unidad que IAM entiende.
  #
  # ⛔ Y NINGÚN VALOR VIVE AQUÍ. Terraform crea la ranura VACÍA a propósito: escribir un
  # valor desde el estate lo dejaría en el fichero de estado en claro, que es peor que no
  # tenerlo. Los valores los pone quien tiene esa autoridad, fuera de Git y fuera del plan.
  slots = [
    "dsn",              # el DSN del engine, y SÓLO ése — lo lee como FICHERO, ver compute
    "license-signing",
    "audit",
    "tls",
    "cloud-cp-admin",
    "cloud-cp-forward",
    "cloud-cp-databases", # las DIEZ URLs por rol, como claves JSON
    "cloud-cp-runtime",   # claves de API y secretos de comercio, como claves JSON
  ]
}

# Without kms_key_id Secrets Manager uses the AWS-managed key.
# This CMK is the named remainder. Unapplied: CLOUD-ACC does not exist.
resource "aws_kms_key" "secrets" {
  description             = "${var.name} secrets"
  enable_key_rotation     = true
  deletion_window_in_days = 30
  tags                    = merge(var.tags, { Name = "${var.name}-secrets" })

  lifecycle {
    prevent_destroy = true
  }
}

resource "aws_kms_alias" "secrets" {
  name          = "alias/${var.name}-secrets"
  target_key_id = aws_kms_key.secrets.key_id
}

resource "aws_secretsmanager_secret" "slot" {
  for_each                = toset(local.slots)
  name                    = "${var.name}/${each.key}"
  recovery_window_in_days = 30
  kms_key_id              = aws_kms_key.secrets.arn
  tags                    = merge(var.tags, { Slot = each.key })
}

output "secret_arns" {
  value = { for k, s in aws_secretsmanager_secret.slot : k => s.arn }
}

output "secrets_kms_key_arn" {
  value = aws_kms_key.secrets.arn
}
