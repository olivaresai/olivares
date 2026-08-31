---
title: "Recette : budgets & garde-fous FinOps"
description: >-
  Posez une limite ferme en dollars sur la dépense IA — par modèle, équipe,
  workspace ou une seule identité : alertez aux seuils, puis throttle ou bloquez
  au plafond. Plus le coût par résultat pour donner un dénominateur à la
  dépense.
sidebar:
  order: 2
---

**Objectif :** « les agents de cette équipe arrêtent de dépenser à 500 $/mois »
— déclaré une fois, appliqué en direct, avec des seuils d'alerte en chemin vers
le haut.

L'application des budgets est l'une des actuations qui est **active dans le
binaire par défaut** : un budget contraignant à son plafond refuse la dépense
sans provisioning supplémentaire ([le catalogue des modules](/fr/reference/modules/overview/)
le marque `v1 | v1`).

## Créer un budget

```bash
curl -ks -X POST "$BASE/v1/m/finops/budgets" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  -H 'Content-Type: application/json' \
  -d '{
    "dimension": "team",
    "key": "payments",
    "limit_micro_usd": 500000000,
    "period": "monthly",
    "thresholds": [0.5, 0.8, 1.0],
    "action": "block"
  }'
```

- **L'argent est en micro-USD** (`limit_micro_usd: 500000000` = 500 $), de
  sorte qu'il n'y a aucune ambiguïté de flottant dans le contrat.
- **`dimension` + `key`** délimitent le budget. Les dimensions délimitables
  incluent `global`, `model`, `provider`, `agent`, `session`, `team`,
  `project`, `workspace`, `api_key`, `actor`, `service_tier`,
  `context_window`, `inference_geo`, `gateway` et `identity`.
- **`action`** est le mode d'application :

| `action` | Au plafond |
|---|---|
| `alert` (défaut) | showback uniquement — les alertes se déclenchent, rien n'est refusé |
| `throttle` | le seam d'actuation ralentit la nouvelle dépense |
| `block` | le seam d'actuation refuse la nouvelle dépense |

## Budgéter une seule identité

`dimension: "identity"` délimite sur l'**external id d'une identité de roster
ferme** — l'identité de charge de travail ou d'agent que vos
[sources d'identité](/fr/how-to/connectors/sso-scim-identity/) ont enregistrée :

```json
{ "dimension": "identity", "key": "spiffe://corp/agent/billing-reconciler",
  "limit_micro_usd": 50000000, "period": "monthly", "action": "throttle" }
```

L'identité est résolue à l'ingestion du coût à partir de la liaison d'agent, de
la clé d'API ou de l'acteur de l'échantillon — de sorte que le budget suit
l'identité à travers les surfaces, et non une seule clé d'API.

## La voir à l'œuvre

```bash
# Live consumption vs limit, with run-rate projection:
curl -ks "$BASE/v1/m/finops/budgets/$BUDGET_ID/status" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT"

# Threshold crossings (your 50% / 80% / 100% alerts):
curl -ks "$BASE/v1/m/finops/alerts" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT"
```

Au plafond, le check d'un budget contraignant retourne `allowed: false` avec
l'action (`throttle` ou `block`) et le budget qui s'est déclenché — le refus
nomme sa raison. Les alertes circulent aussi sur le flux de notifications, de
sorte qu'une [destination](/fr/how-to/forward-audit-to-splunk/) Slack ou PagerDuty
entend le franchissement des 80 % avant le refus à 100 %.

Dans la console, **Cost & FinOps** affiche la dépense par dimension avec le
statut du budget en ligne :

<img class="light:sl-hidden" src="/console/finops-dark.png" alt="La vue Cost & FinOps avec les tendances de dépense et la posture des budgets." />
<img class="dark:sl-hidden" src="/console/finops-light.png" alt="La vue Cost & FinOps avec les tendances de dépense et la posture des budgets." />

## Donner un dénominateur à la dépense : les résultats

Le coût par résultat est ce qui fait d'un budget une conversation métier.
Rapportez les résultats (un ticket résolu, une PR mergée, un dossier clos) et
lisez les panneaux de valeur :

```bash
curl -ks -X POST "$BASE/v1/m/finops/outcomes" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  -d '{"kind":"ticket.resolved","subject_ref":"agent:support-triage","count":1}'

curl -ks "$BASE/v1/m/finops/value" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT"
```

Le résumé de valeur inclut le **risque d'annulation** — la consommation sans
résultats — qui est l'inverse honnête d'une métrique de succès.

## Notes

- **Fail-open, délibérément :** si le check du budget lui-même échoue (un échec
  de lecture FinOps), l'inférence est autorisée plutôt que bloquée
  silencieusement — un compteur cassé ne doit pas devenir une panne. L'échec
  est journalisé et visible.
- La capacité réservée (`reserved_micro_usd`) compte dans la limite, de sorte
  qu'un budget ne peut pas être contourné par une pré-réservation.
- `cost_type` n'est délibérément **pas** une dimension de budget — les lignes de
  fallback estimé circulent sur la dimension à laquelle elles appartiennent au
  lieu de former un pool parallèle.
