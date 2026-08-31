---
title: "Construire un workflow gouverné (DAG)"
description: "Composez les actions gouvernées existantes dans un graphe de dépendances, examinez son plan d'exécution sans effets secondaires et exécutez-le derrière une approbation humaine liée au graphe exact qui a été examiné."
---

Un **workflow** enchaîne des actions que la plateforme gouverne déjà —
déclencher une planification, signaler d'autres modules, envoyer une
notification de test, attendre — dans un graphe de dépendances (un DAG).
L'exécuter constitue un unique acte privilégié, approuvé par un humain, et
chaque étape qui agit sur quelque chose laisse une ligne dans le même ledger
de décisions append-only que le déclenchement unique d'une planification.

Les workflows sont **de la composition, pas un nouveau pouvoir**. Il n'existe
délibérément aucun type d'étape qui exécute une commande, appelle une URL
arbitraire ou transporte une payload : un graphe ne peut que réorganiser des
verbes que l'estate expose déjà, sous les gates qui existent déjà. Exécuter un
workflow exige le niveau admin *et* l'approbation d'un humain ; ce n'est donc
jamais un moyen d'atteindre quelque chose qui ne serait pas directement
accessible.

## Forme d'un graphe

Un workflow est un ensemble d'**étapes**, chacune avec une courte `ref` unique
dans le workflow, un `kind`, sa `config` typée et les refs dont elle
`depends_on`. Le graphe doit être acyclique ; avant tout stockage, le serveur
impose cette propriété ainsi que l'existence des références et les limites de
fan-in/fan-out.

| Type | Fonction | Gates traversés |
|---|---|---|
| `schedule-fire` | déclenche une planification gouvernée existante | kill switch, budget, couture du dispatcher |
| `eventing-emit` | publie un événement `workflow.signal` auquel d'autres modules peuvent s'abonner | — |
| `notify-test` | envoie le test synthétique par une route d'alerte | couture de l'actionneur notify |
| `wait` | met l'exécution en pause pour une durée bornée (1 s–24 h) | — |
| `approval-gate` | ouvre une approbation humaine **au milieu du graphe** et met en pause jusqu'à la décision | gate d'approbation |

`eventing-emit` publie un type d'événement **fixe**. La configuration de
l'étape ne fournit qu'un label ; l'auteur d'un workflow ne peut donc jamais
forger un événement first-party tel que `edge.observed` dans l'ingestion d'un
autre module.

## 1. Déclarer le workflow

```bash
curl -sS -X POST "$OLIVARES/v1/m/orchestration/workflows" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  -H 'Content-Type: application/json' -d '{
    "name": "release-train",
    "steps": [
      {"ref":"announce","kind":"eventing-emit","config":{"label":"starting"},"depends_on":[]},
      {"ref":"hold","kind":"approval-gate","config":{"reason":"release window"},"depends_on":["announce"]},
      {"ref":"deploy","kind":"schedule-fire","config":{"schedule_id":"<id>"},"depends_on":["hold"]}
    ]}'
```

La création est de niveau **write**. Un graphe refusé revient sous la forme
d'un `400` qui nomme l'étape fautive :

```json
{"error":{"message":"step deploy: schedule <id> is retired","step_ref":"deploy"}}
```

La console ancre cette `step_ref` au nœud du canevas. Remplacer ultérieurement
le graphe est un unique `PUT .../steps` atomique — le graphe est examiné et
approuvé dans son ensemble, jamais étape par étape.

Chaque modification ajoute un instantané complet à un ledger de révisions, et
toute révision antérieure peut être restaurée au moyen de la même validation
que les verbes actifs.

## 2. Examiner le plan — sans effets secondaires

```bash
curl -sS -X POST "$OLIVARES/v1/m/orchestration/workflows/$ID/dry-run" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT"
```

Le dry-run renvoie les étapes dans l'ordre topologique, ce que chacune ferait,
les gates qu'elle traverserait et un avertissement lorsqu'une référence est
devenue obsolète depuis l'enregistrement du graphe (par exemple, une
planification retirée la semaine précédente). Il n'écrit rien, ne dispatche
rien et n'ouvre aucune approbation ; c'est donc une **lecture**, disponible
pour toute personne autorisée à lire les workflows.

