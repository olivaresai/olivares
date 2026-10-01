---
title: "Sauvegarder et restaurer"
description: >-
  Des sauvegardes chiffrées, sûres pour la continuité du ledger, avec olivares
  dr : bundles planifiés pour SQLite et Postgres, la restauration qui vérifie la
  chaîne, l'exercice que vous pouvez exécuter sans toucher à la production — et
  les deux clés qui décident si vos preuves survivent.
---

La sauvegarde d'un control plane a une tâche plus difficile que la plupart : il
doit revenir avec son **ledger à altération détectable dont l'intégrité est prouvée**. `olivares dr` est
construit autour de cette exigence — chaque bundle enregistre les pointes de
chaîne par tenant, la restauration **échoue avec un code non nul si le ledger
restauré n'est pas sûr pour la continuité**, et `dr drill` vérifie la
continuité du ledger, de la sauvegarde à la restauration, sur un ledger
synthétique dans un répertoire de travail, puis affiche la RTO mesurée. Il ne
vérifie pas les droits, la garde des clés ni les effets postérieurs au point
de restauration, et il ne touche pas un répertoire de données de production.

Le bundle est chiffré sous une **KEK que vous fournissez** — une phrase secrète
dérivée par Argon2id (`--passphrase-file`) ou une clé brute de 32 octets issue de
votre KMS (`--kek-key-file`) ; l'une des deux exactement est requise. Les clés de
signature de l'audit et du catalogue voyagent **scellées** à l'intérieur du
bundle.

**Le contrôle de restauration DR enregistré prend en charge PostgreSQL 15, 16, 17 et 18 dans 26.10** (mesuré sur 15.19, 16.15, 17.11 et 18.6). Les autres versions majeures sont refusées avec `DR restore control contract: PostgreSQL major <major> is unsupported (verified contracts: 15, 16, 17, 18)`, où `<major>` est la version majeure détectée du serveur. Un contrôle n'est jamais réparé : après une mise à niveau majeure de l'installation existante, un contrôle qui ne correspond pas au catalogue et aux permissions de la nouvelle version est refusé.

## Sauvegarder

**SQLite** (nœud unique) — sûr pendant que `serve` tourne (l'instantané utilise
`VACUUM INTO` ; le WAL autorise la lecture concurrente) :

```bash
olivares dr backup \
  --data-dir /var/lib/olivares --engine sqlite \
  --out /backups/olivares-dr-$(date -u +%Y%m%dT%H%M%SZ).drbundle \
  --passphrase-file <your-dr-passphrase-file>
```

**Postgres** — un `pg_dump --format=custom` cohérent piloté par la même commande
(`--engine postgres --dsn … --admin-dsn …`), ou bien remettez-lui un dump
pré-fabriqué avec `--snapshot-file`. Piloter le dump directement **exige
`--admin-dsn`** : `pg_dump` maintient `row_security=off` et **avorte** dès qu'il
est exécuté sous le rôle applicatif contre des tables en `FORCE ROW LEVEL
SECURITY` ; la commande **refuse d'entrée de jeu** plutôt que de partir et de ne
rien produire. Pour un RPO quasi nul, `--pitr-ref` produit un bundle
compagnon clés+manifeste qui s'associe à votre configuration PITR d'archivage du
WAL (`deploy/postgres/backup/pitr-setup.md`) ; les scripts wrapper
`deploy/postgres/backup/pg-dump.sh` / `pg-restore.sh` empaquettent le même flux.

Deux interrupteurs d'honnêteté à connaître :

- La sauvegarde **refuse de capturer un ledger qui ne se vérifie pas** au moment
  de la sauvegarde — `--allow-unverified` existe, est journalisé et n'est pas
  recommandé.
- Une sauvegarde Postgres sans `--admin-dsn` (un rôle dédié `NOSUPERUSER
  BYPASSRLS`) avertit que l'ensemble des tenants capturé peut être limité par la
  RLS et **incomplet** — cet avertissement concerne le cas d'un instantané
  **pré-fabriqué** (`--snapshot-file` / `--pitr-ref`) : le dump lui-même est
  correct, ce que le rôle admin apporte, c'est l'**inventaire inter-tenant du
  manifeste**. Provisionnez-le pour une couverture multi-tenant complète.
- Piloter `pg_dump` directement est un cas différent, pas un avertissement : sans
  `--admin-dsn`, la commande **se refuse d'emblée** (voir ci-dessus).

