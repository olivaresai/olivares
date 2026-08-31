---
title: "Module VI — identité, permissions et gouvernance"
description: >-
  Le plan de contrôle sur le modèle d'autorisation : réconciliation du registre
  des identités, le pont agent↔identité, le moteur ABAC en deny-only, et la barrière
  d'approbation human-in-the-loop avec une piste de décision en ajout seul. La
  racine de l'actionnement gouverné.
---

Le module VI est le **plan de gouvernance sur le modèle d'autorisation existant du
moteur** — il ne réimplémente **pas** l'enforcer ni les connecteurs d'identité, il
les consomme. Il lie cinq sous-systèmes derrière un contexte délimité unique
(l'identité et sa gouvernance) : un réconciliateur de **registre** d'annuaire, le
**pont agent↔identité** qui rend l'attribution ferme, un moteur **ABAC** en
deny-only, la barrière d'approbation **human-in-the-loop**, et des backends
d'**édition** de politiques/identités. C'est la racine de toute action *gouvernée*
dans le produit.

## Ce que c'est

Le module se situe sur la couche de gestion et est l'autorité de **décision** pour
le plan de contrôle : qui et quoi peut faire quoi, et quelles actions requièrent
d'abord un humain. Son contrat est la posture deny-only, refus par défaut
(deny-by-default), rendue applicable —

- **La réconciliation du registre** fait converger un annuaire connecté (les
  sources d'identité) vers les entités `Identity` canoniques du moteur ainsi que le
  graphe collection/appartenance détenu par le module, en find-or-create indexé sur
  l'id externe seul, de sorte qu'il **met à niveau la même ligne** que la carte
  d'accès crée à partir d'une référence d'audit. Cette convergence sur une seule
  ligne est ce qui rend possible une attribution ferme.
- **Le pont agent↔identité** lie un agent à l'id interne de l'identité non humaine
  canonique que présente son identifiant, résolvant la dépendance dure qui permet
  au module III (la carte d'accès) d'annuler la dérive (drift) erronée entre
  permis-et-observé (permitted-vs-observed).
- **Le moteur ABAC** est un évaluateur natif qui s'exécute **après** le RBAC et ne
  peut que *restreindre davantage* — il n'élargit jamais une autorisation.

## Son contrat et ses entités

Le module VI détient quatre entités dans le modèle de données partagé — une
**collection** et une arête **collection-member** (le graphe groupe/rôle dérivé des
sources, résolu transitivement dans des bornes), une **approbation** (une requête
HITL mutable), et une piste **approval-decision** en ajout seul. Les identités ne
sont **pas** dupliquées dans une table du module ; elles sont réconciliées vers
l'entité `Identity` canonique du moteur.

L'**évaluateur ABAC** implémente le seam d'évaluateur de politiques du moteur avec
des propriétés vérifiées : chaque règle est une règle de **refus** (deny) ; il
s'exécute après le RBAC à l'intérieur d'un ET (AND), de sorte qu'une politique ne
peut jamais étendre l'accès ; une politique *activée* malformée **échoue en
fermeture** (refuse) ; le chemin critique d'autorisation est servi depuis un cache
par locataire invalidé **après** qu'une écriture s'est validée (commit), isolé
strictement par locataire. Les specs de politiques sont **typées et re-sérialisées**
à l'écriture (le JSON de l'opérateur n'est jamais retransmis à l'identique), de
sorte qu'un identifiant ne peut pas pénétrer une spec. OPA/Rego est le seam
d'évaluateur externe, jamais une dépendance traînée dans le moteur.

La **barrière d'approbation** est la traçabilité action→humain que le registre
d'audit ancre : la séparation des tâches (separation-of-duty) et la garde contre le
décideur en double s'indexent sur l'**identité utilisateur stable** (un jeton
système ne peut pas décider), le seuil multi-approbation est **race-safe** sur le
store (un franchissement concurrent se résout en exactement un gagnant), et
l'expiration est dérivée paresseusement à la lecture puis matérialisée par un
balayage explicite et cadré par locataire. Les backends d'édition
(managed-settings/hooks, politique-en-tant-que-code Cedar/OPA, le graphe d'objets
WIF) ajoutent un chemin d'écriture **publication→révision-immuable→dérive (drift)** ;
pour Cedar, une politique publiée est activée sur la couche de superposition
deny-only en service par locataire et rechargée au démarrage, de sorte qu'une
affirmation `active` survit à un redémarrage.

## Ce qu'il consomme et produit

Le module **consomme** la base d'autorisation et d'audit du moteur ainsi que le
registre d'identités typé des sources d'annuaire configurées ; il remplit le champ
`Agent.IdentityID` dont dépend la carte d'accès. Il **produit** des événements
`FindingReport` sur le [bus d'événements](/fr/reference/events/) — une **identité
partagée** liée à plus d'un agent, plus l'**escalade** et l'**expiration**
d'approbations — chacun émis une seule fois, conditionné à un marqueur persisté de
sorte qu'un balayage répété ne puisse pas double-émettre. Chaque mutation
privilégiée, et les lectures d'identité et de liaison pertinentes pour la
réconciliation, **s'auto-auditent vers le principal réel** à l'intérieur d'une
transaction validée ; l'acteur d'audit est toujours une référence de principal
typée, jamais un e-mail.

:::caution[Limites honnêtes]
- **Le moteur ABAC est édité et audité, mais l'application dépend de la
  composition.** L'état de gouvernance est écrit et audité aujourd'hui ; la racine de
  composition au démarrage câble l'évaluateur et injecte les fournisseurs
  d'annuaire. Là où ils ne sont pas câblés, le moteur n'est pas en vigueur et une
  synchronisation de registre n'a pas de fournisseurs — c'est **énoncé, jamais une
  opération nulle silencieuse**.
- **L'attribution ferme requiert une identité par agent.** Une liaison rattache un
  agent à une identité *canonique*, jamais à une identité fraîchement émise utilisée
  pour feindre la réconciliation d'une entité partagée. Une identité liée à plus
  d'un agent **fait s'effondrer l'attribution** au niveau de l'identité — révélée
  honnêtement comme un constat, jamais récupérée.
- **La grammaire deny-only est bornée par conception.** Les règles v1 ne
  correspondent qu'aux attributs qui atteignent réellement l'évaluateur ; les règles
  sur attributs de ressource (p. ex. la sensibilité) nécessitent un seam central et
  constituent un suivi documenté — **non livrées comme syntaxe inerte**, un champ
  inconnu est rejeté à l'écriture. La politique *restreint* ; les autorisations
  additives restent dans le RBAC.
- **Un module ne peut pas énumérer les locataires.** L'expiration/escalade
  d'approbations est matérialisée par un **balayage explicite et cadré par
  locataire** — il n'existe aucune garantie d'arrière-plan inter-locataires, car en
  affirmer une serait un mensonge. L'expiration effective est tout de même honorée
  paresseusement à la lecture.
:::

## Voir aussi

- [Catalogue des modules](/fr/reference/modules/overview/) — où se situe le module VI et son statut d'actionnement honnête.
- [Carte d'accès et des ressources (III)](/fr/reference/modules/iii-access-map/) — le consommateur dont ce module résout la dépendance d'attribution.
- [Référence du bus d'événements](/fr/reference/events/) — l'événement `finding.reported` que ce module émet.
- [Gouverner et approuver](/fr/how-to/govern-and-approve/) — utiliser les surfaces de politique et d'approbation.
- [Vue d'ensemble de l'architecture](/fr/explanation/architecture/overview/) — le moteur et les couches sur lesquels ce module se compose.
- [Honnêteté et limites](/fr/start/honesty-and-limits/) — la posture fermée par défaut, détective par défaut.
