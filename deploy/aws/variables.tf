# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only

variable "region" {
  type        = string
  description = "Primary region. Ratified: us-east-1."
  default     = "us-east-1"
}

variable "environment" {
  type        = string
  description = "Deployment name (pilot, staging). Not a business catalogue."
  default     = "pilot"
}

variable "vpc_cidr" {
  type        = string
  description = "VPC CIDR. Isolated from customer RFC1918 by construction."
  default     = "10.42.0.0/16"
}

variable "desired_count" {
  type        = number
  description = "Fargate desiredCount. 2 = writer + standby for advisory-lock election."
  default     = 2
}

variable "enable_ha_nat" {
  type        = bool
  description = "Second NAT in the second AZ. Cost: +$32.85/mo hours (2026-08-02)."
  default     = false
}

# ── LAS CUATRO QUE LA RAÍZ NO TENÍA, Y EL DEFECTO QUE ARREGLAN ────────────────
#
# ⛔ `modules/ingress/variables.tf:8-17` declara `hostname` y `certificate_arn`, y
# `modules/ingress/main.tf:153-203` los usa para decidir si existen el certificado ACM,
# el listener HTTPS y la redirección :80 → :443 (los tres `count = … == "" ? 0 : 1`).
# `modules/compute/variables.tf:8-17` declara `image` y `engine_image`, y
# `modules/compute/main.tf:177-295` los usa igual para las task definitions y los
# services. **La raíz no pasaba NINGUNA de las cuatro**, así que no había forma de
# darles valor desde fuera: un apply completo creaba VPC, RDS, ECR, secretos y los dos
# balanceadores, y se quedaba SIN listener HTTPS y SIN una sola tarea corriendo.
# No era una decisión de diseño con su razón escrita: era un cableado que faltaba —
# la misma clase que los argumentos duplicados que `hcl-module-guard` existe para ver.
#
# ⚠ ESTA NOTA DECÍA «el default vacío se conserva a propósito», Y LA ORDEN 24 LA SUPERA.
# Corregido el 2026-08-28 en el mismo commit que la cambia, porque una nota que describe
# el comportamiento anterior se cita después como si fuera el actual.
#
# Lo que sigue siendo cierto: `image`, `engine_image` y `certificate_arn` **siguen vacías**
# por defecto, y con ellas vacías no hay task definition, ni service, ni certificado
# externo. Lo que ha cambiado: `hostname` e `ingest_hostname` **ya no están vacías** —
# traen los nombres de SANDBOX adjudicados—, así que un apply sin argumentos **sí PIDE un
# certificado ACM**. No crea listener ni espera a nadie: eso es la fase 2. La diferencia
# entre pedir y esperar es justo lo que `await_certificate_validation` gobierna.
#
# ⛔ DECÍA «BYTE A BYTE» Y ESA AFIRMACIÓN ERA MÍA Y ERA MÁS FUERTE DE LO SOSTENIBLE.
# Retirada el 2026-08-28 tras el contraste `sol max` (A-01): un plan guardado contiene la
# CONFIGURACIÓN entera y sus variables de entrada, así que una configuración con cuatro
# expresiones de módulo añadidas **no es** un plan byte-idéntico aunque proponga las mismas
# acciones. Lo que se puede defender es la equivalencia de instancias y acciones, y es eso
# lo que dice ahora la línea de arriba.
#
# ⛔ Y `nullable = false` NO ES ADORNO, es la otra mitad de A-01: `nullable` vale true por
# defecto, y un `null` EXPLÍCITO **anula el default** — no lo sustituye por "". Con estas
# expresiones `null != ""`, así que un llamador que pasara `image = null` habría elegido
# `count = 1` y roto el apply en un argumento obligatorio. El dispatch de hoy no puede
# hacerlo (sus entradas son cadenas), pero la invariante no debe depender de quién llama.
# Quién les da valor y en qué orden: `.github/workflows/aws-terraform.yml`
# (`workflow_dispatch` → `TF_VAR_*`) y el runbook de despliegue.