**Planification :** la stack Compose livre un
[profil de sauvegarde](/fr/tutorials/getting-started/docker-compose/#3-sauvegardes-dr-chiffrées-le-profil-backup),
le chart Helm un
[CronJob](/fr/tutorials/getting-started/kubernetes/#4-sauvegardes-chiffrées-planifiées) ;
sur bare metal, planifiez la commande ci-dessus avec cron. Les chiffres du
tableau sont des cibles. Pour les paliers cron, la cible de RPO est
l'intervalle cron. `dr drill` mesure la RTO SQLite sur un ledger synthétique
(restauration, démarrage et vérification). Il ne mesure pas le RPO et ne
mesure pas une restauration Postgres. Les RTO SQLite mesurées, avec le nombre
d'événements et l'hôte, sont dans `docs/DR-RUNBOOK.md` ; ce ne sont pas les
cibles de ce tableau.

| Palier | Mécanisme | RPO cible | RTO cible |
|---|---|---|---|
| SQLite | `dr backup` via cron | l'intervalle cron | < 15 min |
| Postgres logique | `pg-dump.sh` via cron | l'intervalle cron | < 30 min |
| Postgres PITR | sauvegarde de base + archivage WAL | ≈ secondes | < 30 min |

Mettez les bundles en miroir **hors site** et conservez la KEK **séparée des
bundles** (3-2-1) : une sauvegarde sur le même hôte n'est pas une reprise après
sinistre, et un bundle voyageant avec sa phrase secrète n'est chiffré en aucun
sens qui compte.

## Exercice — avant d'en avoir besoin

`dr verify` vérifie un bundle **sans toucher à votre data dir**. Sous SQLite,
il restaure dans un répertoire de travail et vérifie la continuité du ledger,
puis sort avec un code non nul si cette vérification échoue. Sous Postgres, il
vérifie le digest du bundle et le déchiffrement des clés. La vérification
complète de la chaîne d'un bundle Postgres exige une restauration sur un
Postgres de travail. Lancez la vérification avec :

```bash
olivares dr verify --in /backups/olivares-dr-<ts>.drbundle \
  --passphrase-file <your-dr-passphrase-file>
```

`dr inspect --in <bundle>` affiche le manifeste (sans KEK requise, sans secrets
montrés) — quel moteur, quels tenants, quelles pointes de chaîne. Exécutez
l'exercice à la même cadence que la sauvegarde ; une sauvegarde non vérifiée est
un espoir, pas un contrôle.

## Restaurer

```bash
olivares dr restore --in /backups/olivares-dr-<ts>.drbundle \
  --data-dir /var/lib/olivares --engine sqlite \
  --passphrase-file <your-dr-passphrase-file>
```

La séquence de restauration est délibérée : les clés de signature d'abord
(fail-closed en cas d'écrasement — `--force` est la dérogation explicite), puis
l'instantané du store, puis elle **démarre le store restauré et prouve la
continuité du ledger**, en sortant avec un code non nul si la chaîne n'est pas
sûre. Après toute restauration, re-vérifiez face à votre épingle de checkpoint
**hors machine** — un instantané plus ancien restauré peut passer un parcours
naïf tout en échouant à la comparaison hors machine
([dépannage § ledger](/fr/how-to/troubleshooting/#le-ledger-échoue-à-la-vérification)).

## Les deux clés qui décident de tout

| Clé | Règle |
|---|---|
| **La KEK du PRA** (phrase secrète ou clé brute) | sans elle, chaque bundle n'est que du bruit. Stockez-la dans un système différent de celui des bundles ; perdre les deux à la fois est le mode de défaillance |
| **`audit-signing.key`** (dans le data dir) | sauvegardez-la hors machine au provisionnement — le moteur ne fait qu'**avertir** au premier démarrage, il n'y a pas de séquestre imposé, et une clé perdue rend le ledger définitivement invérifiable. Épinglez aussi la clé publique hors machine (`GET /v1/audit/pubkey`) |

Pour la garde des clés de signature elles-mêmes via KMS (enveloppes BYOK,
cérémonies de rotation, `olivares keys`), voir la
[référence CLI](/fr/reference/cli/) ; pour les parcours guidés des modes de
défaillance, la [page de dépannage](/fr/how-to/troubleshooting/).
