# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only

variable "name" {
  type = string
}

variable "vpc_cidr" {
  type = string
}

variable "azs" {
  type        = list(string)
  description = "Exactly two AZs. ALB requires two subnets."
}

variable "enable_ha_nat" {
  type    = bool
  default = false
}

variable "tags" {
  type    = map(string)
  default = {}
}
