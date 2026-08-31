# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only

variable "name" { type = string }
variable "vpc_id" { type = string }
variable "public_subnet_ids" { type = list(string) }
variable "alb_security_group_id" { type = string }
variable "certificate_arn" {
  type        = string
  description = "ACM certificate ARN. Empty until the public hostname is named."
  default     = ""
  nullable    = false
}
variable "hostname" {
  type        = string
  description = "Public hostname for ACM DNS validation. Empty until named."
  default     = ""
  nullable    = false
}
variable "access_logs_bucket" {
  type        = string
  description = "S3 bucket id for ALB access logs. Empty disables logs."
  default     = ""
}
variable "connection_logs_bucket" {
  type        = string
  description = "S3 bucket for ALB connection logs. Empty disables the block."
  default     = ""
}

variable "tags" {
  type    = map(string)
  default = {}
}

# ⛔ LAS DOS QUE PARTEN EL APPLY EN FASES, y existen por un defecto real que sin ellas
# hace imposible el primer apply con hostname.
#
# `aws_acm_certificate` devuelve su ARN **en cuanto se PIDE**, en `PENDING_VALIDATION`.
# El listener HTTPS colgaba de ese ARN (`local.cert_arn`), y **adjuntar un certificado
# pendiente a un listener falla**. Con un hostname puesto y sin su registro DNS creado
# —que es exactamente el estado del primer apply— el apply se rompía a mitad, con la red,
# RDS y los balanceadores ya creados.
#
# La partición: FASE 1 pide el certificado y publica su registro de validación crea
# los tres CNAME; FASE 2 espera la validación y sólo entonces crea el listener.

variable "ingest_hostname" {
  type        = string
  description = "Public hostname for the collector NLB (TCP passthrough, the binary terminates mTLS). It configures no AWS resource here — the NLB needs no ACM certificate — and exists so the CNAME the operator has to create is derived in one place instead of typed twice."
  default     = ""
  nullable    = false
}

variable "await_certificate_validation" {
  type        = bool
  description = "FASE 2. false: the certificate is requested and its validation record published as an output, and NO HTTPS listener is created — the apply finishes cleanly. true: the apply WAITS for DNS validation and then creates the listener. Setting it true before the CNAME exists makes the apply hang until its timeout, which is the honest failure and not a silent one."
  default     = false
  nullable    = false
}
