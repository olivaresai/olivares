---
title: "Module III — la carte d'accès en lecture/écriture"
description: >-
  Une capacité clé et différenciée : une carte d'accès en lecture/écriture de chaque arête
  origine→ressource, avec le diff Permis-vs-Observé (dérive de moindre privilège). Comment les
  arêtes sont construites, classifiées et fiabilisées, et les limites.
---

Le module III est la **carte d'accès en lecture/écriture** : quelle origine (agent, identité,
session) touche quelle ressource, classifiée en lecture ou lecture-écriture, et le **diff
Permis-vs-Observé** qui fait apparaître la dérive de moindre privilège. C'est l'une des
capacités les plus utiles et les plus différenciées du produit — l'un des 30 modules, pas le
produit entier. Cette page est la référence de ce qu'est la carte et de la façon de la lire
honnêtement.

## L'arête

La carte est un graphe d'**arêtes**. Chaque arête est le fait normalisé, à donnée minimale,
`origin → resource`, qui porte :

| Champ | Valeurs | Signification |
|---|---|---|
| **mode** | `read` \| `write` \| `readwrite` \| `unknown` | la classification lecture/écriture (`unknown` lorsqu'elle ne peut être déterminée — jamais devinée) |
| **source** | `otel` \| `mcp_annotation` \| `pg_audit` \| `cloudtrail` \| `ebpf` \| `policy` \| `a2a` | quel signal a produit l'arête |
| **confidence** | `attributed` \| `approximate` | avec quelle fermeté l'accès est rattaché à l'origine |

Les arêtes arrivent sur le bus d'événements sous forme d'événements
[`edge.observed`](/fr/reference/events/), et le moteur les fusionne dans l'entité persistée
`AccessEdge` — laquelle porte elle-même à la fois le côté **permis** et le côté **observé**,
de sorte que la carte d'accès est une **vue au-dessus du modèle de données général**, et non
un magasin séparé.

## Comment les arêtes sont construites

Le module III croise deux chemins :

- **Chemin coopératif** — les agents qui émettent de l'OpenTelemetry (`otel`) et exposent des
  serveurs MCP. Combiné à l'**audit natif du magasin**, ceci est de haute fidélité : pgAudit
  de Postgres (`pg_audit`) classifie READ/WRITE verbatim ; AWS CloudTrail (`cloudtrail`)
  fournit le `readOnly` de S3 ; les entrepôts de données de même.
- **Chemin non coopératif** — un **filet de sécurité eBPF/Tetragon** au niveau noyau (`ebpf`)
  enregistre `MAY_READ`/`MAY_WRITE` au niveau de l'appel système, hors du contrôle de l'agent
  (anti-évasion), aveugle au corps chiffré.

Les annotations d'outils MCP (`readOnlyHint`/`destructiveHint`, source `mcp_annotation`) sont
un signal utile mais sont **non fiables selon la spécification MCP** — le produit les
**corrobore** et ne s'y fie jamais seules.

Le côté **permis** (source `policy`) provient des grants déclarés ; le côté **observé**
provient des signaux ci-dessus.

## Permis vs Observé (dérive de moindre privilège)

La vue déterminante est le **diff** entre ce qu'une origine est *autorisée* à toucher et ce
qu'elle est *observée* en train de toucher. Elle fait apparaître :

- les **accès inattendus** — une origine a utilisé une ressource qui ne lui a jamais été
  accordée ;
- les **grants inutilisés** — une permission qu'aucune origine n'a jamais exercée ;
- les **réconciliations en attente** — un accès que le système ne peut pas encore attribuer
  fermement.

Le [tutoriel de zéro au graphe](/fr/tutorials/zero-to-graph/) atteint un résultat de dérive
peuplé sur le parc de démonstration.

:::caution[Limites honnêtes]
- **L'identité par agent est une dépendance dure.** L'audit attribue l'activité à un
  identifiant (credential) ou à un rôle, pas intrinsèquement à un agent. Un compte de service
  partagé avec un pool de connexions effondre l'attribution à `approximate`. Bien gouverner
  signifie émettre une identité par agent (le pont vers le module VI).
- **La couverture est en paliers.** *Propre* sur les magasins disposant d'un audit natif (SQL,
  stockage d'objets, entrepôts) ; *avec pertes* sur certains magasins (document/vecteur) ;
  **impossible à reconstruire passivement** sur d'autres (par ex. Redis, SQLite, D1). Une arête
  absente n'est **pas** une preuve qu'un accès n'a pas eu lieu là où la couverture est avec
  pertes ou absente.
- **`unknown` et `approximate` sont montrés, pas masqués.** Le produit ne fabrique jamais une
  classification ou une certitude qu'il n'a pas.
:::

## Lire la carte

Les résultats de la carte d'accès — y compris la dérive Permis-vs-Observé — sont servis par
des routes de module publiées dans la référence **bêta** distincte des
[routes de module](/reference/api-beta/) (et non dans le contrat stable du cœur) ; leurs
formes au niveau des champs vivent dans les interfaces typées Go/TypeScript du produit, et
l'UI web rend le graphe et la surcouche de dérive par-dessus. Lire le graphe d'accès est
une action **privilégiée, à portée de tenant et entièrement auditée** (le rôle editor et
au-dessus, jamais le viewer le plus bas) — voir le
[modèle de sécurité](/fr/explanation/security/security-model/) et le
[modèle de menace](/fr/explanation/security/threat-model/).

## En lien

- [Référence du bus d'événements](/fr/reference/events/) — l'événement `edge.observed` et sa
  charge utile.
- [Vue d'ensemble de l'architecture](/fr/explanation/architecture/overview/) — où se situe le
  module III.
- [Gouverner et approuver](/fr/how-to/govern-and-approve/) — agir sur la dérive.
