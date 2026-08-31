---
title: "PostgreSQL pgAudit (palier clean R/RW)"
description: >-
  Capturez les accès en lecture/écriture à PostgreSQL depuis sa piste d'audit
  native pgAudit — le signal du palier clean : READ/WRITE pris textuellement
  depuis la CLASS d'audit, jamais inféré du SQL, le connecteur ne lisant que le
  fichier de log.
sidebar:
  order: 1
---

La source `pgaudit` transforme la piste d'audit propre à PostgreSQL en arêtes
de l'access map : une arête par accès aux données audité, avec le mode
lecture/écriture pris **textuellement depuis le champ CLASS de pgAudit** —
jamais inféré du texte SQL. C'est la source canonique du **palier clean** : un
store objet/relationnel qui classifie les accès dans sa piste native.

Le connecteur est **en lecture seule sur un fichier de log**. Il ne se connecte
jamais à la base de données, ne voit jamais les résultats de requêtes et ne
capture jamais le corps SQL — l'identité, l'objet et la classification sont
toutes la sortie propre de pgAudit.

## Ce qu'il émet

| Champ | Valeur |
|---|---|
| Source de signal | `pg_audit` |
| Mode | depuis CLASS, textuellement : READ → `read`, WRITE → `write`, DDL → `write` (une écriture de schéma), FUNCTION → `unknown` (pgAudit ne le dit pas) ; ROLE/MISC sont ignorés, non devinés |
| Origine | l'`application_name` s'il est présent (→ `attributed`), sinon le rôle de session |
| Confiance | `attributed`, ou `approximate` pour les rôles/applications que vous déclarez partagés |
| Palier de couverture | clean |

## 1. Activer pgAudit, les logs structurés, UTC

Côté PostgreSQL (la configuration standard de pgAudit — voir la documentation
pgAudit de votre version majeure) :

```ini
# postgresql.conf
shared_preload_libraries = 'pgaudit'
pgaudit.log = 'read, write'        # the classes this source consumes
logging_collector = on
log_destination = 'csvlog'         # or 'jsonlog' (PostgreSQL 15+)
log_timezone = 'UTC'               # REQUIRED — see below
```

Deux contraintes découlent de la manière dont le connecteur analyse les
données, toutes deux vérifiées par rapport à son implémentation :

- **Le serveur doit journaliser en UTC.** PostgreSQL écrit les horodatages avec
  une *abréviation* de fuseau, et une abréviation non UTC ne peut pas être
  résolue de manière fiable en un décalage — le connecteur **ignore** donc ces
  enregistrements plutôt que de deviner un mauvais horodatage.
  `log_timezone = 'UTC'` est la configuration prise en charge.
- **`csvlog` est par lots ; `jsonlog` peut être suivi.** Les enregistrements
  csvlog peuvent s'étendre sur plusieurs lignes, donc ce format est lu par lots
  à chaque passe ; `jsonlog` est délimité par lignes et prend en charge le
  tailing continu (`follow`, la valeur par défaut).

Pour rendre l'attribution nette, faites en sorte que les applications
définissent `application_name` par agent — c'est ce qui fait passer une arête
d'un rôle partagé à une origine attribuée (voir
[la dépendance d'identité](/fr/how-to/connect-a-source/#la-dépendance-dure--lidentité-par-agent)).

## 2. Déclarer la source

Dans votre [config des sources](/fr/how-to/connect-a-source/#câbler-une-véritable-source)
(`OLIVARES_SOURCES_CONFIG`) :

```json
{
  "sources": [{
    "name": "salesdb-pgaudit",
    "kind": "pgaudit",
    "tenant": "<tenant-id>",
    "config": {
      "log_path": "/var/log/postgresql/postgresql.csv",
      "format": "csvlog",
      "shared_accounts": "etl_role,app_pool"
    }
  }]
}
```

Clés de configuration (issues du descripteur livré avec le connecteur) :

| Clé | Requise | Défaut | Signification |
|---|---|---|---|
| `log_path` | oui | — | chemin du fichier de log PostgreSQL que l'hôte du moteur peut lire |
| `format` | non | `csvlog` | `csvlog` ou `jsonlog` |
| `follow` | non | `true` | tailing continu (**jsonlog uniquement** — csvlog est par lots) |
| `shared_accounts` | non | — | rôles / application_names partagés, séparés par des virgules ; leurs arêtes sont honnêtement marquées `approximate` |

Redémarrez le moteur et confirmez la ligne de démarrage
`ingest: wired source … kind=pgaudit`.

## 3. Ce que vous verrez dans la console

Ouvrez l'**Access map**. Chaque accès audité se rend comme une arête du rôle ou
de l'application vers la table, colorée en lecture ou écriture, avec le badge de
couverture `CLEAN` sur les ressources Postgres. Le panneau **Permitted vs
observed** fait remonter tout accès sans grant correspondant — avec pgAudit
câblé et aucun grant encore déclaré, *chaque* accès observé est une dérive
honnête, ce qui est le premier état attendu.

## Limites honnêtes

- **Il voit ce que pgAudit journalise.** Les classes que vous n'activez pas
  (`pgaudit.log`) ne sont pas observées ; une absence d'arêtes n'est pas une
  preuve d'absence d'accès si la classe est désactivée.
- **L'attribution est celle de la base de données.** Un rôle partagé sans
  `application_name` fait s'effondrer les appelants sur une seule identité —
  déclarez-le dans `shared_accounts` pour que la map dise `approximate` au lieu
  de prétendre.
- **FUNCTION est `unknown` par conception** — l'exécution d'une fonction peut
  lire ou écrire, et pgAudit ne dit pas laquelle ; le produit ne forcera pas
  une étiquette. Les classes non liées aux données (ROLE, MISC) sont ignorées
  plutôt qu'émises comme des arêtes dénuées de sens.

## Voir aussi

- [Connecter une source](/fr/how-to/connect-a-source/) — le modèle de connecteur
  et la taxonomie des paliers honnêtes.
- [CloudTrail](/fr/how-to/connectors/cloudtrail/) — la même idée de palier clean
  pour les objets S3.
- [Connecteurs & paliers de couverture](/fr/reference/connectors/) — le catalogue
  complet.
