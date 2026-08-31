# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# ⛔ NEVER APPLIED.
#
# Split ingress (ADR-0027): NLB TCP passthrough for collectors (binary
# terminates mTLS) + ALB HTTPS for the control plane.
#
# Source IP (design §2.2): preserve_client_ip.enabled = true on the NLB
# IP target group. Default for IP/TCP is OFF; leaving it off would make
# every collector appear as the NLB private address.

resource "aws_lb" "nlb" {
  name               = "${var.name}-nlb"
  load_balancer_type = "network"
  internal           = false
  subnets            = var.public_subnet_ids
  # desired_count defaults to 2 (HA). NLB cross-zone is OFF by default, so a
  # collector hitting the node in AZ-a would not reach the task in AZ-b.
  enable_cross_zone_load_balancing = true
  # RDS already has deletion_protection. LBs default OFF; a tofu destroy
  # of ingress would drop every collector's mTLS front door.
  enable_deletion_protection = true
  # Zonal shift for collectors. Default false keeps mTLS on a sick
  # AZ. Named true. Not the ALB pin (#1239). Unapplied.
  enable_zonal_shift         = true
  # DNS routing among AZs. AWS default any_availability_zone.
  # Named so a later affinity policy cannot land unnamed.
  # NLB + ipv4 only. Not zonal shift (#1240). Unapplied.
  dns_record_client_routing_policy = "any_availability_zone"
  tags                       = merge(var.tags, { Name = "${var.name}-nlb" })
}

resource "aws_lb_target_group" "collectors" {
  name        = "${var.name}-collectors"
  port        = 8444
  protocol    = "TCP"
  vpc_id      = var.vpc_id
  target_type = "ip"

  # ⛔ REQUIRED. Fargate = IP targets + TCP = preserve OFF by default.
  preserve_client_ip = true
  # C04-03: PROXY v2 on the collector NLB. The binary terminates mTLS
  # and must see the real peer; this estate is unapplied until CLOUD-ACC.
  proxy_protocol_v2 = true

  health_check {
    protocol = "TCP"
    port     = "8444"
  }

  tags = merge(var.tags, { Name = "${var.name}-collectors" })
}

resource "aws_lb_listener" "collectors" {
  load_balancer_arn = aws_lb.nlb.arn
  port              = 8444
  protocol          = "TCP"
  # CLOUD-DISENO §0: a TLS listener is fixed at 350 s. TCP is how we own
  # the idle timeout (60–6000). Default NLB TCP is also 350; leaving it
  # unset would be the TLS ceiling with extra steps. Collectors keep
  # mTLS streams open; 6000 is the published maximum.
  tcp_idle_timeout_seconds = 6000

  default_action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.collectors.arn
  }
}

resource "aws_lb" "alb" {
  name                       = "${var.name}-alb"
  load_balancer_type         = "application"
  internal                   = false
  subnets                    = var.public_subnet_ids
  security_groups            = [var.alb_security_group_id]
  enable_deletion_protection = true
  # Default OFF. Leaving it off forwards desynced headers to the engine.
  drop_invalid_header_fields = true
  # HTTP idle, not NLB TCP 6000. Unnamed default can move under us.
  # Unapplied: CLOUD-ACC does not exist.
  idle_timeout             = 60
  desync_mitigation_mode   = "defensive"
  # HTTP/2 is the AWS default. Named so a later false cannot land
  # as an unnamed flip. Unapplied: CLOUD-ACC does not exist.
  enable_http2               = true
  # WAF fail-open defaults to false. Unnamed, a later true would
  # pass traffic when WAF is unreachable. Deny-closed stays named.
  # Unapplied: CLOUD-ACC does not exist.
  enable_waf_fail_open       = false
  # Default false rewrites Host to the target IP. Fargate IP
  # targets would then see a private address, not the public
  # hostname. Named true so a later false cannot land unnamed.
  # Unapplied: CLOUD-ACC does not exist.
  preserve_host_header       = true
  # Zonal shift. Default false keeps sending to a sick AZ.
  # Named true so a later false cannot disable the shift unnamed.
  # The ARC practice itself is apply-time. Unapplied.
  enable_zonal_shift         = true
  # HTTP client keep-alive. AWS default 3600 s (range 60-604800).
  # Named so a later 60 cannot land unnamed. Not NLB TCP idle
  # (6000) and not ALB idle_timeout (#1214). Unapplied.
  client_keep_alive          = 3600
  # append is the AWS default. preserve would trust client-supplied
  # XFF on a public ALB. remove would hide the client from the
  # target. Named so a later remove cannot land unnamed. Unapplied.
  xff_header_processing_mode = "append"
  # Default false omits the client port from X-Forwarded-For.
  # Named true so a later false cannot drop the port unnamed.
  # Not overlay ALC-04 (trusted-proxy resolve). Unapplied.
  enable_xff_client_port     = true
  tags                       = merge(var.tags, { Name = "${var.name}-alb" })
  # Empty bucket keeps logs off. Unapplied: CLOUD-ACC does not exist.
  access_logs {
    bucket  = var.access_logs_bucket
    prefix  = "alb"
    enabled = var.access_logs_bucket != ""
  }
  # Connection logs are TLS handshakes, not HTTP access lines.
  # #1225 writes access logs into the plane bucket. This block
  # uses a sibling bucket. Prefix must not contain AWSLogs.
  # Unapplied: CLOUD-ACC does not exist.
  connection_logs {
    bucket  = var.connection_logs_bucket
    prefix  = "alb-conn"
    enabled = var.connection_logs_bucket != ""
  }
  # Adds x-amzn-tls-version and x-amzn-tls-cipher-suite on the
  # request to the target. Default false omits them. Named true
  # so a later false cannot drop the negotiated TLS unnamed.
  # Not HSTS (browser) and not Server (response). Unapplied.
  enable_tls_version_and_cipher_suite_headers = true
}

