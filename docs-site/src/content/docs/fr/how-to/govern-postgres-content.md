---
title: "Postgres comme source de contexte gouvernée"
description: "Connectez une base de données PostgreSQL comme source de connaissances gouvernée et en lecture seule : matérialisez les lignes sous forme de documents, transposez fidèlement les ACL, classifiez les colonnes sensibles et garantissez la lecture seule par construction."
---

Le connecteur de contenu `postgres` (`olivares.pg-content`) permet au plan de contrôle
d'accéder à une base de données PostgreSQL et de transformer ses lignes en **documents de
connaissances gouvernés**. Ceux-ci suivent le même pipeline que toutes les autres sources
de contenu — masquer → classifier → fragmenter → intégrer → indexer → servir via MCP —
avec des ACL propres à chaque document et une classification par colonne.

Il est le pendant, pour les bases de données opérationnelles, des sources de contenu
SaaS/data warehouse (gdrive, confluence, s3content, snowflake…). Deux choses qu'il
**n'est pas** :

- **Ce n'est pas `pgaudit`.** `pgaudit` observe les *arêtes d'accès* R/RW pour la carte
  d'accès ; il ne lit jamais le contenu des lignes. `pg-content` matérialise les *lignes
  sous forme de documents*. Ce sont des connecteurs différents pour des tâches
  différentes.
- **Ce n'est pas du NL-to-SQL.** Ce connecteur ingère les lignes comme contenu ; il ne
  génère **pas** de SQL à partir du langage naturel au moment d'une requête.
  (Certains concurrents appellent « base de connaissances avec données structurées »
  une fonctionnalité text-to-SQL : il s'agit d'une interface de requête pour agent,
  pas d'une source de contenu gouvernée. Ce connecteur est délibérément cette
  dernière.)

## Lecture seule par construction

Le connecteur n'écrit jamais dans votre base de données et applique cette contrainte sur
**trois couches indépendantes** afin qu'une écriture soit impossible, et pas seulement
découragée :

1. **Requêtes SELECT uniquement.** Le connecteur ne *construit* que des instructions
   `SELECT`. Si vous fournissez votre propre `query`, elle est validée comme une unique
   requête `SELECT`/`WITH` en lecture seule : une deuxième instruction, une CTE qui
   modifie des données (`WITH x AS (DELETE …)`), `COPY`, `SELECT … INTO` ou toute DDL est
   refusée dans `Open`, en mode fermé (fail-closed).
2. **Session en lecture seule.** Chaque instruction s'exécute dans une transaction
   `READ ONLY`, au sein d'une session ouverte avec
   `default_transaction_read_only = on`, de sorte que PostgreSQL lui-même refuse toute
   écriture. Dans `Open`, le connecteur *vérifie* que la session est en lecture seule et
   refuse de démarrer dans le cas contraire : c'est une garantie de posture, pas un
   conseil.
3. **Rôle de moindre privilège.** Vous attribuez au connecteur un rôle qui possède
   `SELECT` et rien d'autre. Consultez le rôle de référence ci-dessous.

Cette protection est plus forte que celle de tous les concurrents en mode géré, qui ne
présentent la lecture seule que comme un *conseil* dans leur documentation.

### Rôle de moindre privilège

```sql
CREATE ROLE olivares_ro LOGIN PASSWORD '…';
GRANT USAGE  ON SCHEMA public TO olivares_ro;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO olivares_ro;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT ON TABLES TO olivares_ro;
-- Never grant INSERT/UPDATE/DELETE/DDL. Optionally pin the role read-only:
ALTER ROLE olivares_ro SET default_transaction_read_only = on;
```

Pour limiter au maximum le périmètre, n'accordez `SELECT` que sur les tables que vous
souhaitez ingérer.

## Définir comment une ligne devient un document

La définition du document est déclarative : vous indiquez quelles colonnes constituent
la clé, le corps, le titre, l'ACL, la classification et le curseur de synchronisation :

