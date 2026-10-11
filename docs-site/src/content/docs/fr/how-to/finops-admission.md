---
title: "Réserver, solder et rapprocher l’admission FinOps"
description: >-
  Comment un effet facturable réserve une dépense et reçoit un seul handle,
  comment valider ou libérer ce handle, comment se comportent les nouvelles
  tentatives et les validations tardives, et comment lire le rapprochement de
  l’admission.
sidebar:
  order: 21
---

Les budgets et l’analyse des dépenses FinOps sont des fonctions **[Business](https://olivares.ai/pricing)**. Community conserve le suivi des coûts par session et l’exportation des données. Les budgets enregistrés avant 0.1 restent consultables et supprimables, et sont appliqués tant que le module FinOps est actif ; Community ne peut pas les créer ni les modifier. Les évaluations et les environnements de test restent dans Community.


Un effet facturable **réserve** sa dépense estimée avant de s’exécuter et reçoit
**un seul handle**. Quand l’effet a eu lieu, l’appelant **valide** (commit) le
coût mesuré avec ce handle. S’il n’a pas eu lieu, il **libère** la retenue. Les
contrôles du moteur lui-même (le proxy d’inférence, le lancement de sessions et
les tâches planifiées) le font pour eux-mêmes. Cette page s’adresse à un
connecteur qui appelle les routes et à l’exploitation qui lit le résultat.

## Réserver

```bash
olivares finops admission reserve --data @reserve.json -o json
```

```json
{
  "scope": "model_gateway",
  "idempotency_key": "gateway/req-7f3a",
  "estimate_micro_usd": 2000000,
  "actor_ref": "alice",
  "dims": { "provider_ref": "anthropic", "model_ref": "claude-sonnet-4" }
}
```

L’estimation est retenue sur chaque budget appliqué qui couvre la requête et, si
`actor_ref` est fourni, sur les limites de dépense de cet acteur. La réponse
porte le seul handle :

```json
{
  "allowed": true,
  "handle": "0192f6c4-7d2e-7a31-9b7c-4d6f1e2a3b4c",
  "estimate_micro_usd": 2000000
}
```

Une estimation nulle ne retient rien et ne renvoie aucun handle. Un plafond déjà
dépassé la refuse quand même.

| Statut | Signification |
|---|---|
| 200 | Admise. `handle` est vide quand rien n’a été retenu. Avec `"unreachable": "allow"`, une admission qui n’a pas pu être établie est admise sans retenue, avec la raison `admission could not be established; admitted without a hold (unreachable=allow)`. |
| 402 | Un verdict de blocage : un budget ou une limite de dépense avec `action=block` n’a plus de marge (`spend_limit` vaut true quand une limite par siège a refusé), l’ensemble des budgets est trop grand pour être évalué, ou le locataire est sous une frontière d’activation du cycle de vie des tentatives ou son état ne peut pas être lu. La raison dit lequel. |
| 429 | Un budget avec `action=throttle` n’a plus de marge. |
| 503 | L’admission n’a pas pu être établie : le stockage des budgets n’a pas pu être lu, la clé est restée occupée au-delà des nouvelles tentatives (la réclamation en cours d’un autre appel, ou une paire de retenues publiée par une version antérieure dont la retenue restante retient encore), la clé porte une réclamation laissée par une version antérieure, ou la liste des retenues dues de la clé ne se décode pas. La raison est `budget store unreachable (deny-closed)`. |
| 409 | La clé d’idempotence a servi avec un autre contenu. |
| 500 | La ligne d’admission de la clé a échoué à son contrôle d’intégrité. La clé est refusée dans toute posture jusqu’à la réparation de la ligne. |
| 400 | Le document est inexploitable : une portée inconnue, une clé absente, une estimation négative ou un champ que le schéma ne publie pas. |

Un 402 ou un 429 dû à un plafond est définitif : ne relancez pas le même effet
avant une nouvelle période ou une limite plus haute. Un 402 dû à une frontière
d’activation dure tant que le cycle de vie des tentatives tient le registre du
locataire. Un refus 402, 429, 503 ou 500 est aussi une ligne d’audit
`finops.admission.denied`.

La posture par défaut quand l’admission ne peut pas être établie est
**refuser**. Indiquez `"unreachable": "allow"` dans le document de réservation
pour admettre une telle requête sans retenue : la réponse est 200 avec la raison
ci-dessus, et le journal du moteur l’enregistre en ERROR avec le locataire, la
portée, `posture=allow outcome=admitted` et la classe de l’échec, jamais avec le
message du stockage lui-même. Une valeur inconnue vaut refus.

## Règle de nouvelle tentative

- **Réservation.** La même clé d’idempotence avec le même contenu, dans la
  fenêtre de rejeu, reçoit le handle du premier appel (`"replayed": true`). La
  fenêtre dure cinq minutes à partir de la réponse, ou à partir de la validation
  une fois la retenue validée. Hors de la fenêtre, et pour un appel qui n’a rien
  retenu, la requête est évaluée à nouveau.
- **Validation.** Après une réponse incertaine, répétez `commit` avec le même
  handle et le même montant autant de fois que nécessaire. Chaque répétition
  aboutit aux mêmes lignes. Un autre montant après une validation est refusé
  avec **409** : le premier coût mesuré fait foi.
- **Libération.** Répétable sans limite. Une libération n’annule jamais une
  validation.

## Valider et libérer

```bash
olivares finops admission commit --data '{"handle":"0192f6c4-7d2e-7a31-9b7c-4d6f1e2a3b4c","actual_micro_usd":1500000}'
olivares finops admission release --data '{"handle":"0192f6c4-7d2e-7a31-9b7c-4d6f1e2a3b4c"}'
```

Ingérez le coût mesuré avant de valider, pour que le plafond ne compte jamais
trop peu. Les documents portent `handle` et rien d’autre qui nomme une retenue :
tout autre champ est refusé avec 400. Un handle vide ne solde rien.

| Statut | Signification |
|---|---|
| 200 | Soldée, ou déjà soldée de la même façon. |
| 409 | Un autre montant après une validation ; une retenue dont l’admission est encore une réclamation en cours, si bien qu’aucun appelant n’a reçu ce handle ; ou une retenue dont l’argent appartient désormais au cycle de vie des tentatives (`lifecycle_api_required`). |
| 500 | La ligne d’admission qui nomme la retenue a échoué à son contrôle d’intégrité. Rien n’a été écrit : gardez le coût ingéré et répétez le même appel une fois la ligne réparée. |
| 400 | Un handle qui n’est pas une identité de retenue, ou un montant négatif. |

`committed: true` et `released: true` disent que l’appel a été accepté, pas
qu’une ligne a changé : valider un handle vide ne solde rien, et libérer une
retenue validée la laisse validée.

## Règle de validation tardive

Une validation enregistre que l’effet a eu lieu : elle est donc acceptée si tard
qu’elle arrive, après une libération, après l’expiration de la retenue ou après
qu’un autre appel a repris la clé. Les lignes de la retenue passent validées au
montant mesuré et gardent l’instant où leur retenue a pris fin. Aucun plafond de
budget ne change, car une ligne libérée ou expirée ne retient rien. Une
validation tardive d’une retenue expirée fait baisser `expired_unsettled` au
rapprochement suivant.

## Retenues laissées par une version antérieure de l’admission

Une base qui a exécuté une version antérieure de l’admission peut contenir des
lignes écrites par cette version. Une clé qu’elle a admise sous deux retenues se
solde par l’une ou l’autre, et les deux se soldent ensemble. Une réclamation
qu’elle a laissée en cours n’est retirée que sous un instant d’arrêt déclaré par
l’exploitation :

```bash
OLIVARES_FINOPS_ADMISSION_LEGACY_WRITERS_STOPPED_AT=2026-09-20T09:00:00Z
```

Réglez-le sur l’instant où chaque écrivain de la version antérieure s’est arrêté,
en heure RFC 3339 UTC terminée par `Z`. Il est lu une fois au démarrage. La
récupération ne retire une telle réclamation qu’une fois cinq minutes écoulées
depuis cet instant, et seulement tant qu’aucune ligne de ces écrivains n’est
datée plus tard. Vide, la valeur par défaut, n’en retire aucune. Un texte qui
n’est pas un tel instant n’en retire aucune et journalise une erreur au
démarrage. Le rapport montre l’état dans `legacy_stop` : `absent`, `invalid`,
`future`, `contradicted`, `waiting` ou `usable`.

## Rapprochement

Le moteur exécute la récupération chaque minute et la tâche de rapprochement
toutes les cinq minutes, pour chaque locataire actif.

```bash
olivares finops admission reconciliation -o json
olivares finops admission reconcile -o json
```

`reconciliation` ne fait que lire, avec la lecture des budgets : rien n’est
récupéré, balayé ni signalé. `reconcile` est la tâche, avec l’écriture des
budgets : elle exécute la récupération, balaie les retenues expirées sans être
soldées et signale un constat de type `finops_reservation_drift` quand `drift`
vaut true. La console montre le même rapport à côté des budgets.

| Champ | Signification |
|---|---|
| `active`, `committed`, `released` | Lignes du registre dans chaque état |
| `expired_unsettled` | Lignes que personne n’a soldées ; le TTL a rendu la marge |
| `active_lapsed` | Lignes expirées que la tâche n’a pas encore balayées |
| `idempotency_orphans` | Lignes d’admission réservées dont le handle n’a aucune ligne au registre |
| `owed_remaining` | Retenues encore dues par une réclamation en cours ou par une ligne publiée |
| `legacy_pending`, `legacy_owes_release` | Réclamations et libérations laissées par une version antérieure |
| `unresolved` | Écritures de récupération dont l’issue n’est pas encore établie ; le passage suivant décide à nouveau |
| `undecodable` | Lignes d’admission dont la liste des retenues dues ne se décode pas |
| `frontier_blocked` | Retenues que la récupération ne peut ni solder ni abandonner sous une frontière d’activation |
| `corrupt` | Lignes d’admission qui échouent à leur contrôle d’intégrité ; elles sont comptées et jamais écrites |
| `drift` | True quand `expired_unsettled`, `active_lapsed`, `idempotency_orphans`, `unresolved`, `undecodable` ou `corrupt` n’est pas nul |

`unresolved` et `frontier_blocked` ne sont comptés que par `reconcile`, dont le
passage de récupération les remplit ; `reconciliation` les rapporte à 0.

La récupération n’a plus rien à faire pour un locataire quand les compteurs de
`owed_remaining` à `corrupt` sont tous nuls. Traitez une ligne `corrupt` comme
une faute d’intégrité du stockage de ce locataire et comparez-la à la dernière
sauvegarde. Les retenues qu’elle nomme expirent par leur TTL ; rien ne les solde
tant que la ligne n’est pas réparée.

## Portées

| Portée | Appelant |
|---|---|
| `model_gateway` | Le proxy d’inférence et le routage des modèles : réserve avant l’appel, puis valide ou libère |
| `session_launch` | Le lancement de sessions opérées et l’ouverture vocale |
| `scheduled_job` | Déclenchements d’orchestration, juges d’évaluation et tâches MCP |
