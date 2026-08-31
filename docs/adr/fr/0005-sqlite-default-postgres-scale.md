> Traduction automatique. La version anglaise fait foi.

# ADR-0005: SQLite embarqué par défaut, Postgres + RLS pour la montée en charge

- **Status:** accepted
- **Date:** 2026-06
- **Deciders:** Fran Olivares
- **References:** registre des décisions de stack (T4) ; conception du modèle de données

## Contexte et énoncé du problème

Le control plane stocke un modèle de données multi-tenant (le graphe d'accès en est une
*vue*). Il doit fonctionner comme un binaire unique sans dépendances pour les petites
installations / air-gapped, tout en montant en charge vers des déploiements multi-hôtes et
multi-tenants.

## Facteurs de décision

- Zéro dépendance externe pour le chemin binaire unique / air-gap.
- Une isolation multi-tenant robuste à l'échelle.
- Pas de CGO, afin de préserver un binaire statique pur-Go.

## Options envisagées

- **SQLite (pur-Go) → Postgres + row-level security (sécurité au niveau ligne).**
- **Une base de données graphe** (Neo4j, Dgraph) pour le graphe d'accès.

## Résultat de la décision

Option retenue : **SQLite embarqué** (`modernc.org/sqlite`, pur-Go, sans CGO) pour le
mono-nœud et l'air-gap ; **Postgres** (via `pgx`) avec **row-level security** indexée sur
`tenant_id` pour le multi-hôte, la montée en charge et le multi-tenant. Le graphe d'accès est
modélisé comme une **vue sur le modèle de données général**, et non comme un store distinct.

### Conséquences

- **Bon :** le binaire unique n'a aucune base de données à installer ; le même modèle monte
  en charge vers Postgres avec une isolation RLS par tenant.
- **Mauvais / compromis :** deux backends de stockage à prendre en charge ; la justesse de
  la RLS doit être testée (elle l'est — sous RLS forcée en CI).
- **Neutre :** le graphe d'accès n'a besoin d'aucun moteur de graphe spécial puisqu'il s'agit
  d'une vue.

## Pourquoi les alternatives ont été rejetées

- **Base de données graphe** — lourde à auto-héberger et surdimensionnée : le graphe d'accès
  est une vue sur le modèle relationnel, pas une charge de travail nécessitant un moteur de
  graphe dédié.