```jsonc
// OLIVARES_SOURCES_CONFIG — document sources live under "documents"
{
  "documents": [
    {
      "name": "support-articles",
      "kind": "postgres",
      "config": {
        "mode": "live",
        "dsn": "vault:secret/data/pg-ro#dsn",   // secret-store REFERENCE, never inline
        "schema": "public",
        "table": "kb_articles",
        "key_columns": "id",                     // the stable document id
        "body_columns": "title,body",            // concatenated into the document body
        "title_column": "title",
        "updated_at_column": "updated_at",       // drives incremental (delta) sync
        "acl_columns": "owner_group",            // → ACL "group:<value>"
        "acl_prefix": "group:",
        "classification_column": "sensitivity",
        "sensitive_columns": "email,ssn",        // → external label "pii:<column>"
        "sensitive_label": "pii",
        "metadata_columns": "url_path",
        "sslmode": "require",
        "statement_timeout": "30s",
        "max_rows": "100000"
      }
    }
  ]
}
```

À la place d'une `table`, vous pouvez fournir une `query` en lecture seule (un `SELECT`
validé), par exemple pour joindre une table d'ACL ou filtrer les lignes à exposer.
Les informations d'authentification sont toujours une **référence vers un magasin de
secrets** (`vault:…`, `aws-secretsmanager:…`, …) ; un secret en clair est refusé.

## Transposition *fidèle* des ACL

Le connecteur transpose **uniquement ce qu'exprime la ligne**. Il construit l'ACL d'un
document à partir des valeurs des `acl_columns` déclarées (par exemple, la colonne
`owner_group` → `group:eng`). Il **n'invente pas** d'ACL propre à une ligne si la source
n'en contient aucune, et expose clairement les limites suivantes :

| Situation | Comportement du connecteur |
|---|---|
| Une colonne `owner_group` / rôle | Associe chaque valeur à une référence d'ACL (`<acl_prefix><value>`). |
| Aucun `acl_columns` déclaré | Le document hérite de l'**ACL par défaut** de la base de connaissances, toujours appliquée lors de la récupération. |
| **Row-level security (RLS)** sur la table | Respectée implicitement : le rôle du connecteur voit exactement les lignes que RLS l'autorise à voir. Le connecteur ne réimplémente pas RLS ; il en hérite. |
| Une permission que la table ne modélise **pas** dans une colonne | **Impossible à déduire** → non transposée. Modélisez-la dans une colonne (ou dans une table d'ACL jointe via `query`) pour qu'elle soit appliquée. |

Il s'agit de la différence délibérée par rapport aux concurrents en mode géré, qui vous
demandent de créer manuellement les colonnes d'ACL *et* ne transmettent pas RLS. Ici,
vous transposez également les colonnes d'ACL à la main, **mais** le connecteur respecte
en plus RLS et ne fabrique jamais une permission absente de la ligne.

## Classification par colonne

Répertoriez les colonnes sensibles dans `sensitive_columns`. Lorsqu'une ligne possède une
valeur dans l'une d'elles, le document reçoit une étiquette externe
`"<sensitive_label>:<column>"` (par exemple `pii:ssn`). Ces étiquettes alimentent la DLP
de récupération et sont appliquées en mode fermé (deny-closed), parallèlement à la
`classification_column` de la ligne.

## Live ou export

- **`mode: live`** lit la base de données à travers le pool en lecture seule et prend en
  charge la **synchronisation incrémentale (delta)** au moyen du curseur
  `updated_at_column`, avec une réconciliation de la liste complète comme solution de
  repli si aucun curseur n'est configuré.
- **`mode: export`** analyse un instantané statique des lignes (un dump JSON que vous
  produisez hors bande). Un instantané n'est **jamais présenté comme live** : la source
  signale fidèlement son mode.

## Limites déclarées

- Le **corps d'un document est limité à 1 Mio** ; une ligne plus volumineuse est tronquée
  (le streaming de très grandes colonnes est un suivi ultérieur).
- Dans une `query` fournie par l'opérateur, une **colonne portant littéralement le nom
  d'un mot-clé SQL** (par exemple `update`) doit avoir un alias : la garde de lecture
  seule échoue en mode fermé.
- Le connecteur lit du contenu ; **agir sur la base de données est hors périmètre**
  (aucun chemin d'écriture n'existe, par conception), tout comme le streaming CDC et le
  NL-to-SQL.

## Preuve en conditions réelles

Le connecteur fournit une preuve E2E (`-tags e2e`, CI) exécutée sur une véritable
instance PostgreSQL : elle vérifie la session en lecture seule dans `Open`, ingère des
lignes initiales avec les ACL/classifications transposées et prouve qu'une écriture sur
la session en lecture seule est **refusée** par PostgreSQL. Consultez
`connectors/pgcontent/testdata/docker-compose.e2e.yml`.
