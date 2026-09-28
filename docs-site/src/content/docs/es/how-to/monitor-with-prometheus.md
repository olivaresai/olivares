---
title: "Monitorizar con Prometheus (SLO, métricas, alertas)"
description: >-
  Recolecta el /metrics del motor, adopta los objetivos de SLO publicados y carga
  las reglas de alerta por burn-rate que vienen incluidas —los mismos SLI en los
  que se apoyan los propios runbooks del producto, con las cifras de escritor único
  expuestas con honestidad.
---

El motor expone tres endpoints operativos en el listener HTTP, todos
aptos para sondas:

| Endpoint | Auth | Propósito |
|---|---|---|
| `/livez` | ninguna | liveness del proceso — **sin comprobaciones de dependencias**, de modo que una caída del store nunca provoca un bucle de reinicios |
| `/readyz` | ninguna | readiness — ping al store (y liderazgo HA): `200 {"status":"ok","store":"up","leader":true,…}`, `503 {"status":"unavailable","store":"down"}`, o `503 {"status":"standby",…,"leader":false}` en un standby de HA |
| `/metrics` | ninguna | exposición Prometheus. Deliberadamente sin autenticar: transporta series operativas, nunca datos de tenant |

La accesibilidad de `/readyz` **es** el SLI de disponibilidad.

## El conjunto de métricas que importa

Todas las series las registra el motor (verificado contra el código actual);
las que soportan el peso:

| Serie | Qué te dice |
|---|---|
| `olivares_store_up` | el store responde a un ping — lo primero que comprueba cada runbook |
| `olivares_http_requests_total{code}` | SLI de éxito de peticiones (`code!~"5.."`) |
| `olivares_http_request_duration_seconds` | latencia de la API (objetivo p99 más abajo) |
| `olivares_ingest_duration_seconds` | **el SLI de backpressure** — el p99 de ingesta sube cuando un suscriptor se satura |
| `olivares_ingest_observations_total` / `olivares_ingest_rejected_total` | throughput de ingesta y rechazos |
| `olivares_eventbus_queue_depth` / `_queue_capacity` (por suscriptor) | qué módulo es el consumidor lento |
| `olivares_eventbus_publish_blocked_total` | eventos de backpressure (el bus bloquea; no descarta) |
| `olivares_eventbus_bridge_*` | salud del puente NATS cuando el bus distribuido está activo — `_connected`, `_pending_messages`, `_dropped_total` (la entrega entre nodos es at-most-once; los descartes se cuentan, nunca son silenciosos) |
| `olivares_audit_checkpoint_age_seconds` | frescura de la evidencia de manipulación — alerta cuando supera 2× el intervalo de checkpoint |
| `olivares_auth_login_attempts_total{outcome}` | login con éxito / fallo / bloqueo / abandono |
| `olivares_http_ratelimit_decisions_total{decision}` | presión de rate-limit |
| `olivares_grpc_requests_total` / `olivares_grpc_request_duration_seconds` | el plano de ingesta colector→core |

## Objetivos de SLO (publicados, honestos)

Objetivos operativos para despliegues de nodo único y HA, no resultados medidos
de una topología ni compromisos con el cliente. Cualifica el despliegue y la carga
concretos antes de afirmar que se cumplen; `deploy/support-matrix.md` registra su estado:

| SLI | Nodo único | Nivel HA (Postgres) |
|---|---|---|
| Disponibilidad (`/readyz`) | **99,5 % / 28d** | 99,9 % / 28d |
| Éxito de petición (no-5xx) | **99,9 %** | 99,95 % |
| Latencia p99 de la API | **< 300 ms** | < 200 ms |
| Latencia p99 de ingesta | **< 250 ms** | < 150 ms |
| Éxito de ingesta | **99,9 %** | 99,95 % |

En 28 días, un objetivo de disponibilidad del 99,5 % permite **201,6 minutos
(3h 21m 36s)** de indisponibilidad; el 99,9 % permite **40,32 minutos**.
Desplegar la [topología HA](/es/tutorials/getting-started/kubernetes/#3-ha-activo-pasivo) no logra por sí solo ninguno de esos porcentajes.
Mide la disponibilidad durante la ventana indicada y conserva la evidencia de
fallos y recuperación del despliegue. Estos objetivos no son una promesa
contractual de disponibilidad.

## Cargar las reglas de alerta incluidas

`deploy/monitoring/olivares-slo.rules.yaml` incluye 14 alertas listas para tu
Prometheus: alertas de burn-rate multi-ventana sobre el presupuesto de éxito de petición
(rápida 14,4× página / media 6× página / lenta 1× ticket), disparos absolutos de latencia y
disponibilidad (`OlivaresIngestP99High`, `OlivaresApiLatencyP99High`,
`OlivaresStoreDown`, `OlivaresControlPlaneUnscrapeable`), saturación
(`OlivaresEventBusSaturated` a >90 % de cola durante 10m), salud del puente
(`OlivaresEventBusBridgeDropping`, `OlivaresEventBusBridgeDisconnected`) y
frescura del ledger (`OlivaresAuditCheckpointStale` con edad > 2h).

La receta de burn-rate usa un periodo de referencia de 30 días; los objetivos de
disponibilidad anteriores usan 28 días. Distingue las tasas y ventanas de alerta
de ese periodo: 14,4× durante 1h, 6× durante 6h y 1× durante 3 días consumen
aproximadamente el 2,14 %, el 5,36 % y el 10,71 % de un presupuesto de 28 días,
respectivamente. Los valores de las alertas incluidas no cambian.

```yaml
# prometheus.yml
rule_files:
  - olivares-slo.rules.yaml
scrape_configs:
  - job_name: olivares
    scheme: https
    tls_config: { insecure_skip_verify: true }   # or pin the real cert
    static_configs: [{ targets: ["olivares.internal:8443"] }]
```

En Kubernetes, la opción `ServiceMonitor` del chart cablea la recolección para el
operador de Prometheus. Junto a las reglas se incluye una configuración de página de estado Gatus
para la sonda externa de `/readyz` (`deploy/monitoring/status-page.gatus.yaml`).

## Cuando salta una alerta

El diagnóstico síntoma a síntoma —store caído, p99 de ingesta alto, bus saturado,
checkpoint obsoleto— está en la [página de resolución de problemas](/es/how-to/troubleshooting/),
destilada de los mismos runbooks que referencian las anotaciones de las alertas.
