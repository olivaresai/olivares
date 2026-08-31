# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only

output "nlb_arn" {
  value = aws_lb.nlb.arn
}

output "alb_arn" {
  value = aws_lb.alb.arn
}

output "collector_target_group_arn" {
  value = aws_lb_target_group.collectors.arn
}

output "http_target_group_arn" {
  value = aws_lb_target_group.http.arn
}

# ── Lo que necesita para crear los tres CNAME a mano ────────────────────
# No hay proveedor de Cloudflare ni token de DNS en este estate, y es deliberado: los
# registros los crea él. Estos outputs existen para que no tenga que deducir ni un valor.

output "alb_dns_name" {
  description = "DNS name of the control-plane ALB. Target of the api CNAME (DNS only, no proxy: the ALB terminates TLS with its own certificate)."
  value       = aws_lb.alb.dns_name
}

output "nlb_dns_name" {
  description = "DNS name of the collector NLB. Target of the ingest CNAME (DNS only: the listener is raw TCP and the binary terminates mTLS, so a proxy in front would break it)."
  value       = aws_lb.nlb.dns_name
}

output "acm_validation_records" {
  description = "The DNS records ACM needs to validate the certificate. Empty when no hostname is set. Each entry is name/type/value, ready to paste."
  value = [
    for o in try(aws_acm_certificate.alb[0].domain_validation_options, []) : {
      name  = o.resource_record_name
      type  = o.resource_record_type
      value = o.resource_record_value
    }
  ]
}
