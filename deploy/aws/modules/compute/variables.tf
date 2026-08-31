# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only

variable "name" { type = string }
variable "private_subnet_ids" { type = list(string) }
variable "task_security_group_id" { type = string }
variable "desired_count" { type = number }
variable "image" {
  type        = string
  description = "Signed control-plane image. Empty until ECR has a digest."
  default     = ""
  nullable    = false
}
variable "engine_image" {
  type        = string
  description = "Signed engine image. Empty until ECR has a digest."
  default     = ""
  nullable    = false
}
variable "dsn_secret_arn" {
  type        = string
  description = "Secrets Manager ARN for the engine DSN. Empty until first apply."
  default     = ""
}
variable "alb_target_group_arn" {
  type    = string
  default = ""
}
variable "nlb_target_group_arn" {
  type    = string
  default = ""
}
variable "tags" {
  type    = map(string)
  default = {}
}

# ⛔ LOS TRES BOOLEANOS QUE EXISTEN PORQUE UN `count` NO PUEDE MIRAR UN ARN.
#
# Medido: el apply 1 del estate (run 33244273912) produjo su plan —99 recursos— y murió con
# «Invalid count argument» en `aws_iam_role_policy.execution_secrets`, cuyo `count` leía
# `var.dsn_secret_arn`. Ese ARN sale de `module.secrets` **en el mismo apply**, así que en
# tiempo de plan es desconocido y OpenTofu no puede saber cuántas instancias crear.
#
# El arreglo NO es el `-exclude` que sugiere la herramienta —eso es un rodeo que convierte
# cada primer apply en un caso especial—: es separar los DOS hechos que estaban mezclados.
# *Si existe* el secreto se sabe al planificar, porque lo decide el llamador; *cuál es su
# ARN* no se sabe hasta aplicar. El booleano lleva el primero y el ARN sigue llevando el
# segundo, **como valor y nunca como condición**.
#
# Que no puedan derivar lo impone una `precondition` en cada recurso: si el booleano dice
# que sí y el ARN llega vacío, el apply para y lo dice. Un hecho en dos sitios deriva, y
# aquí la separación es obligatoria — así que se ata.

variable "dsn_secret_enabled" {
  type        = bool
  description = "Whether the caller manages a DSN secret whose ARN it passes in dsn_secret_arn. Known at plan time; the ARN is not. The root sets it true because modules/secrets always creates the dsn slot."
  default     = true
  nullable    = false
}

variable "attach_alb_target_group" {
  type        = bool
  description = "Whether the control-plane service registers with the ALB target group in alb_target_group_arn. Known at plan time; the ARN is not."
  default     = true
  nullable    = false
}

variable "attach_nlb_target_group" {
  type        = bool
  description = "Whether the collector service registers with the NLB target group in nlb_target_group_arn. Known at plan time; the ARN is not."
  default     = true
  nullable    = false
}

# ⛔ LOS ARNs DE LOS SECRETOS QUE LAS TASK DEFINITIONS REFERENCIAN, Y NADA MÁS.
#
# Medido el 2026-08-29 antes de escribir una línea: `git grep` en `deploy/aws` daba **CERO**
# para `mountPoints`, `valueFrom`, `containerPath` y `volumes`, mientras el engine arrancaba
# con `--dsn file:/mnt/secrets/dsn`. Es decir: **el estate aplicaba limpio y no podía
# funcionar** — el fichero que el engine lee no lo creaba nadie, y el plano de control no
# recibía ninguno de los valores sin los que `Load()` se niega a arrancar.
#
# Aquí viajan **ARNs, nunca valores**. Un valor en una variable de Terraform acaba en el
# fichero de estado en claro, y el estado vive en S3.

variable "cp_databases_secret_arn" {
  type        = string
  description = "ARN of the secret holding the ten role-separated database URLs as JSON keys. Referenced ten times with :<key>:: so each variable comes from its own key. Empty disables the wiring."
  default     = ""
  nullable    = false
}

variable "cp_runtime_secret_arn" {
  type        = string
  description = "ARN of the secret holding the control plane's API keys and commerce secrets as JSON keys. Empty disables the wiring."
  default     = ""
  nullable    = false
}

variable "cp_secrets_enabled" {
  type        = bool
  description = "Whether the caller wires the control plane's secrets. Known at plan time; the ARNs are not — see the two booleans above for the reason this is a separate fact."
  default     = true
  nullable    = false
}

variable "engine_base_url" {
  type        = string
  description = "ENGINE_BASE_URL for the control plane. Not a secret: it is an in-VPC address."
  default     = ""
  nullable    = false
}

variable "otel_endpoint" {
  type        = string
  description = "OTEL_EXPORTER_OTLP_ENDPOINT. Not a secret. Empty is refused by the control plane at boot, so it has no default here either — the caller must decide it."
  default     = ""
  nullable    = false
}

variable "operator_alert_to" {
  type        = string
  description = "CLOUD_OPERATOR_ALERT_TO. Not a secret: an address the operator publishes anyway."
  default     = ""
  nullable    = false
}

variable "secrets_kms_key_arn" {
  type        = string
  description = "ARN of the customer key that encrypts the secret slots. Required: reading a CMK-encrypted secret needs kms:Decrypt on that key, and the execution role did not have it — the wiring would have failed at runtime even with the mount in place."
  default     = ""
  nullable    = false
}
