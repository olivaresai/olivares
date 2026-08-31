---
title: "Superviser avec Prometheus (SLO, métriques, alertes)"
description: >-
  Scrapez le /metrics du moteur, adoptez les cibles SLO publiées et chargez les
  règles d'alerte de burn-rate livrées — les mêmes SLI sur lesquels s'appuient les
  propres runbooks du produit, avec les chiffres single-writer énoncés honnêtement.
---

Le moteur expose trois endpoints opérationnels sur l'écouteur HTTP, tous
adaptés au probing :

| Endpoint | Auth | Objet |
|---|---|---|
| `/livez` | aucune | liveness du processus — **aucune vérification de dépendance**, de sorte qu'une panne du store ne provoque jamais de boucle de redémarrage |
| `/readyz` | aucune | readiness — ping du store (et leadership HA) : `200 {"status":"ok","store":"up","leader":true,…}`, `503 {"status":"unavailable","store":"down"}`, ou `503 {"status":"standby",…,"leader":false}` sur un standby HA |
| `/metrics` | aucune | exposition Prometheus. Délibérément non authentifié : il porte des séries opérationnelles, jamais de données de tenant |

L'accessibilité de `/readyz` **est** le SLI de disponibilité.

## L'ensemble de métriques qui compte

Toutes les séries sont enregistrées par le moteur (vérifié contre le code actuel) ;
les plus déterminantes :

| Série | Ce qu'elle vous indique |
|---|---|
| `olivares_store_up` | le store répond à un ping — la première chose que vérifie chaque runbook |
| `olivares_http_requests_total{code}` | SLI de succès des requêtes (`code!~"5.."`) |
| `olivares_http_request_duration_seconds` | latence de l'API (cible p99 ci-dessous) |
| `olivares_ingest_duration_seconds` | **le SLI de backpressure** — la p99 d'ingestion augmente quand un abonné sature |
| `olivares_ingest_observations_total` / `olivares_ingest_rejected_total` | débit et rejets d'ingestion |
| `olivares_eventbus_queue_depth` / `_queue_capacity` (par abonné) | quel module est le consommateur lent |
| `olivares_eventbus_publish_blocked_total` | événements de backpressure (le bus bloque ; il ne perd rien) |
| `olivares_eventbus_bridge_*` | santé du pont NATS quand le bus distribué est activé — `_connected`, `_pending_messages`, `_dropped_total` (la livraison inter-nœuds est at-most-once ; les pertes sont comptées, jamais silencieuses) |
| `olivares_audit_checkpoint_age_seconds` | fraîcheur de la preuve d'altération — alertez quand elle dépasse 2× l'intervalle de checkpoint |
| `olivares_auth_login_attempts_total{outcome}` | succès / échec / verrouillage de connexion |
| `olivares_http_ratelimit_decisions_total{decision}` | pression du rate-limit |
| `olivares_grpc_requests_total` / `olivares_grpc_request_duration_seconds` | le plan d'ingestion collector→core |

## Cibles SLO (publiées, honnêtes)

Les cibles single-node — ce que la topologie par défaut prend réellement en charge — et
le palier HA :

| SLI | Single node | Palier HA (Postgres) |
|---|---|---|
| Disponibilité (`/readyz`) | **99,5 % / 28 j** | 99,9 % / 28 j |
| Succès des requêtes (non-5xx) | **99,9 %** | 99,95 % |
| Latence API p99 | **< 300 ms** | < 200 ms |
| Latence d'ingestion p99 | **< 250 ms** | < 150 ms |
| Succès d'ingestion | **99,9 %** | 99,95 % |

L'honnêteté dans les chiffres : un writer unique sur un seul nœud ne peut pas promettre trois
neuf de disponibilité, donc la documentation ne le fait pas — 99,5 % (≈ 3 h 39 min de budget par
28 jours) est la vérité single-node, et le palier 99,9 % se gagne par la
[topologie HA](/fr/tutorials/getting-started/kubernetes/#3-ha-active-passive),
non par l'optimisme.

## Charger les règles d'alerte livrées

`deploy/monitoring/olivares-slo.rules.yaml` livre 14 alertes prêtes pour votre
Prometheus : des alertes de burn-rate multi-fenêtres sur le budget de succès des requêtes
(rapide 14,4× page / moyenne 6× page / lente 1× ticket), des déclenchements absolus de latence et de
disponibilité (`OlivaresIngestP99High`, `OlivaresApiLatencyP99High`,
`OlivaresStoreDown`, `OlivaresControlPlaneUnscrapeable`), de saturation
(`OlivaresEventBusSaturated` à >90 % de file pendant 10 min), de santé du pont
(`OlivaresEventBusBridgeDropping`, `OlivaresEventBusBridgeDisconnected`) et
de fraîcheur du ledger (`OlivaresAuditCheckpointStale` à un âge > 2 h).

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

Sur Kubernetes, l'option `ServiceMonitor` du chart câble le scrape pour
l'opérateur Prometheus. Une configuration de page de statut Gatus pour la sonde externe `/readyz`
est livrée aux côtés des règles (`deploy/monitoring/status-page.gatus.yaml`).

## Quand une alerte se déclenche

Le diagnostic symptôme par symptôme — store down, p99 d'ingestion élevée, bus saturé,
checkpoint obsolète — se trouve sur la [page de dépannage](/fr/how-to/troubleshooting/),
distillée à partir des mêmes runbooks que référencent les annotations des alertes.