resource "aws_lb_target_group" "http" {
  name        = "${var.name}-http"
  port        = 8443
  protocol    = "HTTPS"
  vpc_id      = var.vpc_id
  target_type = "ip"

  health_check {
    protocol = "HTTPS"
    path     = "/readyz"
    matcher  = "200"
  }

  tags = merge(var.tags, { Name = "${var.name}-http" })
}

# CLOUD-DISENO §6 ingress names ACM. Empty hostname keeps count 0.
# Unapplied: CLOUD-ACC does not exist.
resource "aws_acm_certificate" "alb" {
  # ⛔ F-3 (MEDIO, contraste `sol max` 2026-08-28): decía `var.hostname == "" ? 0 : 1`, y con
  # el hostname por defecto eso pedía un certificado propio **incluso cuando se pasa uno
  # externo** — un certificado de más que nadie sirve, y unos `acm_validation_records` que
  # describen el que NO se usa. Si el operador trae su certificado, este módulo no pide otro.
  count             = (var.hostname == "" || var.certificate_arn != "") ? 0 : 1
  domain_name       = var.hostname
  validation_method = "DNS"

  lifecycle {
    create_before_destroy = true
  }
}

# ⛔ LA ESPERA DE VALIDACIÓN ES UN RECURSO APARTE, Y ESO ES LO QUE PERMITE PARAR LIMPIO.
# `aws_acm_certificate_validation` no crea nada en AWS: bloquea hasta que ACM ve el
# registro DNS. Con `await_certificate_validation = false` no existe (count 0), así que la
# FASE 1 termina con el certificado PEDIDO y su registro publicado en los outputs, sin
# esperar a nadie. Con true, espera — y si el CNAME no está, el apply se queda en su
# timeout, que es un fallo honesto y ruidoso, no un verde.
resource "aws_acm_certificate_validation" "alb" {
  count           = (var.hostname != "" && var.await_certificate_validation) ? 1 : 0
  certificate_arn = aws_acm_certificate.alb[0].arn
  validation_record_fqdns = [
    for o in aws_acm_certificate.alb[0].domain_validation_options : o.resource_record_name
  ]
}

locals {
  # ⛔ LA CONDICIÓN QUE DECIDE SI HAY LISTENER, EXPRESADA EN LO QUE SE SABE AL PLANIFICAR.
  # `local.cert_arn` vale para el ARN que se adjunta, pero NO sirve para un `count`: en la
  # fase 2 es un atributo de `aws_acm_certificate_validation`, o sea **desconocido hasta el
  # apply**, y OpenTofu rechaza el plan entero con «Invalid count argument». Es exactamente
  # el defecto que tumbó el apply 1 en `modules/compute/main.tf:164` — la misma clase,
  # esperando 75 minutos más adelante en la fase 2.
  #
  # Esto es equivalente a «`cert_arn` no está vacío» y se evalúa sin aplicar nada: o el
  # certificado viene de fuera, o lo emitimos nosotros Y estamos esperando su validación
  # (que es justo cuando el recurso de validación existe).
  have_cert = var.certificate_arn != "" || (var.hostname != "" && var.await_certificate_validation)

  # ⛔ EL ARN QUE SE PUEDE ADJUNTAR NO ES EL DEL CERTIFICADO: ES EL DE SU VALIDACIÓN.
  # Esta línea decía `try(aws_acm_certificate.alb[0].arn, "")`, y ese ARN existe desde que
  # el certificado se PIDE, en `PENDING_VALIDATION` — adjuntarlo a un listener HTTPS falla.
  # Con un hostname puesto y sin su CNAME creado, el apply se rompía a mitad. Ahora el
  # listener sólo aparece cuando la validación ha terminado de verdad.
  #
  # `var.certificate_arn` se sigue honrando tal cual: un certificado que se pasa desde
  # fuera se da por validado por quien lo pasa, y ésa es su razón de ser.
  cert_arn = var.certificate_arn != "" ? var.certificate_arn : try(aws_acm_certificate_validation.alb[0].certificate_arn, "")
}

