# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# ⛔ NEVER APPLIED. Root wiring of the six modules. `terraform apply`
# is not authorised. The account does not exist in this lote.

data "aws_availability_zones" "available" {
  state = "available"
}

module "network" {
  source        = "./modules/network"
  name          = "olivares-${var.environment}"
  vpc_cidr      = var.vpc_cidr
  azs           = slice(data.aws_availability_zones.available.names, 0, 2)
  enable_ha_nat = var.enable_ha_nat
}

module "data" {
  source                 = "./modules/data"
  name                   = "olivares-${var.environment}"
  private_subnet_ids     = module.network.private_subnet_ids
  rds_security_group_id  = module.network.rds_security_group_id
}

module "compute" {
  source                 = "./modules/compute"
  name                   = "olivares-${var.environment}"
  private_subnet_ids     = module.network.private_subnet_ids
  task_security_group_id = module.network.task_security_group_id
  desired_count          = var.desired_count
  # ⛔ El booleano va EXPLÍCITO aunque su default sea true: quien lea este bloque tiene que
  # ver que la decisión se toma aquí, en tiempo de plan, y no dentro del módulo a partir de
  # un ARN que todavía no existe. `modules/secrets` crea la ranura `dsn` siempre
  # (`for_each = toset(local.slots)`, sin condición), así que aquí es `true` fijo.
  dsn_secret_enabled     = true
  dsn_secret_arn         = module.secrets.secret_arns["dsn"]
  # Las dos ranuras que llevan lo del plano de control, y la clave que las cifra. El
  # booleano decide en tiempo de plan; los ARNs son valores, como en las tres de arriba.
  cp_secrets_enabled      = true
  cp_databases_secret_arn = module.secrets.secret_arns["cloud-cp-databases"]
  cp_runtime_secret_arn   = module.secrets.secret_arns["cloud-cp-runtime"]
  secrets_kms_key_arn     = module.secrets.secrets_kms_key_arn
  # No secretos, y por eso viajan en claro y se revisan en el diff. El endpoint de OTel y el
  # aviso al operador quedan VACÍOS a propósito: el plano de control los admite ausentes, y
  # rellenarlos con una suposición sería peor que dejarlos sin decidir.
  engine_base_url         = "https://olivares-${var.environment}-engine.internal:8443"
  otel_endpoint           = ""
  operator_alert_to       = ""
  # Ídem: los dos target groups los crea `modules/ingress` sin condición, así que el
  # llamador sabe AL PLANIFICAR que va a registrarse en ellos; lo que no sabe es su ARN.
  attach_alb_target_group = true
  attach_nlb_target_group = true
  alb_target_group_arn   = module.ingress.http_target_group_arn
  nlb_target_group_arn   = module.ingress.collector_target_group_arn
  # Empty until aws-images.yml has pushed a signed digest to ECR. Empty keeps the
  # task definitions and services at count 0, which is the state before this line
  # existed — the module already defaulted both to "".
  image                  = var.image
  engine_image           = var.engine_image
}

module "ingress" {
  source                 = "./modules/ingress"
  name                   = "olivares-${var.environment}"
  vpc_id                 = module.network.vpc_id
  public_subnet_ids      = module.network.public_subnet_ids
  alb_security_group_id  = module.network.alb_security_group_id
  access_logs_bucket     = module.data.plane_bucket_id
  connection_logs_bucket = module.data.alb_conn_bucket_id
  # Empty until the public name is registered in Cloudflare: ACM validates by DNS,
  # so a hostname without its record leaves the certificate PENDING_VALIDATION and
  # the apply waiting. Empty keeps aws_acm_certificate, the :443 listener and the
  # :80 redirect at count 0 — the state before this line existed.
  hostname               = var.hostname
  ingest_hostname        = var.ingest_hostname
  certificate_arn        = var.certificate_arn
  # FASE 1 = false: pide el certificado y publica su registro; no espera y no crea el
  # listener. FASE 2 = true, sólo cuando el CNAME de validación ya exista.
  await_certificate_validation = var.await_certificate_validation
}

module "secrets" {
  source = "./modules/secrets"
  name   = "olivares-${var.environment}"
}

module "observability" {
  source       = "./modules/observability"
  name         = "olivares-${var.environment}"
  cluster_name = "olivares-${var.environment}"
  vpc_id       = module.network.vpc_id
}
