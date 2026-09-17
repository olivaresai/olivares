---
title: "Module I — inventaire et découverte"
description: >-
  Découverte passive et catalogage des agents, sessions, serveurs MCP, skills,
  outils, modèles, fournisseurs et identités non humaines observés dans le parc
  (estate). Comment les entités sont matérialisées à partir des observations, ce
  que le catalogue et la provenance enregistrent, comment fonctionne la fraîcheur
  durable, et les limites.
---

Le module I est le **catalogue des entités observées** du parc (estate) : un
inventaire passif, piloté par le bus, des agents, sessions, instances de
Claude Code, serveurs MCP, skills, outils, ressources, modèles, fournisseurs
et identités non humaines que les connecteurs ont effectivement nommés. Il
découvre en *écoutant*, jamais en sondant. Il enregistre des relations, des
identifiants et la vivacité — pas de charges utiles — et ce n'est **pas** un
recensement de tout ce qui existe. Cette page est la référence de ce que le
catalogue contient, de la façon dont la provenance d'observation et la
fraîcheur durable fonctionnent dans le développement de la prochaine version, et de
ce que le module ne prétend délibérément pas.

La provenance d'observation et la fraîcheur durable sont acceptées pour une
composition de développement bornée. Ce sont des capacités actuelles de
développement pour la prochaine version, non une release publiée, un RC complet ou un
déploiement.

## Ce qu'il matérialise

Les connecteurs émettent des **observations**, pas des entités. Ils publient
sur le bus d'événements des faits normalisés
[`edge.observed`](/fr/reference/events/) et
[`cost.sampled`](/fr/reference/events/) ; les entités qu'ils impliquent ne
sont jamais envoyées. Le module I **matérialise** l'entité de cœur que chaque
observation nomme à partir de sa référence naturelle : une `session`/un
`agent`/une `identity` d'origine, un serveur MCP, un outil, une ressource, un
skill, et — à partir des échantillons de coût — un fournisseur et un modèle
(découverts, **sans tarification** ; cela appartient à FinOps). L'inventaire
s'abonne à ces deux types d'événements ; les constats en direct appartiennent
au [module II](/fr/reference/modules/ii-sessions/).

Le find-or-create de l'entité de cœur sur la clé naturelle évite de dupliquer
l'alias de catalogue sous une livraison « au moins une fois » dans le modèle
actuel à un seul rédacteur. Deux sources peuvent conserver des observations
distinctes qui partagent cet alias ; le partager ne prouve pas qu'il s'agit
de la même chose physique et ne transfère ni propriétaire, ni workspace, ni
grants. Un nouvel identifiant d'événement avec la même charge utile est un
reçu **nouveau** — savoir s'il s'agit d'une autre activité reste inconnu. Le
`occurrence_count` du catalogue compte les **livraisons**, y compris un
rejeu du même identifiant d'événement, pas des activités distinctes.

## Provenance d'observation

Outre l'alias de catalogue, le module stocke une projection additive, à
portée de tenant, de la façon dont une observation est arrivée : un reçu
indexé par identifiant d'événement, une ligne membre par entité matérialisée
(référence native et, lorsque l'instantané d'enregistrement est valide, une
identité observationnelle stable) et une ligne de conflit lorsque le même
identifiant porte ensuite des faits projetés différents. Le reçu d'origine
est conservé ; le conflit est enregistré plutôt qu'écrasé. Un identifiant
absent n'obtient qu'une identité de stockage et n'est jamais dédupliqué par
charge utile. Le rejeu du même identifiant et des mêmes faits rafraîchit le
compteur héritage des livraisons du catalogue sans résoudre à nouveau les
alias. La console peut lister les reçus stockés d'une entité de catalogue à
`GET /v1/m/inventory/entities/{kind}/{id}/observations` avec la permission
tenant-wide existante `inventory:catalog:read` : un item par reçu distinct
en ordre croissant d'identifiant de reçu, pages de 25 au plus. Chaque item
est un instantané historique d'enregistrement à la réception (pas
l'enregistrement ou la santé actuels de la source), l'instant d'occurrence
déclaré par la source lorsqu'il l'est, la première et la dernière réception
des faits conservés, les livraisons à faits coïncidents, et si une
relivraison conflictuelle est retenue. Une page vide ne prouve pas que
l'entité n'a jamais été observée. `has_more`, une liste vide et le total du
catalogue ne sont pas une couverture. La fraîcheur du catalogue sur la fiche
d'entité vient d'un point read courant réussi ; c'est une lecture distincte
de l'historique. La couverture par source/famille et l'estate de référence
C4 restent ouverts.

