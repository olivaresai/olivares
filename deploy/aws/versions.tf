# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# ⛔ NEVER APPLIED. Pin providers exactly. No backend until CLOUD-ACC exists.

terraform {
  required_version = ">= 1.6"

  # Partial backend: validate uses `tofu init -backend=false`.
  # Apply fills bucket/key/region via -backend-config (C04-02).
  #
  # ⛔ EL BLOQUEO DE ESTADO VIVE AQUÍ, EN CÓDIGO, Y NO EN LA LÍNEA DE COMANDOS.
  # Estaba como `-backend-config="use_lockfile=true"` en el paso de apply, y el contraste
  # `sol max` del 2026-08-27 (C-01) demostró por qué eso no se puede gatear: la invariante
  # se comprobaba buscando esa cadena en el `run:` del paso, y **un `echo use_lockfile=true`
  # la satisfacía** mientras el flag real decía `false`. Una propiedad que un `echo` puede
  # fingir no es una propiedad verificable.
  #
  # Aquí es HCL, y quien la comprueba es el parser de verdad (`scripts/hcl-module-guard`,
  # el mismo que ya recorre los cuerpos de los módulos): un comentario no es un atributo y
  # una cadena en otro sitio no es este argumento. Lo que se pierde —poder cambiarlo por
  # `-backend-config` sin tocar el código— es exactamente lo que no se quiere poder hacer.
  #
  # Qué hace: escritura condicional `PutObject … IfNoneMatch: "*"` sobre
  # `<key>.tflock` (OpenTofu v1.12.1, `internal/backend/remote-state/s3/backend.go:469-473`
  # y `client.go:319-335`). Cero recursos nuevos; DynamoDB habría exigido crear la tabla
  # ANTES del apply que la crearía.
  backend "s3" {
    use_lockfile = true
  }

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "5.100.0"
    }
  }
}

provider "aws" {
  region = var.region

  default_tags {
    tags = {
      Project     = "olivares-cloud"
      ManagedBy   = "terraform"
      Environment = var.environment
      NeverApplied = "true"
    }
  }
}
