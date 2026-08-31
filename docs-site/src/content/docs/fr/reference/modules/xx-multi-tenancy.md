---
title: "Module XX — multi-tenancy et gestion des organisations"
description: >-
  Le socle d'isolation : chaque entité du cœur porte un tenant_id, et le store
  refuse de s'ouvrir tant que cette frontière n'est pas appliquée au niveau de la
  couche de requêtes. Ce que le modèle de données garantit aujourd'hui, et ce que
  la hiérarchie d'organisations et l'administration déléguée restent encore.
---

Le module XX n'est pas un service greffé sur le moteur — c'est une **propriété du
moteur lui-même**. Il n'existe pas de module de tenancy distinct à attacher ; au
contraire, le modèle de données du cœur porte une frontière de tenant sur chaque
entité et le store l'applique sous chaque requête. Cette page est la référence de ce
que cette frontière garantit aujourd'hui, et des parties de la gestion des
organisations qui en sont encore au stade de la conception.

## Ce que c'est

La multi-tenancy vit dans la couche Engine (couche 0), aux côtés de l'API propre à la
plateforme (module XIX), car réintégrer l'isolation dans un modèle de données déjà en
service est précisément le genre de changement qu'on ne peut pas faire en sécurité plus
tard. Chaque entité du cœur porte un **`tenant_id`**, et un appelant n'en passe jamais
un comme paramètre libre : il **fixe le tenant une seule fois** et reçoit un scope dont
les dépôts sont déjà liés à celui-ci. Il **n'existe aucun vocabulaire dans l'API pour
traverser les tenants** — cette absence est la première barrière d'isolation, avant tout
mécanisme de base de données. Le scope privilégié inter-tenants (créer une organisation,
lister les organisations, supprimer un tenant) n'est accessible **que par le démarrage
propre du moteur**, jamais par un module.

## Le contrat et les entités

Le modèle de tenant appartient au contrat du modèle de données, non à un schéma propre
à chaque module. L'entité racine est l'**`Org`**, qui *est* le tenant : lorsque le
moteur amorce une organisation, son identifiant devient l'identifiant du tenant et la
chaîne d'audit propre à l'organisation est établie au même moment. Toute autre entité du
cœur — agents, sessions, ressources, identités, politiques, enregistrements de coûts,
findings, déploiements, l'access map et l'audit ledger — est créée **à l'intérieur** d'un
scope de tenant et estampillée avec ce tenant à l'écriture ; l'appelant ne peut pas le
surcharger.

L'isolation est appliquée au niveau de la couche de requêtes, selon le déploiement :

- Sur **PostgreSQL**, chaque table portant `tenant_id` s'exécute sous `FORCE ROW LEVEL
  SECURITY` avec une politique `tenant_isolation` liée par transaction. Une transaction
  qui ne parvient pas à lier un tenant **lève une erreur** plutôt que de renvoyer
  silencieusement zéro ligne (fail-closed). Le rôle applicatif est non-superutilisateur
  et n'a jamais `BYPASSRLS`, et `FORCE` lie la politique même pour le propriétaire de la
  table. La **propriété**, elle, est un choix de déploiement : l'installation mono-rôle
  par défaut laisse le rôle applicatif propriétaire de la base — la RLS le lie toujours,
  mais un propriétaire peut altérer ses propres tables, de sorte que cette posture est
  capable de *détecter les altérations* plutôt qu'à l'épreuve du propriétaire. La frontière dure de privilège
  — un rôle applicatif qui est *aussi* non-propriétaire — vient de la topologie scindée
  propriétaire/application, où un rôle propriétaire distinct exécute le provisionnement
  et le rôle applicatif ne reçoit que le DML dont il a besoin.
- Sur **SQLite** (le déploiement mononœud) il n'y a pas de sécurité au niveau ligne ;
  l'équivalence vient de deux faits — le *seul* chemin vers la base est le SQL généré par
  descripteur qui ajoute toujours le prédicat de tenant, et des **déclencheurs
  tripwire** annulent toute écriture dont le tenant ne correspond pas au scope épinglé.

Un **auto-test de démarrage** interroge les garde-fous d'isolation en vigueur après la
migration et **refuse d'ouvrir** le store si une table portant `tenant_id` n'est pas
protégée — de sorte qu'un garde-fou oublié sur une nouvelle table devient un échec de
démarrage, et non une fuite silencieuse.

## Ce qu'il consomme et produit

Le module XX n'a aucune surface de bus d'événements ni aucune actuation. Il ne consomme
pas `edge.observed`, n'émet pas de findings et n'appelle aucun fournisseur — c'est le
substrat *à travers* lequel les autres modules écrivent. Son seul effet observable est
structurel : chaque entité qu'un module persiste est déjà scopée par tenant, et chaque
mutation sur une entité auditée s'ajoute à l'[audit ledger chaîné par hash](/fr/reference/events/)
de ce tenant au sein de la même transaction.

:::caution[Limites honnêtes]
- **Ce que le modèle de données modélise réellement, c'est `Org`-en-tant-que-tenant + la
  frontière d'isolation** — pas la hiérarchie complète d'organisations. **Les équipes,
  projets, administration déléguée, rôles par niveau et l'usage/facturation par
  organisation sont au stade de la conception**, et non des entités livrées. Considérez la
  garantie de tenancy du produit aujourd'hui comme : *une organisation = un tenant isolé,
  appliqué au niveau de la couche de requêtes.*
- **L'isolation en lecture sur SQLite repose sur la couche de requêtes, pas sur le
  moteur.** SQLite n'a pas de sécurité au niveau ligne : le scoping en lecture est une
  propriété du SQL généré (les écritures sont aussi couvertes par les déclencheurs
  tripwire). La multi-tenancy **à l'échelle, c'est PostgreSQL avec RLS** comme filet de
  sécurité au niveau noyau ; SQLite est le déploiement mononœud / air-gapped.
- **Le scope d'administration inter-tenants dépend du déploiement sur PostgreSQL.** Lister
  les organisations à travers les tenants nécessite un rôle d'administration dédié sur
  PostgreSQL et relève du déploiement, non du code applicatif. Cela fonctionne directement
  sur SQLite (écrivain unique).
- **La tenancy n'est pas l'administration déléguée.** Qui peut agir *au sein* d'un tenant
  — rôles, approbations, séparation des tâches — est régi par le
  [module VI](/fr/reference/modules/vi-governance/), pas ici. Le module XX garantit le mur
  entre les tenants ; le module VI garde la porte à l'intérieur de l'un d'eux.
:::

## Liens connexes

- [Catalogue des modules](/fr/reference/modules/overview/) — où se situe le module XX et son statut d'actuation honnête.
- [Identité, permissions et gouvernance](/fr/reference/modules/vi-governance/) — rôles et autorité déléguée au sein d'un tenant.
- [Vue d'ensemble de l'architecture](/fr/explanation/architecture/overview/) — la couche moteur et le modèle de données général.
- [Référence du bus d'événements](/fr/reference/events/) — l'audit ledger par tenant auquel s'ajoute chaque mutation.
- [Honnêteté et limites](/fr/start/honesty-and-limits/) — ce qui est construit aujourd'hui face à ce qui est au stade de la conception.