# ⛔ HOSTNAMES — CORREGIDOS EL 2026-09-01: todo el cloud vive bajo `cloud.olivares.ai`, para
# evitar los `-` y tener todo el cloud dentro de un mismo nombre. Los defaults de la zona de
# pruebas que había aquí quedan RANCIOS.
#
#   defaults de hoy   api.cloud.olivares.ai   ·  ingest.cloud.olivares.ai   (zona olivares.ai)
#
# Lo que decía antes, y se deja escrito porque explica de dónde venía el nombre de pruebas:
# el reparto anterior (2026-08-28) ponía el sandbox en una zona aparte y producción en
# `*.cloud.olivares.ai`, y por eso el default era el par de SANDBOX.
#
# ⚠ Y ESE REPARTO ES LO QUE LA CORRECCIÓN DESHACE, así que hay que decirlo en voz alta en vez
# de dejarlo implícito: **el default ya NO es «el nombre de pruebas»**. Hoy sólo existe UN
# estate —la clave del backend es `cloud/sandbox/terraform.tfstate` y el token de confirmación
# es `apply-sandbox-estate`—, así que un dispatch sin variables aplica ese estate bajo el
# nombre definitivo y **pide el certificado de ACM para él**. Si más adelante se quiere un
# sandbox con nombre propio, ese nombre lo elige dentro de la misma zona y sin guiones
# (p. ej. un `sandbox.cloud.olivares.ai`), y se pasa por dispatch como cualquier otro valor.
# No se inventa aquí: esta línea sólo deja de mentir sobre lo que hace el default.
variable "hostname" {
  type        = string
  description = "Public hostname for the control-plane ALB. Default is the SANDBOX name; production (api.cloud.olivares.ai) is passed explicitly and is not touched by the first apply."
  default     = "api.cloud.olivares.ai"
  nullable    = false
}

variable "ingest_hostname" {
  type        = string
  description = "Public hostname for the collector NLB. Default is the SANDBOX name. It configures no AWS resource — the NLB is TCP passthrough and needs no ACM certificate — and exists so the CNAME to create is derived here instead of typed twice."
  default     = "ingest.cloud.olivares.ai"
  nullable    = false
}

# ⛔ LA FASE, Y ES LO QUE HACE QUE EL PRIMER APPLY PUEDA TERMINAR.
# `false` (FASE 1): se PIDE el certificado y su registro de validación sale por los
# outputs; no se espera a nadie y no se crea el listener HTTPS. El apply termina limpio.
# `true` (FASE 2): se espera a que ACM valide — que sólo ocurre si el CNAME ya existe — y
# entonces aparecen el listener :443 y la redirección :80.
variable "await_certificate_validation" {
  type        = bool
  description = "FASE 2. Leave false for the first apply: the certificate is requested and its validation record published, and the apply finishes. Turn it true only AFTER the validation CNAME exists, or the apply will sit until its timeout."
  default     = false
  nullable    = false
}

variable "certificate_arn" {
  type        = string
  description = "Pre-existing and ALREADY VALIDATED ACM certificate ARN. Empty makes the ingress module issue its own from var.hostname by DNS validation, in two phases (see await_certificate_validation)."
  default     = ""
  nullable    = false
}

variable "image" {
  type        = string
  description = "Control-plane image BY DIGEST (repo@sha256:...). Empty keeps the ECS task definition and service at count 0. Produced by .github/workflows/aws-images.yml."
  default     = ""
  nullable    = false
}

variable "engine_image" {
  type        = string
  description = "Engine image BY DIGEST (repo@sha256:...). Empty keeps the engine task definition and service at count 0. Produced by .github/workflows/aws-images.yml."
  default     = ""
  nullable    = false
}

variable "roles_task_image" {
  type        = string
  description = "Image BY DIGEST (repo@sha256:...) with a Postgres client, for the one-shot task that provisions the control plane's database roles. Empty means no such task: its role, policy and task definition stay at count 0. Produced by .github/workflows/aws-images.yml, which builds three images: the control plane, the engine, and this client-only one."
  default     = ""
  nullable    = false
}

variable "cloud_cp_max_pool_connections" {
  type        = number
  description = "CLOUD_CP_MAX_POOL_CONNECTIONS for the control plane. Not a secret: the client connection slots ONE control-plane process may declare across its nine runtime pools. 36 is four per runtime capability, the binary's default, and the floor is one slot per capability. It bounds neither server sessions nor desired_count replicas, deployment surge, draining or rollback tasks, migrations or poolers: account for those per database target."
  default     = 36
  nullable    = false

  validation {
    condition     = var.cloud_cp_max_pool_connections >= 9 && var.cloud_cp_max_pool_connections <= 2147483647 && floor(var.cloud_cp_max_pool_connections) == var.cloud_cp_max_pool_connections
    error_message = "cloud_cp_max_pool_connections must be a whole number from 9 (one slot per runtime capability) to 2147483647."
  }
}
