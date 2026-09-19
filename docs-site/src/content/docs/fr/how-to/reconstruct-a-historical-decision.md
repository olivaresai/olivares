---
title: "Reconstruire une décision d'autorisation historique"
description: >-
  Ce qu'une reconstruction prouve, ce qu'elle ne peut pas prouver, et
  comment un auditeur rejoue l'allow du lundi après la révocation du mardi.
sidebar:
  order: 22
---

Un auditeur peut demander ce qu'une politique **a décidé** à une date
passée. Le plan de contrôle répond depuis le **registre d'évidence** :
la version de politique enregistrée et les entrées de cette décision. Il
n'évalue **pas** la politique active aujourd'hui.

## Commande

```bash
olivares policy replay \
  --at 2026-09-15T12:00:00Z \
  --principal agent-7 \
  --resource public.customers \
  --resource-kind postgres.table \
  --action SELECT
```

Ou une ligne stockée :

```bash
olivares policy replay --decision-id <decision-id> -o json
```

HTTP : `POST /v1/m/governance/decisions/replay` et
`GET /v1/m/governance/decisions/{id}/reconstruct`. Cette livraison
n'ajoute pas de bouton console. Utilisez la CLI ou HTTP.

## Ce qu'une reconstruction prouve

Le statut `reconstructed` signifie : une décision `live_authorization`
existe ; l'artefact nommé est **conservé** ; ce binaire peut exécuter
l'évaluateur (Cedar) ; la réévaluation utilise l'artefact **d'origine** ;
le PDP vivant n'a **pas** été lu.

`policy_version_id` est l'id d'artefact. Ce n'est pas le
`PolicyVersion` du témoin de route ni le numéro de révision d'auteur.

## Ce qu'elle ne prouve pas

- L'honnêteté du point d'application d'origine. Un allow stocké
  n'autorise pas à répéter l'effet.
- L'authenticité du producteur ni l'intégrité de la chaîne.
- Une activité externe sans décision Olivares : la réponse est
  `COULD NOT RECONSTRUCT`.
- Les moteurs que ce binaire n'exécute pas (OPA/Rego : rédaction
  seulement).

## COULD NOT RECONSTRUCT

```
COULD NOT RECONSTRUCT
missing: authorization_decision
```

`missing` nomme le fait absent. Les anciennes lignes restent
**unknown** ; le magasin ne les complète pas avec la politique actuelle.

## Lundi, mardi, mercredi

Lundi : enregistrer allow. Mardi : révoquer. Mercredi : `--at <lundi>`
répond encore **allow** depuis l'artefact du lundi.