## Son contrat et ses entités

Le module enregistre `inventory.catalog_entry` — une surcouche de découverte
attachée à chaque entité de cœur matérialisée. Elle enregistre *comment* une
chose a été trouvée, et non *ce qu'elle* a fait : sources de signal, hôtes
lorsqu'ils sont connus, horodatages de première et dernière observation, un
`occurred_at` optionnel déclaré par la source (omis si elle n'en a déclaré
aucun ; distinct de `last_seen`, qui est le moment où **cette plateforme**
l'a vue), un compteur d'occurrences et un `status` de vivacité `active` ou
`stale`. La surface de lecture est restreinte et en lecture seule : un
`summary` comptant par kind et par source, une liste `entities` paginée
filtrable par kind et par statut, une vue de détail d'une entité unique et
l'historique d'observations par entité ci-dessus.
Chaque lecture exige une permission de lecture à portée de tenant et placée
sous espace de noms (le palier viewer le plus bas suffit). Les écritures de
catalogue et de provenance sont à haute fréquence et ne sont pas auditées par
écriture.

La fraîcheur durable tient une seconde surcouche,
`inventory.freshness_sweep` : au plus une ligne créée paresseusement par
tenant, portant la coupure d'un cycle ouvert, le curseur opaque du catalogue
et la dernière coupure dont le cycle s'est terminé. Cette dernière coupure
enregistre un **balayage** terminé, non qu'une source ait été entièrement
énumérée. Les formes complètes vivent dans la
[référence du bus d'événements](/fr/reference/events/) et dans les
interfaces typées du produit.

## Fraîcheur durable

Un balayage périodique marque une entrée du catalogue `stale` lorsque cette
plateforme ne l'a pas vue depuis la coupure du cycle, et la repasse à
`active` dès qu'elle réapparaît. Les seuils par défaut sont 30 minutes de
silence et une cadence de 5 minutes ; ce sont des défauts du module, pas une
surface YAML d'opérateur dans le binaire actuel (la racine de composition
enregistre le module avec des réglages d'hôte vides).

Le module n'énumère **pas** les tenants et ne se rabat **pas** sur les
tenants que ce processus a pu observer. Un adaptateur privé de composition
lit l'annuaire durable des organisations et ne renvoie que des
**candidats** : les tenants métier actifs que la résidence de cette instance
sert — jamais la partition système, jamais une organisation suspendue, jamais
un tenant épinglé à une région que cette instance ne sert pas. Chaque tour
ouvre toujours l'écriture store ordinaire à portée de tenant, de sorte que
résidence, retrait de service et leadership sont revérifiés ; un tenant dont
l'état a changé entre l'instantané et son tour y est refusé.

Sans un annuaire faisant autorité, le balayage échoue de façon visible et ne
mute rien. Sous PostgreSQL, cette autorité exige le pool de lecture admin
`NOSUPERUSER` `BYPASSRLS` déjà utilisé pour les lectures d'annuaire
inter-tenants (`--admin-dsn`). Sans ce pool attesté, le balayage ne traite
pas les lignes visibles du rôle applicatif comme un annuaire complet. SQLite
n'a pas d'équivalent. Les nouvelles observations continuent d'être
persistées lorsque le handle de données est câblé ; un balayage qui ne peut
pas s'exécuter ne désabonne pas l'ingestion.

Chaque passe donne à chaque candidat **un tour**. Chaque tour classe au plus
une page de 1 000 entrées actives plus anciennes que la coupure et enregistre
le curseur pour qu'un redémarrage continue sans nouvel événement.
L'énumération et chaque tour de tenant ont des **budgets de temps séparés**,
de sorte qu'un tenant lent ne consomme pas le reste de la passe. Un échec
local n'empêche pas les tenants suivants d'obtenir un tour **dans un
processus vivant**. Il n'y a pas de curseur global de tenants, ni d'équité
sous redémarrages répétés, ni d'affirmation de haute disponibilité.

Si un tour ne se termine pas avec succès, cette page n'est pas comptée comme
marquée. Page et progression restent ensemble ; le tour suivant relit ce qui
a été stocké en dernier — la frontière précédente, ou une déjà avancée — et
continue à partir de là. Le produit ne reconstruit pas la progression depuis
la mémoire.

## Ce qu'il consomme et produit

Le module I est un **consommateur**. Il s'abonne à `edge.observed` et
`cost.sampled` et écrit sa surcouche de catalogue, les entités de cœur qu'il
en dérive, et les lignes additives de provenance et de fraîcheur ci-dessus.
Il n'émet aucun événement propre et n'expose aucune surface d'actionnement.
Les références et les faits d'observation sélectionnés qu'il persiste sont
stockés tels qu'ils arrivent ; le module ne les assainit pas davantage. La
minimisation des données incombe au producteur et doit être résolue avant la
publication des observations ; ce module ne peut pas la certifier pour tous
les producteurs. Une référence persistée peut conserver une query, des
identifiants ou d'autres données sensibles si un producteur les a publiés.
L'inventaire ne collecte pas de son propre chef le contenu brut complet de
l'activité, et n'ajoute aucune charge utile, secret, prompt, commande ou SQL
bruts qui lui soient propres.

:::caution[Limites honnêtes]
- **L'inventaire ne possède pas le graphe d'accès.** À compter de la
  décision A (2026-06-03), le module III (la carte d'accès) est le **seul
  rédacteur** de l'`AccessEdge` en lecture/écriture et le seul propriétaire
  de la topologie et du diff Permis-vs-Observé. L'inventaire découvre et
  catalogue les *entités* qu'une arête nomme ; il n'enregistre plus l'arête
  elle-même, et ne sert aucune route de topologie. Le graphe n'est peuplé
  que lorsque le module III est câblé au démarrage.
- **La découverte n'est aussi complète que les signaux.** Une entité
  n'existe dans le catalogue que si un connecteur l'a observée. L'absence du
  catalogue n'est **pas** une preuve d'absence dans le parc. Terminer un
  balayage de fraîcheur n'est pas une couverture de source, une complétude
  de la découverte, une santé formelle ni la preuve qu'une entité a été
  retirée.
- **La vivacité est la péremption, pas la santé.** `stale` signifie que
  cette plateforme n'a pas observé l'entité depuis la coupure, rien de plus.
  La réapparition la ramène à `active`. Le silence d'une session est normal,
  et la santé/le SLA formels relèvent du module XXII. Le balayage ne mute
  jamais le cycle de vie propre de l'entité de cœur.
- **Aucun détail fabriqué.** Le module ne stocke que des identifiants, des
  relations et des compteurs de vivacité — jamais le contenu brut complet de
  l'activité collecté de son propre chef — et n'ajoute ni charges utiles, ni
  secrets, ni prompts, ni commandes, ni SQL qui lui soient propres. Les URI
  de ressource reçues peuvent être stockées comme références. L'inventaire
  ne ré-assainit pas ces valeurs et ne certifie pas l'absence de chaînes de
  requête, d'identifiants ou de données personnelles à l'intérieur.
:::

## En lien

- [Catalogue des modules](/fr/reference/modules/overview/) — où se situe le module I et la
  répartition honnête de l'actionnement (Actuate).
- [Module III — la carte d'accès](/fr/reference/modules/iii-access-map/) — le seul propriétaire
  du graphe R/RW et de la dérive.
- [Référence du bus d'événements](/fr/reference/events/) — les événements `edge.observed`,
  `cost.sampled` et `finding.reported` que consomment l'inventaire et les sessions.
- [De zéro au graphe](/fr/tutorials/zero-to-graph/) — peupler le catalogue et la carte sur le
  parc de démonstration.
- [Vue d'ensemble de l'architecture](/fr/explanation/architecture/overview/) — le moteur, les
  couches et le bus.
