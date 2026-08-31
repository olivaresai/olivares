---
title: "Vues de console enregistrées"
description: >-
  Instantanés nommés et partageables de l'état d'une vue de console — filtres,
  plages, scopes — stockés côté serveur par tenant. Enregistrez une enquête,
  partagez-la avec l'équipe. Ce que le module stocke, ses règles de propriété
  et de partage, ainsi que ses limites honnêtes.
---

Le module `consoleviews` fournit à la console des **vues enregistrées** : un
instantané nommé de l'état d'une vue — les mêmes filtres, plages de temps et
scopes que la console encode dans l'URL — stocké **côté serveur par tenant**.
Ainsi, une enquête comme *« admissions refusées, dernières 24 h »* survit au
navigateur, suit l'opérateur d'une machine à l'autre et, lorsqu'elle est
partagée, reste accessible en un clic à toute l'équipe.

## Ce qu'il stocke — et ce qu'il ne stocke jamais

Une vue enregistrée ne contient **que des paramètres** : un objet JSON dont la
taille est plafonnée (4 Ko maximum) et qui contient l'état URL de la vue, ainsi
qu'un nom, une description facultative, le principal propriétaire et un flag
`shared`. Le module ne stocke **jamais de résultats de requête, de lignes du
ledger, ni aucune donnée que les paramètres sélectionneraient** — charger une
vue enregistrée réexécute la requête sous-jacente avec les propres permissions
de l'appelant. La console traite strictement les paramètres stockés comme des
données.

## Propriété, partage et permissions

- **Créer/mettre à jour** — tout membre disposant de
  `consoleviews:view:write` (niveau éditeur). Une vue appartient au principal
  qui l'a créée ; seul son propriétaire peut la modifier.
- **Visibilité** — le propriétaire voit toujours ses propres vues ; une vue
  marquée `shared` est visible par chaque membre du tenant disposant de
  `consoleviews:view:read` (niveau lecteur). Une vue que vous n'êtes pas
  autorisé à voir répond `404`, jamais `403` — la vérification de visibilité
  ne révèle pas son existence.
- **Supprimer** — le propriétaire, ou un rôle **admin/owner** du tenant pour
  n'importe quelle vue (afin de nettoyer les vues laissées par des utilisateurs
  partis).
- **Plafonds** — 200 vues par propriétaire, 2 000 par tenant ; une fois le
  plafond atteint, l'opération est refusée avec un message explicite.
  `(feature, owner, name)` est une clé naturelle : enregistrer un nom en double
  pour la même feature répond `409`.

Chaque création, mise à jour et suppression est consignée dans l'audit ledger
du tenant et attribuée au véritable principal — les métadonnées consignées
identifient la vue (feature, nom, flag `shared`), jamais ses paramètres.

## Routes

| Méthode | Route | Permission |
|---|---|---|
| `GET` | `/v1/m/consoleviews/views?feature_id=` | `consoleviews:view:read` |
| `GET` | `/v1/m/consoleviews/views/{id}` | `consoleviews:view:read` |
| `POST` | `/v1/m/consoleviews/views` | `consoleviews:view:write` |
| `PUT` | `/v1/m/consoleviews/views/{id}` | `consoleviews:view:write` |
| `DELETE` | `/v1/m/consoleviews/views/{id}` | `consoleviews:view:write` |

Les routes de module font partie de la surface **bêta** — consultez la
[référence des routes de module](/reference/api-beta/).

## Limites honnêtes

- Le serveur valide le `feature_id` d'une vue comme un slug, mais ne fixe
  **pas** la liste des features de la console — le registre de la console fait
  autorité et change à chaque release ; la console ignore les vues
  enregistrées des features qu'elle ne possède plus.
- Une vue partagée partage des **paramètres**, pas des résultats : deux
  opérateurs chargeant la même vue peuvent voir des données différentes si
  leurs permissions diffèrent. C'est intentionnel — le partage n'élargit
  jamais l'accès.
- Les vues enregistrées sont du mobilier de console, pas de la preuve : elles
  vivent hors de la chaîne du ledger (seuls les événements de leur cycle de
  vie sont mis en preuve).
- Un opérateur **confiné à un workspace** peut lire les vues enregistrées mais
  ne peut pas les créer, les modifier ou les supprimer : le moteur de grants
  scopés interdit les écritures au niveau collection pour les principals
  confinés (fail-closed), et l'override de suppression admin à l'échelle du
  tenant exclut explicitement les admins confinés.
- Sous Postgres, les plafonds par propriétaire/tenant sont souples en cas
  d'écritures concurrentes (dépassement marginal borné) ; les noms en double
  sont toujours strictement refusés.
