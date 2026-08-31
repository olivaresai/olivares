---
title: "Module X — gestion des modèles et des fournisseurs"
description: >-
  La couche de gouvernance sur l'ensemble de la pile de modèles d'IA — Claude,
  OpenAI, Gemini et l'inférence locale. Un catalogue de référence versionné, une
  matrice de capacités et une politique de routage qui résout une chaîne primaire +
  fallback ; il route mais n'exécute pas encore l'appel au modèle.
---

Le module X gouverne **l'ensemble de la pile de modèles et de fournisseurs d'IA** — Claude,
OpenAI, Gemini et l'inférence locale, pas un seul fournisseur. C'est un module de la
**couche Core** qui se situe *au-dessus* des connecteurs de modèles/fournisseurs : il ne
ré-implémente aucune intégration de fournisseur ni la passerelle d'inférence. Ce qu'il
possède, c'est la **couche de gouvernance** — un catalogue versionné, une matrice de
capacités cross-vendor, et une politique de routage nommée.

## Ce que c'est

Le module transforme les entités brutes `Provider`/`Model` que l'inventaire (module I)
découvre en un catalogue gouverné. Deux moitiés :

- **Un catalogue de référence déclaré** — une table versionnée-dans-le-repo, surchargeable par
  l'opérateur, des familles de modèles avec leurs capacités de fonctionnalités d'API déclarées
  et leurs **valeurs par défaut de list-price**. Les prix sont estampillés de la date à laquelle
  ils ont été déclarés (`pricing_as_of`), sont explicitement des *valeurs par défaut à vérifier
  sur la page de tarification de chaque fournisseur*, et ne sont jamais de la télémétrie
  fabriquée. Une famille sans entrée correspondante reste **non tarifée** plutôt que de se voir
  attribuer un prix inventé.
- **Enrichissement de l'estate vivante** — le module écoute le flux
  [`cost.sampled`](/fr/reference/events/) et enrichit les entités `Model`/`Provider` découvertes
  avec la famille, la fenêtre de contexte, la modalité, la tarification au token et l'ensemble
  de capacités (les champs de tarification que l'inventaire lui délègue).

Le vocabulaire de capacités est une seule **matrice cross-vendor** — toute la pile Claude
(prompt caching, batch, Files, citations, extended thinking, computer use, l'outil de mémoire,
context management, vision/PDF, structured outputs) plus les analogues que chaque autre
fournisseur expose réellement — de sorte que l'UI rend une seule matrice et qu'une politique de
routage peut exiger une capacité *à travers* les fournisseurs. Les familles Claude sont
cataloguées par famille (`claude-opus`, `claude-sonnet`, `claude-haiku`, `claude-fable`, `claude-mythos`), les versions
deprecated/legacy étant conservées sous des préfixes plus longs afin que les ids courants se
résolvent vers le niveau de prix courant.

## Son contrat et ses entités

Le routage est la surface d'actionnement, et il est **routing-only** :

- **La politique de routage** est persistée sur l'entité `Policy` du cœur (`Kind="routing"`) :
  des politiques nommées de sélection / fallback / version-pinning (cheapest-first,
  lowest-latency, capability-ordered, ou un modèle épinglé). `POST …/routing-policies/{id}/resolve`
  résout une politique en regard de l'estate gouvernée et renvoie une **chaîne primaire +
  fallback** avec la raison du choix. C'est **en lecture seule** : il calcule une sélection que
  le connecteur/la passerelle exécute ensuite — le module ne réalise **aucune inférence**.
- **La gouvernance des clés d'API / workspaces** est de la **métadonnée minimal-data uniquement**
  — quel agent ou quelle équipe utilise quel credential, porté comme un indice masqué, jamais la
  valeur du secret.
- Un **inventaire de rate-limits Anthropic** en lecture seule (les plafonds qu'une passerelle ou
  un proxy doit garder synchronisés) est servi comme un inventaire consultable ; ce n'est jamais
  un contrôle que le module mute, et il se dégrade en une réponse honnête
  *unavailable-with-reason* lorsque le connecteur Admin en lecture seule n'est pas provisionné.

Les lectures de catalogue et de fonctionnalités ne sont pas sensibles et sont gatées au niveau
viewer ; les mutations de routage et de gouvernance de clés sont un changement de niveau editor,
audité ; le chemin d'exécution gouvernée est une action de niveau admin distincte du resolve de
niveau lecture. Les routes sont publiées dans la
[référence des routes de module](/reference/api-beta/) **bêta** séparée, et non dans le contrat
stable du cœur ; leurs formes au
niveau des champs vivent dans les interfaces typées du produit.

## Ce qu'il consomme et produit

Le module **consomme** `cost.sampled` depuis le [bus d'événements](/fr/reference/events/) pour
enrichir le catalogue avec la tarification réelle au token et l'usage ; il n'introduit pas de
nouveau type d'observation. Sur le chemin d'exécution gouvernée, un appel réussi **produirait** un
`CostSample` expurgé vers FinOps — la sortie du modèle va à l'appelant, mais n'est persistée nulle
part ici. L'argent n'apparaît jamais sur cette surface : aucun montant en USD n'est renvoyé,
seulement des décomptes de tokens et la cible qui a servi.

:::caution[Limites honnêtes]
- **Actionnement routing-only.** Le module **résout** une route (chaîne primaire + fallback) mais
  **n'exécute pas l'appel au modèle**. Le chemin d'exécution gouvernée est une **jonction
  deny-closed** : sans exécuteur provisionné, il renvoie un `503` clair — le control plane peut
  *sélectionner* un modèle mais ne *dépensera* pas auprès d'un fournisseur. Lorsqu'un exécuteur est
  câblé, un budget FinOps à son plafond refuse la dépense *avant* tout appel au fournisseur.
- **La tarification déclarée est une valeur par défaut, pas une garantie.** Les list prices sont
  des valeurs par défaut vérifiées par l'opérateur estampillées d'une date ; le coût qui fait
  autorité pour l'usage réel est toujours le `CostSample` dérivé du connecteur, jamais le chiffre
  de commodité au token. Les familles sans correspondance sont affichées non tarifées — jamais avec
  un prix inventé.
- **Les modèles fraîchement annoncés sont listés mais signalés.** Un modèle en preview dont les
  capacités ne sont pas encore vérifiées contre une model card est catalogué avec son ensemble de
  capacités marqué *to-confirm* et laissé non tarifé, plutôt que d'inventer les données.
- **L'inventaire de clés est de la métadonnée, jamais un secret.** Le module persiste les relations
  de gouvernance et un indice masqué ; la valeur du credential ne quitte jamais l'API Admin du
  fournisseur et n'est jamais stockée. Certains fournisseurs n'exposent aucun inventaire de clés —
  une limite documentée, pas une omission.
:::

## Liens connexes

- [Catalogue des modules](/fr/reference/modules/overview/) — où se situe le module X et son statut d'actionnement.
- [Access & resource map](/fr/reference/modules/iii-access-map/) — la R/RW map et le least-privilege drift.
- [Référence du bus d'événements](/fr/reference/events/) — l'événement `cost.sampled` que ce module consomme.
- [Vue d'ensemble de l'architecture](/fr/explanation/architecture/overview/) — moteur, couches et connecteurs.
- [Gouverner et approuver](/fr/how-to/govern-and-approve/) — agir sur le routage et la gouvernance.
- [Honnêteté et limites](/fr/start/honesty-and-limits/) — le contrat observe-broadly / actuate-on-a-subset.
