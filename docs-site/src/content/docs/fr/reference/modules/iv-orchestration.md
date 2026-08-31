---
title: "Module IV — communication inter-agents et orchestration"
description: >-
  Le plan d'observation et de gouvernance de la coordination des agents : un graphe
  dérivé de communication et de délégation, des agents planifiés gouvernés, et un
  déclenchement en deux phases sous contrôle HITL. Le dispatch en direct est une
  couture fermée par défaut, exposée honnêtement.
---

Le module IV est le plan d'**observation et de gouvernance** de la coordination des
agents. Il ne **réimplémente pas** un framework d'agents (pas de LangGraph/CrewAI/AutoGen),
il n'exécute pas d'agent, et il ne crée jamais de processus. Il dérive un graphe en
direct de communication et de délégation à partir de signaux déjà présents sur le bus,
gouverne les agents planifiés/autonomes comme des déclarations d'état désiré, et signale
l'évasion de cadence — tandis que l'acte d'*exécuter* un agent ne sort que par une
couture fermée par défaut.

## Ce que c'est

Deux choses coexistent côte à côte. D'abord, un **graphe dérivé de communication et de
délégation** — qui délègue à qui (superviseur→travailleur) et qui parle à qui — construit
comme une vue sur des arêtes d'accès déjà observées, frère de l'access map ([module III](/fr/reference/modules/iii-access-map/)),
jamais une seconde copie ré-ingérée. Ensuite, un registre de **planifications
gouvernées** : un agent planifié ou piloté par événement est une *déclaration d'état
désiré*, et en déclencher un est la seule action affectant la production que le module
expose.

## Contrat et entités

Le module possède trois types d'entités, déclarés dans le modèle de données partagé :

- **`orchestration.relation`** (upsert) — l'arête dérivée du graphe : un lien
  `delegation`, `mcp_server` ou `mcp_tool` entre deux références, avec une source de
  signal, un `mode` lecture/écriture, une `confidence`, des compteurs et un horodatage
  première/dernière vue.
- **`orchestration.schedule`** (cycle de vie) — une déclaration gouvernée : sujet, type
  de déclencheur (`cron`/`event`/`manual`), une **spécification de cadence opaque qui
  n'est jamais analysée pour s'auto-déclencher**, un intervalle attendu, un facteur de
  grâce, un statut désiré, et le principal déclarant enregistré comme propriétaire de tout
  déclenchement autonome.
- **`orchestration.decision`** (**ajout seul**) — un ledger immuable de chaque demande de
  déclenchement, déclenchement et manquement de cadence, portant le `plan_hash`, le statut
  de gate, l'`op_status` et le **principal réel** (jamais `system`, sauf pour la détection
  de manquement de cadence).

Les routes du module sont accessibles mais délibérément **hors** du contrat OpenAPI
servi ; leurs formes au niveau des champs vivent dans les interfaces typées du produit.
**Le déclenchement est en deux phases et sous contrôle HITL** : la phase un demande
l'approbation ; la phase deux re-vérifie l'approbation et une correspondance stricte du
`plan_hash` (anti-TOCTOU — un re-ciblage ou une re-cadence invalide une approbation
périmée) avant tout dispatch. Lire le graphe et déclencher sont des actions
**privilégiées, à portée tenant, entièrement auditées**, réparties par niveau de verbe
(lecture pour les observateurs, déclarer/recibler pour les éditeurs, **déclencher** pour
les admins uniquement) — voir
[gouverner et approuver](/fr/how-to/govern-and-approve/).

## Ce qu'il consomme et produit sur le bus

Il consomme exactement un canal : [`edge.observed`](/fr/reference/events/). Une arête
session→Task devient une relation de délégation ; les arêtes de topologie MCP deviennent
des relations serveur/outil ; tout le reste est ignoré. La vivacité observée d'un sujet
pour le contrôle de cadence est dérivée des relations elles-mêmes, de sorte qu'aucune
planification n'est interrogée par arête. Il produit des findings sur
[`finding.reported`](/fr/reference/events/) :
`orchestration_cadence_miss` quand une planification **active et récurrente** cesse
d'émettre par rapport à sa cadence déclarée (une planification ponctuelle ou en pause qui
s'est simplement terminée est un silence normal et n'émet rien), et
`orchestration_ungoverned_fire` quand une tentative de déclenchement ne trouve aucun gate
d'approbation câblé — la lacune de gouvernance est rendue visible tandis que le
déclenchement reste refusé. Le contrôle s'effectue à la lecture et est restreint au tenant
épinglé de la requête ; le module n'exécute jamais de balayage en arrière-plan
inter-tenant.

:::caution[Limites honnêtes]
- **Le déclenchement en direct est une couture fermée par défaut.** Le module *gouverne et
  planifie* ; il n'actionne jamais de lui-même. Un déclenchement sort par une couture
  Dispatcher. Avec le dispatcher non configuré (le binaire par défaut), un déclenchement
  approuvé renvoie un honnête `200` avec le statut `declared_not_fired` — l'état sûr est
  « déclaré, non déclenché ». Un dispatcher construit et configuré par l'opérateur achemine
  un déclenchement approuvé et correspondant au plan vers le même exécuteur de déploiement
  ou une tâche A2A vérifiée par carte signée ; une erreur de dispatcher renvoie `502` et
  n'avance jamais le dernier déclenchement. La délégation A2A en direct ajoute son propre
  point d'application des politiques refusé par défaut (carte signée → allowlist → hash de
  plan → approbation) et est contrôlée de la même façon.
- **La couverture du graphe est partielle, et elle le dit.** Chaque réponse de graphe porte
  un descripteur de couverture. Le graphe dérivé couvre la délégation Task, la topologie MCP
  et — lorsqu'un connecteur A2A est câblé — l'A2A pair-à-pair observé ; le cross-talk de
  swarm et les frameworks non-Task sans connecteur émetteur sont **absents, pas nuls**. Le
  module ne présente jamais le graphe comme l'ensemble complet des communications d'agents.
- **Données minimales sur le fil.** Le module persiste uniquement les relations et les
  preuves de gouvernance — qui↔qui, compteurs, horodatage, références caviardées —
  **jamais** les charges utiles de messages, prompts, arguments d'outils ou secrets.
  Aucune colonne de ce type n'existe ; les références sensibles sont hachées avant
  persistance. C'est une propriété du fil, pas un réglage.
:::

## Liens connexes

- [Catalogue des modules](/fr/reference/modules/overview/) — la couche du module IV et son statut d'actionnement honnête.
- [Référence du bus d'événements](/fr/reference/events/) — `edge.observed` en entrée, `finding.reported` en sortie.
- [Access & resource map](/fr/reference/modules/iii-access-map/) — le graphe frère que celui-ci dérive en parallèle.
- [Gouverner et approuver](/fr/how-to/govern-and-approve/) — le déclenchement en deux phases avec humain dans la boucle.
- [Honnêteté et limites](/fr/start/honesty-and-limits/) — ce qui actionne aujourd'hui et ce qui reste une couture.
- [Vue d'ensemble de l'architecture](/fr/explanation/architecture/overview/) — où se situe le module IV dans la couche Intelligence.
