---
title: "Module XXI — tableaux de bord exécutifs et reporting"
description: >-
  La vue de direction sur le control plane : coût, usage, risque, conformité et
  fiabilité agrégés depuis les modules qui détiennent les calculs, soumis au même RBAC
  que les vues techniques, avec export PDF à la demande. Ce qu'il présente, ce qu'il ne
  calcule jamais, et ses limites honnêtes.
---

Le module XXI est la surface de direction de la **couche Web** (couche 4) : une lecture
de haut niveau de l'estate — dépenses, usage, posture de risque, couverture de conformité
et fiabilité — placée aux côtés de l'UI technique par module. Il **agrège et présente ; il
ne recalcule jamais** (les modules détiennent chaque chiffre), et il hérite du même
scoping par tenant et du même RBAC que les vues qu'il résume.

## Ce que c'est

Deux surfaces en lecture seule composent ce module :

- le **tableau de bord exécutif** (`/dashboards`) — l'agrégation transversale complète,
  avec une plage de coût sélectionnable (7 j / 30 j / 90 j / cumul du mois), une
  ventilation des dépenses par équipe, projet, agent, modèle ou fournisseur, et une page
  de garde de rapport imprimable ;
- la **vue d'accueil** (`/`) — une porte d'entrée volontairement plus légère : une seule
  grille de piliers de l'estate (inventaire, sessions en direct, sécurité, conformité,
  rythme de dépense, santé/SLA), chacun étant un lien d'approfondissement vers son module.

La vue d'accueil réutilise les hooks de lecture, les agrégations pures et les primitives
de tuiles du tableau de bord plutôt que de les dupliquer, et partage le même cache de
requêtes scopé par tenant, de sorte que la porte d'entrée reste légère (moins de requêtes)
tout en restant cohérente avec la vue approfondie.

## Ce qu'il présente (et ce qu'il ne calcule jamais)

Le tableau de bord met en avant des KPI répartis sur cinq piliers — **coût** (FinOps XI +
Modèles X), **usage** (Inventaire I + Sessions II), **risque** (Sécurité IX + Red-teaming
XVIII + Access map III), **conformité** (XIII) et **fiabilité** (Santé et SLA XXII). La
couche d'agrégation est un ensemble de **fonctions pures** qui se contentent de compter,
sommer et classer ce que les modules ont déjà décidé : le coût reste dans les unités
entières des modules, et la sévérité d'un finding, le score de red-team, le statut d'un
contrôle et l'état de santé sont transmis tels quels.

Parce qu'il ne détient aucun calcul, il ne peut pas blanchir le seam d'honnêteté d'une
source, et il ne le fait pas : un agrégat `truncated` reste signalé comme un plancher ;
une exécution de red-team qui n'a pas pu mener à bien ses sondes n'est **jamais** comptée
comme une réussite ; un accès observé avec une couverture approximative ou opaque est
présenté comme une borne inférieure ; la conformité se lit comme une **couverture de
contrôles**, jamais comme une affirmation « conforme », et conserve sa clause de
non-responsabilité permanente ; un sujet de santé sans aucune vérification se lit
`unknown`, et non sain.

## Export et le fil

L'export est **à la demande, côté client** : le tableau de bord imprime ce qui est à
l'écran via la fonction Enregistrer en PDF du navigateur (`window.print()`), avec une page
de garde de rapport propre à l'impression (organisation, plage, heure de génération) et un
pied de page de clause de non-responsabilité permanente. C'est fidèle au RBAC et au scoping
par tenant **par construction** — le rapport ne peut jamais contenir que les sections que
le rôle a réellement rendues. Le document exporté, tout comme le tableau de bord lui-même,
ne porte que des **KPI agrégés — aucun payload, aucun secret** : la donnée minimale est une
propriété de ce qui traverse le fil, non une promesse sur le bon comportement d'un lecteur.

## Actuation

Le module XXI **n'a aucune surface d'actuation** (`—` dans le [catalogue des modules](/fr/reference/modules/overview/)).
C'est une couche de présentation au-dessus d'endpoints en lecture que les modules servent
déjà ; il n'émet aucune écriture, ne déclenche rien et ne dispatche rien.

:::caution[Limites honnêtes]
- **Aucun rapport planifié ou livré.** L'intention de conception du catalogue inclut des
  rapports planifiés et exportables ; ce qui est livré aujourd'hui est **uniquement
  l'impression en PDF côté client à la demande**. Il n'y a pas d'endpoint de reporting
  côté serveur, pas de planification récurrente et pas de livraison par e-mail — n'attendez
  pas qu'un rapport arrive de lui-même.
- **Il n'est honnête que dans la mesure où ses sources le sont.** Chaque lacune de
  couverture, troncature, attribution en attente et clause de non-responsabilité provient
  des modules sous-jacents et est affichée, non lissée ; un chiffre faible peut signifier
  un risque faible *ou* une couverture limitée. Lisez chaque pilier avec les limites de son
  module (p. ex. les niveaux de couverture de l'access map).
- **Le RBAC contrôle chaque pilier.** Un rôle qui ne peut pas lire une source ne voit
  jamais son KPI et ne peut pas l'imprimer. Un lecteur sans aucune source autorisée voit un
  état vide honnête, et non un tableau de bord fabriqué.
- **Instantané à un instant donné, source unique.** Le risque, la conformité et la
  fiabilité sont des instantanés de l'état courant ; seul le coût couvre la plage
  sélectionnée. La vue est une agrégation des données propres à ce control plane, et non un
  outil de BI externe.
:::

## Liens connexes

- [Catalogue des modules](/fr/reference/modules/overview/) — les couches, et la séparation Gouverner/Actuer.
- [Module XI — Coût et AI FinOps](/fr/reference/modules/xi-finops/) — les chiffres de dépense qu'il agrège.
- [Module XIII — Conformité](/fr/reference/modules/xiii-compliance/) — la couverture de contrôles, jamais une affirmation de conformité.
- [Module III — access et resource map](/fr/reference/modules/iii-access-map/) — le drift derrière le pilier de risque.
- [Vue d'ensemble de l'architecture](/fr/explanation/architecture/overview/) — où se situe la couche Web.
- [Honnêteté et limites](/fr/start/honesty-and-limits/) — comment le produit énonce ce qu'il fait et ne fait pas.