resource "aws_lb_listener" "https" {
  count             = local.have_cert ? 1 : 0
  load_balancer_arn = aws_lb.alb.arn
  port              = 443
  protocol          = "HTTPS"
  ssl_policy        = "ELBSecurityPolicy-TLS13-1-2-2021-06"
  certificate_arn   = local.cert_arn
  # Default true sends Server: awselb/2.0. Named false so a later
  # true cannot advertise the balancer unnamed. Lives on the
  # LISTENER (aws_lb has no routing_* args). Unapplied.
  routing_http_response_server_enabled = false
  # HSTS max-age only. Sibling hostnames and the public list are
  # out of this pin. Lives on the LISTENER. Unapplied.
  routing_http_response_strict_transport_security_header_value = "max-age=31536000"

  default_action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.http.arn
  }

  # ⛔ F-1 (ALTO, contraste `sol max` 2026-08-28). El default de fase es `false`, y eso está
  # bien para el PRIMER apply — pero después de la fase 2 un dispatch que se limite a
  # aceptar los defaults reduce el waiter a cero, vacía `local.cert_arn` y **destruye este
  # listener**: el frontal HTTPS desaparece por un olvido, no por una decisión. El caso
  # realista es el más cotidiano de todos — alguien re-aplica sólo para cambiar un digest.
  #
  # `prevent_destroy` convierte ese olvido en un ERROR de plan, antes de tocar nada.
  # Residual DECLARADO: retirar el frontal a propósito exige editar esta cláusula, y eso es
  # exactamente lo que se quiere — que quitar el HTTPS de un servicio en pie sea un acto
  # deliberado y visible en un diff, no el efecto lateral de un default.
  lifecycle {
    prevent_destroy = true
  }
}

# Same count as HTTPS: no cert, no listener. Unapplied.
resource "aws_lb_listener" "http_redirect" {
  count             = local.have_cert ? 1 : 0
  load_balancer_arn = aws_lb.alb.arn
  port              = 80
  protocol          = "HTTP"

  default_action {
    type = "redirect"
    redirect {
      port        = "443"
      protocol    = "HTTPS"
      status_code = "HTTP_301"
    }
  }

  # Mismo motivo y misma cláusula que el listener :443 de arriba (F-1). Sin ella, el olvido
  # que quita el HTTPS deja además el :80 sirviendo sin redirección.
  lifecycle {
    prevent_destroy = true
  }
}

resource "aws_wafv2_web_acl" "alb" {
  name  = "${var.name}-alb"
  scope = "REGIONAL"

  default_action {
    allow {}
  }

  # An ACL with no rules is an allow-all. Design §1 promised WAF on the ALB.
  rule {
    name     = "AWS-AWSManagedRulesCommonRuleSet"
    priority = 1

    override_action {
      none {}
    }

    statement {
      managed_rule_group_statement {
        name        = "AWSManagedRulesCommonRuleSet"
        vendor_name = "AWS"
      }
    }

    visibility_config {
      cloudwatch_metrics_enabled = true
      metric_name                = "${var.name}-alb-common"
      sampled_requests_enabled   = true
    }
  }

  visibility_config {
    cloudwatch_metrics_enabled = true
    metric_name                = "${var.name}-alb"
    sampled_requests_enabled   = true
  }

  tags = var.tags
}

resource "aws_wafv2_web_acl_association" "alb" {
  resource_arn = aws_lb.alb.arn
  web_acl_arn  = aws_wafv2_web_acl.alb.arn
}

# Sampled requests on the ACL are a 1 % cap, not a log trail.
# AWS requires the group name to start with aws-waf-logs-.
# WAF puts the AWSWAF-LOGS resource policy on the group; a
# second generic policy races. Unapplied: CLOUD-ACC does not exist.
resource "aws_cloudwatch_log_group" "waf" {
  name              = "aws-waf-logs-${var.name}"
  retention_in_days = 30
  tags              = merge(var.tags, { Name = "aws-waf-logs-${var.name}" })
}

resource "aws_wafv2_web_acl_logging_configuration" "alb" {
  log_destination_configs = [aws_cloudwatch_log_group.waf.arn]
  resource_arn            = aws_wafv2_web_acl.alb.arn

  redacted_fields {
    single_header {
      name = "authorization"
    }
  }
}
