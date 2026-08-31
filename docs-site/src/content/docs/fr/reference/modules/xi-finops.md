---
title: "Module XI — coûts et FinOps de l'IA"
description: >-
  Comptabilisez les dépenses d'IA à partir du flux de coûts, découpez-les selon
  n'importe quelle dimension d'attribution, prévoyez la période, et appliquez des
  budgets qui refusent la dépense au plafond — money-free sur le fil, opt-in et
  fail-open. Ce qu'il fait, et ses limites.
---

Le module XI est la couche **coûts / FinOps** pour l'IA : il comptabilise ce que les
connecteurs de modèles et de fournisseurs rapportent, vous laisse découper les dépenses selon
n'importe quelle dimension d'attribution, prévoit la période courante, et transforme un budget
en application réelle qui **refuse la dépense** au plafond plutôt que de seulement la signaler.
Cette page est la référence de ce que FinOps fait aujourd'hui et où s'arrêtent ses garanties.

## Ce que c'est

FinOps ne ré-implémente **pas** l'intégration des fournisseurs — il consomme le flux de coûts
des modèles/fournisseurs et **comptabilise ce que les connecteurs ont dérivé ou lu de façon
autoritaire**. L'argent est toujours une valeur entière en **micro-USD** (millionièmes de
dollar), jamais un float, de sorte que les totaux ne dérivent jamais. C'est un module de la
couche Intelligence : il possède l'ingestion, les budgets et l'analytique, et les expose via son
propre namespace d'API gaté par RBAC et ses vues UI sans toucher au cœur ni à ses voisins.

Le module est **minimal-data par construction** : il stocke des décomptes de tokens, des coûts
dérivés et des *références* d'attribution — jamais un prompt, une complétion, ou un secret. Le
coût est de la donnée de gouvernance, donc les lectures sont gatées par rôle au niveau de l'API,
et **aucun montant en USD n'est jamais exposé à un utilisateur final** (c'est une propriété du
fil, pas un réglage d'UI).

## Ses entités et son contrat

Chaque événement `cost.sampled` (un `CostSample` — voir le [bus d'événements](/fr/reference/events/))
est enregistré de deux manières :

- le **registre CostRecord** canonique et normalisé (une entité du cœur, indexée par id),
  **dé-dupliqué par une clé naturelle** — l'*identité* du bucket (provider / model / session /
  instant plus chaque dimension d'attribution et provenance), jamais sa *valeur* — de sorte qu'un
  bucket ouvert re-pullé ou un rapport réglé tardivement **fait un upsert en place** plutôt que de
  double-compter sur le flux at-least-once ;
- une ligne **read-model FinOps** dénormalisée indexée par les noms naturels d'attribution
  (provider, model, agent, session, team, project), de sorte que les dépenses s'agrègent
  efficacement selon **n'importe laquelle** de ces dimensions — y compris le `service_tier` du
  fournisseur.

Un **budget** est une `Policy` du cœur de kind `budget` : une dimension (global / model /
provider / agent / session / team / project), une limite, une période, et des seuils d'alerte.
Son `action` est l'une des trois — `alert` (showback-only, la valeur par défaut sûre qui
n'applique jamais), `throttle`, ou `block`. L'analytique sert la répartition des dépenses par
n'importe quelle dimension, les totaux, une série de tendance journalière, un run-rate et une
prévision de tendance de la période courante (avec une bande de confiance explicite), une vue
d'efficacité du prompt-cache, et des recommandations d'optimisation — chacune ancrée dans des
données enregistrées et **honnête sur ses hypothèses**.

## Ce qu'il consomme et produit

FinOps **consomme** `cost.sampled` depuis le [bus d'événements](/fr/reference/events/) et
**produit** deux effets. À l'ingest, lorsque la consommation franchit un seuil de budget qu'elle
n'a pas franchi durant cette période, il enregistre l'alerte et **émet un `FindingReport`**
(`finding.reported`) — le *signal seulement* ; la livraison vers Slack / SIEM / PagerDuty est le
travail du module connecteur de sortie, pas celui de FinOps.

Le second effet est l'**application**. Un budget dont l'`action` est `throttle` ou `block` refuse
la dépense au plafond via une **jonction `BudgetGate`** déclarée dans les propres termes de chaque
module agissant (le *fire* de l'orchestration, l'*open* de la voix, le *resolve* du routeur de
modèles) ; aucun module n'importe FinOps. Le gate s'exécute **orthogonalement au gate d'approbation**
— une action peut être approuvée par un humain et tout de même refusée par le budget — et répond sur
la dépense effective au plafond avec une **raison money-free** (pas d'USD, pas de nom de budget sur
la route en lecture seule). Un `block` dur refuse avec un **HTTP 402**, un `throttle` doux avec un
**HTTP 429**, et le refus est écrit dans le registre append-only et audité. Voir
[Gouverner et approuver](/fr/how-to/govern-and-approve/).

:::caution[Limites honnêtes]
- **L'application est opt-in, pas deny-closed par défaut.** Sans budget d'application qui couvre
  une requête, rien n'est jamais refusé — cette absence est l'état normal, pas une faille de
  sécurité. Seul un budget *définitivement* à sa limite refuse. C'est délibéré et l'inverse de la
  posture deny-closed du gate d'approbation.
- **Le gate fait fail open.** Une erreur de lecture FinOps ne fait jamais tomber une action en
  vol — un fire/open approuvé se poursuit et le routeur résout. Le filet de sécurité durable est
  le finding de budget-cap émis à l'ingest, pas le gate pré-flight.
- **Le routeur n'applique que les portées qu'il connaît avant exécution** (global / provider /
  model) ; les portées plus fines (agent, session, team, project) sont appliquées aux jonctions
  fire/open et à la passerelle de modèles, pas à la résolution de route.
- **FinOps comptabilise ; il ne facture pas.** Il enregistre ce que les connecteurs rapportent —
  la provenance `billed` vs `estimated` est portée, pas réconciliée en une facture — et un sample
  aux champs zéro/vides signifie *« non rapporté »*, jamais *« zéro »*.
- **Aucun actionnement au-delà du refus.** FinOps n'exécute ni un appel au modèle ni un mouvement
  d'argent ; il observe le flux de coûts et gate la dépense qu'il est configuré pour gater.
:::

## Liens connexes

- [Référence du bus d'événements](/fr/reference/events/) — les charges utiles `cost.sampled` / `CostSample` et `finding.reported`.
- [Catalogue des modules](/fr/reference/modules/overview/) — où se situe le module XI et son statut d'actionnement honnête.
- [Vue d'ensemble de l'architecture](/fr/explanation/architecture/overview/) — le moteur, les couches et le flux de coûts.
- [Gouverner et approuver](/fr/how-to/govern-and-approve/) — agir sur une action refusée par le budget.
- [Honnêteté et limites](/fr/start/honesty-and-limits/) — la politique de jonction deny-closed à travers les modules.