Il renvoie également le `plan_hash` — l'empreinte du graphe exact. Continuez.

## 3. Exécuter — deux phases, liées à ce qu'un humain a vu

L'exécution est de niveau admin **et** sous gate. La première phase ouvre
l'approbation :

```bash
curl -sS -X POST "$OLIVARES/v1/m/orchestration/workflows/$ID/run" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT"
# 202 {"op":"run_request","approval_ref":"…","gate_status":"pending", …}
```

Un humain décide au moyen de l'API des décisions de gouvernance. La seconde
phase consomme ensuite cette décision en renvoyant sa référence :

```bash
curl -sS -X POST "$OLIVARES/v1/m/orchestration/workflows/$ID/run" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  -H 'Content-Type: application/json' -d '{"approval_ref":"…"}'
```

L'approbation est **liée au hash du plan**. Modifiez le graphe entre les deux
phases : le hash change, l'approbation n'autorise plus rien et l'exécution est
refusée — le « oui » d'un humain s'applique au graphe qu'il a examiné, jamais
à un autre substitué ensuite. L'exécution utilise alors un **instantané** de
ce graphe ; une modification en cours d'exécution ne peut donc pas changer ce
qui est déjà en train de s'exécuter.

Le deny-by-default reste en vigueur partout : si aucun gate d'approbation
n'est câblé, l'exécution est refusée et la lacune de gouvernance est remontée
comme finding au lieu d'être silencieusement autorisée.

## 4. Suivre l'exécution

```bash
curl -sS "$OLIVARES/v1/m/orchestration/workflows/$ID/runs/$RUN" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT"
```

Chaque étape rapporte son propre état. Une étape dont l'upstream a échoué est
`skipped` — l'exécution ne se poursuit jamais après un échec et ne déclare
jamais un succès qu'elle n'a pas obtenu. Un `wait` indique quand il reprend ;
un `approval-gate` indique l'approbation qu'il attend. Lorsqu'un arrêt
d'urgence est engagé, toute l'exécution **se fige** avec un `paused_reason`
visible et reprend lorsque l'arrêt est levé ; un arrêt n'est jamais absorbé
silencieusement et ne fait jamais échouer immédiatement toute l'exécution.

Les étapes avancent grâce à un processus en arrière-plan. Les attentes et les
approbations au milieu du graphe progressent donc sans que personne garde une
requête ouverte.

### Ce que le ledger consigne

Chaque étape d'actuation ajoute une ligne immuable attribuée à l'humain qui a
lancé l'exécution. Deux propriétés sont à connaître :

- Une exécution **refusée** est elle aussi consignée. Les refus sont de la
  preuve.
- Si le résultat d'une actuation arrive après que le runner l'a déjà
  abandonnée, ce résultat est **réconcilié** dans le ledger avec la véritable
  référence de dispatch. L'étape peut afficher « résultat inconnu » — mais le
  ledger ne prétend jamais qu'une actuation non survenue a eu lieu, et ne
  dissimule jamais une actuation qui a eu lieu.

## Délibérément hors scope

- **Déclencheurs automatiques.** Un workflow s'exécute lorsqu'un humain
  l'approuve. Câbler cron ou un événement pour démarrer une exécution ajoute
  un chemin d'actuation sans surveillance et reste derrière le rail de
  planification existant dans une modification distincte.
- **Étapes à effets secondaires arbitraires** (HTTP, exec). Elles
  transformeraient une surface de composition en moteur d'exécution général
  et annuleraient la propriété selon laquelle un workflow ne peut que
  réorganiser des verbes déjà gouvernés.

## Voir aussi

- [Gouverner et approuver](/fr/how-to/govern-and-approve/) — le moteur
  d'approbation que l'exécution et le gate au milieu du graphe traversent.
- [Référence des événements](/fr/reference/events/) — `workflow.signal` et la
  permission nécessaire à un abonné pour le recevoir.
