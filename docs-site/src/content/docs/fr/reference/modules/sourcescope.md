---
title: "Cadrage des sources et des identifiants"
description: >-
  Lie une source connectée — un serveur MCP, un modèle, un fournisseur, une base
  de connaissances ou une source de données — à un espace de travail ou à un
  groupe d'agents, et résout, au moment où un agent ou une session y accède, si
  l'acteur est dans le périmètre et quelle référence d'identifiant s'applique.
  Échec fermé par construction.
---

Le cadrage des sources et des identifiants (`modules/sourcescope`) répond à
une seule question à l'exécution : lorsqu'un agent ou une session accède à une
source connectée — un serveur MCP, un modèle, un fournisseur, une base de
connaissances ou une source de données — **cet acteur est-il dans le périmètre,
et quelle référence d'identifiant s'applique ?** Il est **EN PRODUCTION** : la
table de liaison, son API d'écriture et le résolveur que les PEP du runtime
appellent sont tous livrés dans le binaire.

C'est un module plutôt qu'une colonne, car le périmètre qu'il applique n'est pas
une propriété d'une seule entité source — la configuration MCP, les modèles, les
fournisseurs et les bases de connaissances vivent dans des modules différents, et
seul l'axe agent/session/ressource porte un espace de travail. Le périmètre est
une **liaison** : `(source) → (espace de travail ou groupe d'agents)`, avec une
référence d'identifiant cadrée optionnelle. Ce module détient cette table de
liaison et le résolveur.

## La liaison et son API

`/v1/m/sourcescope/bindings` est une surface CRUD standard, contrôlée par
`sourcescope:binding:read` et `:binding:write`. Une liaison cible un type de
source (`mcp`, `model`, `provider`, `knowledge`, `data`) et un arbre de périmètre
(`workspace`, `agent_group`), et porte une **`CredRef` sans valeur** — un nom
logique, un localisateur `ref_kind` (`env`, `vault`, `secret_manager`, `file`,
`other`) et un indice masqué optionnel. Aucun champ ne peut contenir un secret
utilisable ; le gestionnaire rejette un identifiant en ligne, le même invariant de
données minimales que `capabilities.mcp_config.secret_refs`.

## Comment le résolveur décide

La décision échoue en mode fermé et est composée, et non un second moteur
d'autorisation :

- **Containment** — une source liée à l'espace de travail W est résoluble par un
  agent ou une session dans W sans configuration supplémentaire.
- **Grant** — une autorisation (grant) Cedar cadrée, couvrant
  [`x-models`](/fr/reference/modules/x-models/), issue de
  [`vi-governance`](/fr/reference/modules/vi-governance/), ouvre un espace de travail
  étranger.
- **RBAC** — une autorité à l'échelle du locataire voit toujours tout ; l'espace
  de travail est une isolation souple, le locataire est la frontière dure.
- **Forbid** — une interdiction (forbid) Cedar cadrée prime sur tout ce qui
  précède.

Le contrôle est **additif** : une source non liée reste globale pour la
rétrocompatibilité ; une source liée sans périmètre contenant, sans autorisation
et sans RBAC est **refusée**. Le résolveur est câblé comme le `ScopeGate` sur la
chaîne d'exécution des modèles et sur la récupération de
[`viii-knowledge`](/fr/reference/modules/viii-knowledge/).

## Contexte délimité, énoncé clairement

- Il s'agit **uniquement de liaison par référence**. La **consommation** d'un
  identifiant cadré dans un appel réel à un fournisseur, et un **broker MCP** à
  l'exécution qui se connecte à un serveur pour le compte d'un agent, **n'existent pas
  encore dans l'arborescence** — le résolveur renvoie la référence dans le
  périmètre, mais rien ici ne l'utilise pour authentifier un appel sortant.
- Le périmètre de l'acteur provient de l'agent/session **nommé par la référence
  d'acteur de l'appelant**. Les valeurs de périmètre sont lues depuis la ligne
  stockée (un appelant ne peut pas injecter un espace de travail), mais le choix
  de l'agent appartient à l'appelant ; lier cette référence au principal est un
  durcissement à venir. Voir
  [honnêteté et limites](/fr/start/honesty-and-limits/).

## Voir aussi

- [Gouvernance (vi)](/fr/reference/modules/vi-governance/) — l'algèbre des
  autorisations/interdictions Cedar et le RBAC que le résolveur compose.
- [Modèles (x)](/fr/reference/modules/x-models/) — la chaîne d'exécution où
  s'exécute le `ScopeGate`.
- [Connaissances (viii)](/fr/reference/modules/viii-knowledge/) — la récupération
  gouvernée, le second endroit où le résolveur applique son contrôle.
