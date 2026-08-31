---
title: "Module I — inventaire et découverte"
description: >-
  Découverte passive et catalogage de tout ce qui compose le parc (estate) — agents,
  sessions, serveurs MCP, skills, outils, modèles, fournisseurs et identités non humaines.
  Comment les entités sont matérialisées à partir des observations, ce que le catalogue
  enregistre, et les limites.
---

Le module I est le **catalogue du parc (estate)** : un inventaire passif, piloté par le bus,
de tout ce qui existe — agents, sessions, instances de Claude Code, serveurs MCP, skills,
outils, ressources, modèles, fournisseurs et identités non humaines. Il découvre en
*écoutant*, jamais en sondant, et n'enregistre que des relations, des identifiants et la
vivacité — jamais de charges utiles. Cette page est la référence de ce que le catalogue
contient et de ce qu'il ne contient délibérément pas.

## Ce qu'il matérialise

Les connecteurs émettent des **observations**, pas des entités. Ils publient sur le bus
d'événements des faits normalisés [`edge.observed`](/fr/reference/events/) et
[`cost.sampled`](/fr/reference/events/) ; les entités qu'ils impliquent ne sont jamais
envoyées. Le module I **matérialise** l'entité de cœur que chaque observation nomme à
partir de sa référence naturelle : une `session`/un `agent`/une `identity` d'origine, un
serveur MCP, un outil, une ressource, un skill, et — à partir des échantillons de coût —
un fournisseur et un modèle (découverts, **sans tarification** ; cela appartient à FinOps).
La matérialisation est **idempotente** sous une livraison « au moins une fois » :
find-or-create sur la clé naturelle, de sorte que la même observation vue deux fois ne
duplique jamais une entité.

## Son contrat et ses entités

Le module enregistre une entité qui lui est propre, `inventory.catalog_entry` — une
surcouche de découverte attachée à chaque entité de cœur matérialisée. Elle enregistre
*comment* une chose a été trouvée, et non *ce qu'elle* a fait : une liste de sources de
signal, les hôtes sur lesquels elle a été vue, les horodatages de première et dernière
observation, un compteur d'occurrences, et un `status` de vivacité `active` ou `stale`. Un
**balayage de péremption (staleness sweep)** périodique marque une entrée comme `stale`
lorsqu'elle n'a pas été vue dans la fenêtre configurée, et la repasse à `active` dès qu'elle
réapparaît ; le balayage ne s'exécute que sur les tenants que le module a effectivement
observés (il ne peut pas, et ne le fait pas, énumérer les tenants). La surface de lecture
est restreinte et en lecture seule : un `summary` comptant par kind et par source, une liste
`entities` paginée filtrable par kind et par statut, et une vue de détail d'une entité unique.
Chaque lecture exige une permission de lecture à portée de tenant et placée sous espace de
noms (le palier viewer le plus bas suffit) ; l'ingestion est à haute fréquence et n'est pas
auditée par écriture. Les formes complètes vivent dans la
[référence du bus d'événements](/fr/reference/events/) et dans les interfaces typées du produit.

## Ce qu'il consomme et produit

Le module I est un pur **consommateur**. Il s'abonne à `edge.observed`, `cost.sampled` et
`finding.reported` et n'écrit que sa propre surcouche de catalogue et les entités de cœur
qu'il en dérive. Il n'émet aucun événement propre et n'expose aucune surface d'actionnement
— la découverte est, par nature, observe-et-catalogue. Les références qu'il persiste
arrivent **déjà expurgées** depuis les connecteurs ; le module les stocke verbatim et
n'ajoute aucun détail brut qui lui soit propre, de sorte que la propriété de donnée minimale
est une propriété du fil, maintenue de bout en bout.

:::caution[Limites honnêtes]
- **L'inventaire ne possède pas le graphe d'accès.** À compter de la décision A
  (2026-06-03), le module III (la carte d'accès) est le **seul rédacteur** de l'`AccessEdge`
  en lecture/écriture et le seul propriétaire de la topologie et du diff Permis-vs-Observé.
  L'inventaire découvre et catalogue les *entités* qu'une arête nomme ; il n'enregistre plus
  l'arête elle-même, et ne sert aucune route de topologie. Le graphe n'est peuplé que lorsque
  le module III est câblé au démarrage.
- **La découverte n'est aussi complète que les signaux.** Une entité n'existe dans le
  catalogue que si un connecteur l'a observée. L'absence du catalogue n'est **pas** une preuve
  d'absence dans le parc là où la couverture est partielle.
- **La vivacité est la péremption, pas la santé.** `stale` signifie « pas vu récemment »,
  rien de plus ; le silence d'une session est normal, et la santé/le SLA formels relèvent du
  module XXII. Le balayage ne mute jamais le cycle de vie propre de l'entité de cœur.
- **Aucun détail fabriqué.** Le module ne stocke que des identifiants, des relations et des
  compteurs de vivacité — jamais de charges utiles, de secrets, de PII, de commandes, de
  requêtes ou d'URL.
:::

## En lien

- [Catalogue des modules](/fr/reference/modules/overview/) — où se situe le module I et la
  répartition honnête de l'actionnement (Actuate).
- [Module III — la carte d'accès](/fr/reference/modules/iii-access-map/) — le seul propriétaire
  du graphe R/RW et de la dérive.
- [Référence du bus d'événements](/fr/reference/events/) — les événements `edge.observed`,
  `cost.sampled` et `finding.reported` qu'il consomme.
- [De zéro au graphe](/fr/tutorials/zero-to-graph/) — peupler le catalogue et la carte sur le
  parc de démonstration.
- [Vue d'ensemble de l'architecture](/fr/explanation/architecture/overview/) — le moteur, les
  couches et le bus.
