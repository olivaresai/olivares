---
title: "Recette : approbations human-in-the-loop"
description: >-
  Mettre les actions destructrices derrière des approbations gouvernées : ouvrir
  une demande liée au plan exact, laisser des humains autorisés décider avec
  séparation des tâches et expiration imposées côté serveur, et obtenir la
  décision enregistrée dans le ledger.
sidebar:
  order: 3
---

**Objectif :** « un apply de déploiement (ou un déclenchement d'orchestration,
ou une ouverture de session vocale) n'a pas lieu tant qu'un humain qui n'est
*pas* le demandeur ne l'approuve pas — et la décision est un fait enregistré. »

Le moteur d'approbation est actif dans le binaire par défaut ; le
[modèle de gouvernance](/fr/how-to/govern-and-approve/#la-posture-human-in-the-loop)
explique la posture. Cette recette est le câblage opérationnel.

## 1. Câbler la barrière d'approbation

Les actions de module qui muteraient l'infrastructure passent par le pont
human-in-the-loop. Il est activé par configuration — sans lui, ces actions
restent deny-closed :

```bash
OLIVARES_APPROVAL_BRIDGE_CONFIG=/etc/olivares/approval-bridge.json
```

Exécutez le composant qui *ouvre* les approbations sous son **propre compte de
service, jamais membre du pool d'approbateurs**. La séparation des tâches est
imposée côté moteur (l'ouvreur ne peut pas décider de sa propre demande, et un
token système ne peut pas approuver du tout) — si le compte de l'ouvreur est
aussi approbateur, vous avez construit un blocage de vivacité, pas un contrôle.

## 2. Ouvrir une demande

```bash
curl -ks -X POST "$BASE/v1/m/governance/approvals" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  -H 'Content-Type: application/json' \
  -d '{
    "subject_kind": "deployment",
    "subject_ref": "deploy:payments-api",
    "action": "deploy.apply",
    "reason": "rollout v2.4.1",
    "expires_in_seconds": 3600
  }'
```

La demande s'ouvre **deny-closed et limitée dans le temps**, liée au plan exact
qu'elle couvre. Si une *politique* d'approbation activée correspond à
`(action, subject_kind)`, le `required_approvals` de la politique fait foi — un
demandeur ne peut pas abaisser la barre depuis le côté demande.

## 3. Décider

```bash
# The queue (filter by status / action):
curl -ks "$BASE/v1/m/governance/approvals?status=pending" \
  -H "Authorization: Bearer $APPROVER_TOKEN" -H "X-Olivares-Tenant: $TENANT"

# The decision (approval-admin permission):
curl -ks -X POST "$BASE/v1/m/governance/approvals/$ID/decisions" \
  -H "Authorization: Bearer $APPROVER_TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  -d '{"decision":"approve","note":"reviewed the plan hash"}'
```

Ce que le moteur impose côté serveur — rien de tout cela n'est une convention
client :

- **Séparation des tâches :** le décideur est clé sur l'identifiant
  utilisateur stable ; le demandeur ne peut pas décider, et le même humain ne
  peut pas décider deux fois (un index unique, pas une règle d'UI).
- **Expiration :** une demande expirée ne peut jamais recevoir de décision
  liante, même avant que le balayeur ne matérialise l'état.
- **Plancher par palier de risque :** les actions pré-classées CRITICAL (la
  famille kill-switch, la finalisation de credential et leurs pairs) exigent
  **au moins deux approbateurs humains distincts avec authentification forte
  (AAL3) par décision** — et le plancher est structurel : une politique
  d'approbation qui tente d'abaisser le palier est ramenée au plancher au point de
  décision.

## 4. L'enregistrement

Chaque décision est ajoutée à l'audit ledger avec l'acteur réel dans la même
transaction — `GET /v1/m/governance/approvals/{id}/decisions` est la piste
immuable, et l'[export par pull](/fr/how-to/forward-audit-to-splunk/) la
transporte vers votre SIEM. Vous ne pouvez pas effectuer un changement gouverné
que le ledger oublie silencieusement.

## Notes

- `escalate_in_seconds` notifie l'équipe SoD si une demande reste indécise —
  utilisez-le pour les actions critiques en production.
- L'annulation (`POST …/{id}/cancel`) est destinée au demandeur ou à un admin
  sur une demande en attente ; elle est enregistrée elle aussi.
- Ce qui est encore en maturation, c'est la **console** de revue plus riche ;
  les garanties côté moteur ci-dessus sont actives
  ([périmètre honnête](/fr/how-to/govern-and-approve/)).
