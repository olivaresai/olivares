# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# ⛔ NEVER APPLIED. Lo que la FASE 1 tiene que decir para que la FASE 2 sea posible.
#
# Este estate NO tiene proveedor de Cloudflare ni token de DNS, y es deliberado: los tres
# registros los crea a mano. La consecuencia es que el apply está obligado a publicar
# **valores literales**, no pistas — si un output falta, alguien deduce, y una deducción
# sobre un CNAME de validación se descubre media hora después, cuando ACM no valida.
#
# Los tres registros, y el momento exacto en que se crea cada uno:
#
#   1 · validación de ACM   — DESPUÉS de la fase 1, con `acm_validation_records`.
#                             Sin él, ACM nunca valida y la fase 2 se queda en su timeout.
#   2 · api    -> ALB       — puede crearse ya en la fase 1, con `alb_dns_name`.
#   3 · ingest -> NLB       — puede crearse ya en la fase 1, con `nlb_dns_name`.
#
# ⛔ LOS TRES VAN **DNS ONLY**, sin proxy naranja, y no es una preferencia:
#   · el de validación, porque ACM lee el valor del registro y un proxy lo reescribe;
#   · el de `api`, porque el ALB termina TLS con SU certificado — un proxy delante
#     presentaría otro y el cliente vería un certificado que no es el que validamos;
#   · el de `ingest`, porque el listener del NLB es **TCP crudo** y el binario termina
#     mTLS: un proxy HTTP delante no puede pasar eso, rompe el mTLS entero.

output "acm_validation_records" {
  description = "FASE 1 → los registros DNS que ACM necesita para validar. Vacío si no hay hostname. Cada entrada trae name/type/value, literales, listos para pegar. DNS only."
  value       = module.ingress.acm_validation_records
}

output "alb_dns_name" {
  description = "Destino del CNAME de `api`. DNS only: el ALB termina TLS con su propio certificado."
  value       = module.ingress.alb_dns_name
}

output "nlb_dns_name" {
  description = "Destino del CNAME de `ingest`. DNS only: el listener es TCP crudo y el binario termina mTLS."
  value       = module.ingress.nlb_dns_name
}

# Un solo sitio del que leer los TRES, para que nadie tenga que cruzar tres outputs a mano
# y equivocarse en uno. El de validación viene con su valor; los dos de servicio, con el
# destino que les toca.
output "dns_records_to_create" {
  description = "Los tres CNAME que hay que crear a mano, en un solo sitio. Todos DNS only (sin proxy)."
  value = concat(
    [
      for r in module.ingress.acm_validation_records : {
        purpose = "acm-validation"
        name    = r.name
        type    = r.type
        value   = r.value
        when    = "after phase 1, before setting await_certificate_validation = true"
      }
    ],
    var.hostname == "" ? [] : [{
      purpose = "api"
      name    = var.hostname
      type    = "CNAME"
      value   = module.ingress.alb_dns_name
      when    = "any time after phase 1"
    }],
    var.ingest_hostname == "" ? [] : [{
      purpose = "ingest"
      name    = var.ingest_hostname
      type    = "CNAME"
      value   = module.ingress.nlb_dns_name
      when    = "any time after phase 1"
    }],
  )
}

# El estado de la fase, dicho por el propio apply en vez de recordado por quien lo lanzó.
output "phase" {
  description = "Which phase this apply ran. Reading it back is how you tell a certificate that was merely requested from one that is validated and serving."
  # ⛔ F-3: esta expresión no miraba `certificate_arn`, así que con un certificado externo
  # decía «fase 1, sin listener» mientras el listener SÍ existía. Un output que describe
  # otra cosa que la que hay es peor que no tenerlo.
  value = var.certificate_arn != "" ? "external certificate: listeners served from the ARN passed in, no ACM request and no validation wait" : (
    var.hostname == "" ? "no hostname: no certificate, no HTTPS listener" : (
      var.await_certificate_validation
      ? "phase 2: certificate validated, HTTPS listener and :80 redirect created"
      : "phase 1: certificate REQUESTED and pending DNS validation, no HTTPS listener yet"
    )
  )
}
