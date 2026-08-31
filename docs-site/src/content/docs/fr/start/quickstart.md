---
title: Démarrage rapide
description: >-
  De zéro à un graphe d'accès lecture/écriture peuplé avec un véritable résultat
  de dérive Permis-vs-Observé en cinq minutes environ — d'abord sur l'estate de
  démonstration fourni, puis sur un véritable connecteur pgAudit pour prouver que
  ce n'est pas une démo.
---

C'est le chemin rapide pour comprendre à quoi sert Olivares AI : une **carte
d'accès lecture/écriture** de votre estate et la **dérive Permis-vs-Observé** par-dessus
— l'écart entre l'accès qu'un agent s'est vu *accorder* et l'accès qu'il est
*observé* en train d'utiliser.

Vous atteindrez ce résultat deux fois, en cinq minutes environ au total :

1. **En une minute, sur l'estate de démonstration fourni** — la rampe d'accès
   instantanée « à quoi ça ressemble au juste » (observations synthétiques, circulant
   à travers le moteur réel).
2. **Puis sur un véritable connecteur** — le même graphe et la même dérive, cette fois
   analysés mot pour mot à partir d'un journal **pgAudit** PostgreSQL, pour prouver que
   la fonctionnalité phare tourne sur des données authentiques, pas sur une démo.

Chaque commande ci-dessous est exécutée, exactement telle qu'écrite, par
`scripts/quickstart-smoke.sh` ([reproductibilité](#5-reproduire-cela-vous-même)) — de
sorte que cette page ne peut pas dériver discrètement par rapport au binaire.

C'est un parcours d'apprentissage, pas un déploiement de production. Pour la véritable
installation (pas d'identifiants par défaut, un jeton de configuration à usage unique,
TLS), allez à [auto-hébergement](/fr/how-to/self-hosting/). Pour une visite guidée de
l'interface, voir le [tutoriel zéro-au-graphe](/fr/tutorials/zero-to-graph/).

:::caution[Le mode démo est réservé à l'apprentissage]
`--seed-demo` provisionne un administrateur de démonstration avec un **mot de passe
public présent dans l'arbre des sources** et des données synthétiques, et il **refuse
de démarrer sur une adresse non-loopback**. Ne l'utilisez jamais pour une véritable
installation — le véritable parcours de premier démarrage est l'étape 3 ci-dessous et
dans [auto-hébergement](/fr/how-to/self-hosting/).
:::

## 1. Compiler le binaire unique

À partir d'un checkout du dépôt (nécessite Go 1.26+, [Task](https://taskfile.dev) et
pnpm — `task build` empaquette l'interface web avant de compiler ; le store est en
SQLite pur-Go, donc pas de chaîne d'outils C) :

```bash
task build                      # compiles ./bin/olivares with the web UI embedded
./bin/olivares version
```

`task build` produit un unique artefact autonome dans `./bin/olivares` — le
moteur, l'interface web embarquée et les plugins de connecteurs de première partie. Les
**installations conteneur et Kubernetes enveloppent ce même binaire** : une image publiée
plus un fichier Compose ([auto-hébergement](/fr/how-to/self-hosting/)), ou un manifeste plat
que vous appliquez avec `kubectl apply -f deploy/manifests/install.yaml` (pas de Helm
requis). La fonctionnalité phare que vous voyez ci-dessous est identique sur les trois —
seul le seed de démo diffère (loopback uniquement, jamais dans une véritable installation).

## 2. Démarrer l'estate de démonstration (loopback uniquement)

```bash
DATA="$(mktemp -d)"
./bin/olivares serve --insecure --seed-demo \
  --listen 127.0.0.1:8901 --grpc-listen 127.0.0.1:8902 \
  --data-dir "$DATA"
```

`--insecure` sert du HTTP en clair sur loopback (acceptable pour une démo locale ; **TLS
est activé par défaut** sinon). Vous verrez d'honnêtes lignes `WARN` pour les coutures
fermées par défaut (deny-closed) prêtes à l'emploi (pas de juge, pas d'embedder, pas de
portail d'approbation, pas de sources réelles), puis une bannière **DEMO MODE** avec les
identifiants :

```text
demo@olivares.local / olivares-demo-estate
```

L'estate synthétique circule à travers le **vrai** bus d'événements exactement comme le
ferait un collecteur pgAudit ou OpenTelemetry en direct — seules les observations sont
ensemencées.

## 3. Atteindre le graphe d'accès et sa dérive (la fonctionnalité phare)

Laissez le serveur tourner ; dans un second terminal, connectez-vous, résolvez le tenant
de démo, et récupérez le graphe et sa dérive :

```bash
BASE=http://127.0.0.1:8901
TOKEN="$(curl -sf -X POST "$BASE/v1/auth/login" \
  -H 'Content-Type: application/json' \
  -d '{"email":"demo@olivares.local","password":"olivares-demo-estate"}' \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])')"

TENANT="$(curl -sf "$BASE/v1/system/orgs" -H "Authorization: Bearer $TOKEN" \
  | python3 -c 'import sys,json;[print(o["tenant_id"]) for o in json.load(sys.stdin)["items"] if o["slug"]=="demo"]')"

# The read/write access map — module III:
curl -sf "$BASE/v1/m/accessmap/graph?limit=200" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" | python3 -m json.tool

# The Permitted-vs-Observed drift:
curl -sf "$BASE/v1/m/accessmap/drift" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" | python3 -m json.tool
```

L'estate de démo retourne exactement **20 nœuds et 13 arêtes**, et la dérive fait
ressortir **8 accès inattendus** et **2 autorisations inutilisées**. Chaque arête porte
les axes d'honnêteté du produit, afin que vous puissiez lire chaque constat sans deviner :

- **`mode`** — `read` / `write` / `readwrite` / `unknown` : la classification L/É, prise
  mot pour mot du signal, jamais déduite.
- **`attribution_tier`** — `firm` / `approximate` / `unknown` : avec quelle fermeté
  l'accès est lié à une identité d'agent ou de charge de travail *spécifique*. Dans la
  démo, **6 arêtes sont firm et 7 approximate** — par exemple un agent lisant une
  ressource qui ne lui a jamais été accordée (`appdb.public.secrets`, *firm*) versus une
  identité de pool partagé écrivant des journaux (`appdb.public.logs`, honnêtement
  *approximate*).
- **`coverage_tier`** — `clean` / `lossy` / `opaque` / `mixed` : la fidélité du signal de
  la *ressource*, orthogonale à l'attribution.

:::tip[Une capacité différenciée clé]
Le **diff entre Permis et Observé** est la *dérive du moindre privilège* — ce que vous
voulez trouver avant qu'un auditeur ou un attaquant ne le fasse. Le seed prouve qu'elle
est réelle, et non « tout est dérive » : les 3 arêtes accordées **et** observées se
réconcilient et disparaissent du résultat de dérive ; seuls les écarts authentiques
subsistent (8 accès inattendus + 2 autorisations déclarées mais jamais exercées). Et le
produit ne fabrique jamais une étiquette qu'il ne peut prouver — une attribution
seulement `approximate` le dit, au lieu d'inventer un agent `firm`.
:::

Le même graphe s'affiche dans l'interface web embarquée à `http://127.0.0.1:8901`
(connectez-vous avec les identifiants de démo et basculez vers l'organisation **Demo
Estate**).

Arrêtez le serveur de démo (`Ctrl-C`) avant l'étape suivante.

## 4. Le prouver sur un véritable connecteur (pas une démo)

La fonctionnalité phare n'est pas une magie ensemencée : elle tourne sur ce que vos
sources observent. Ici vous câblez le **véritable connecteur pgAudit** — le même chemin
de code qu'utilise une installation de production — contre un journal d'audit PostgreSQL,
**sans seed de démo**.

D'abord, un petit `pgAudit` csvlog (trois véritables lignes d'audit : deux lectures et une
écriture par une application). En production, pgAudit écrit celles-ci dans le journal
Postgres ; ici un fichier tient lieu de cette traînée :

```bash
WORK="$(mktemp -d)"
python3 - "$WORK/postgresql.csv" <<'PY'
import csv, sys
def row(ts, user, db, msg, app):
    r = [''] * 26
    r[0], r[1], r[2] = ts, user, db
    r[11] = 'LOG'; r[13] = msg; r[22] = app; r[23] = 'client backend'
    return r
rows = [
    row("2026-06-09 09:00:01.001 UTC", "claude_rw", "salesdb", "AUDIT: SESSION,1,1,READ,SELECT,TABLE,public.customers", "billing-agent"),
    row("2026-06-09 09:00:02.002 UTC", "claude_rw", "salesdb", "AUDIT: SESSION,2,1,WRITE,INSERT,TABLE,public.orders", "billing-agent"),
    row("2026-06-09 09:00:03.003 UTC", "claude_rw", "salesdb", "AUDIT: SESSION,3,1,READ,SELECT,TABLE,public.secrets", "billing-agent"),
]
with open(sys.argv[1], 'w', newline='') as f:
    csv.writer(f).writerows(rows)
PY
```

Maintenant, effectuez un **véritable premier démarrage** : démarrez une fois sans
identifiants par défaut, revendiquez le jeton de configuration à usage unique, et créez un
tenant auquel rattacher le connecteur.

```bash
BASE=http://127.0.0.1:8901
./bin/olivares serve --insecure \
  --listen 127.0.0.1:8901 --grpc-listen 127.0.0.1:8902 \
  --data-dir "$WORK/data" > "$WORK/server.log" 2>&1 &
SERVER=$!
sleep 2

# The one-time setup token is printed to stdout on first boot (look for `olst_…` on the
# server's console, or read it from the redirected log):
SETUP="$(grep -oE 'olst_[A-Z0-9]+' "$WORK/server.log" | head -1)"

curl -sf -X POST "$BASE/v1/setup" -H 'Content-Type: application/json' \
  -d "{\"token\":\"$SETUP\",\"email\":\"admin@local\",\"password\":\"correct-horse-battery-staple\"}"

TOKEN="$(curl -sf -X POST "$BASE/v1/auth/login" -H 'Content-Type: application/json' \
  -d '{"email":"admin@local","password":"correct-horse-battery-staple"}' \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])')"

TENANT="$(curl -sf -X POST "$BASE/v1/system/orgs" -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' -d '{"name":"Production","slug":"prod"}' \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["tenant_id"])')"
echo "tenant: $TENANT"

kill "$SERVER"                  # stop the first-run server; we restart it with pgAudit wired
```

Les connecteurs sont câblés depuis un seul fichier de configuration opérateur, par valeur,
jamais persisté par le moteur. Pointez pgAudit vers le journal de votre tenant et
**redémarrez** avec la configuration :

```bash
cat > "$WORK/sources.json" <<JSON
{"sources":[{"name":"salesdb-pgaudit","kind":"pgaudit","tenant":"$TENANT",
  "config":{"log_path":"$WORK/postgresql.csv","format":"csvlog"}}]}
JSON

OLIVARES_SOURCES_CONFIG="$WORK/sources.json" ./bin/olivares serve --insecure \
  --listen 127.0.0.1:8901 --grpc-listen 127.0.0.1:8902 --data-dir "$WORK/data"
```

Le journal de démarrage affiche `ingest: wired source … kind=pgaudit`. Dans un second
terminal, reconnectez-vous et lisez le graphe — cette fois les arêtes sont
**réellement analysées**, pas ensemencées :

```bash
TOKEN="$(curl -sf -X POST "$BASE/v1/auth/login" -H 'Content-Type: application/json' \
  -d '{"email":"admin@local","password":"correct-horse-battery-staple"}' \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])')"

curl -sf "$BASE/v1/m/accessmap/graph?limit=200" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" | python3 -m json.tool
curl -sf "$BASE/v1/m/accessmap/drift" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" | python3 -m json.tool
```

Vous obtenez **3 arêtes** — `salesdb.public.customers` (lecture), `…orders` (écriture),
`…secrets` (lecture) — chacune avec `signal_source: pg_audit` et `coverage_tier: clean`
(pgAudit rapporte la L/É mot pour mot), et la dérive signale les **3 comme accès
inattendus** (aucune autorisation n'est encore câblée, donc chaque accès observé est une
dérive).

:::note[Honnête par défaut : approximate jusqu'à ce que vous câbliez l'identité]
Ces arêtes réelles atterrissent en `attribution_tier: approximate`, pas `firm` — le signal
pgAudit nomme un rôle de base de données/une application, pas un *agent gouverné*. C'est le
défaut honnête : le produit n'affirmera pas avoir attribué avec fermeté un accès à un agent
qu'il ne peut prouver. Vous gagnez `firm` en câblant une source d'identité
(LDAP/IdP/SPIFFE) qui lie l'identifiant à une identité d'agent ou de charge de travail —
voir [connecter une source](/fr/how-to/connect-a-source/). L'estate de démo montre des arêtes
`firm` précisément parce qu'il pré-lie ses agents.
:::

:::note[La forme de l'endpoint]
Le résultat Permis-vs-Observé est servi à `/v1/m/accessmap/drift` (il n'y a pas de
`/diff`). Les routes `/v1/m/accessmap/*` ne figurent pas dans le contrat stable du cœur à
53 routes ; elles sont publiées dans un document **bêta** distinct — la
[référence des routes de module](/reference/api-beta/). La
[référence API](/reference/api/) documente la surface stable du cœur.
:::

## 5. Reproduire cela vous-même

Tout ce qui précède est vérifié, de bout en bout, contre le binaire réel :

```bash
task smoke:quickstart          # or: scripts/quickstart-smoke.sh
```

Il démarre l'estate de démo **et** le véritable chemin pgAudit, exécute les commandes
exactes de cette page, et vérifie les nombres (20 nœuds / 13 arêtes, 8 inattendus + 2
inutilisés, 3 arêtes pgAudit réelles). Si le parcours installation→valeur ou le résultat
de dérive cesse un jour d'être vrai, le smoke échoue — c'est le contrat qui garde cette
page honnête. Il s'achève en quelques secondes de temps réel ; le parcours parcouru à la
main ci-dessus correspond aux **cinq minutes** documentées.

## Étapes suivantes

- **Le lancer pour de vrai :** les tutoriels de prise en main parcourent chaque scénario
  d'installation de bout en bout —
  [nœud unique (systemd)](/fr/tutorials/getting-started/single-node/),
  [Docker Compose](/fr/tutorials/getting-started/docker-compose/),
  [Kubernetes/Helm](/fr/tutorials/getting-started/kubernetes/) et
  [air-gapped](/fr/tutorials/getting-started/air-gapped/) ;
  [auto-hébergement](/fr/how-to/self-hosting/) est la page de décision qui les traverse.
- **L'alimenter de vrais signaux :** [connecter une source](/fr/how-to/connect-a-source/) et
  le [catalogue de connecteurs](/fr/reference/connectors/) — ce que chaque source observe, son
  niveau de couverture honnête, et comment câbler l'identité pour que l'attribution devienne
  `firm`.
- **Le durcir :** [durcissement de la sécurité](/fr/how-to/security-hardening/) — défauts
  sécurisés, approbations human-in-the-loop, et vérification d'une release avant de
  l'exécuter.
- **Connaître les limites :** [Honnêteté & limites](/fr/start/honesty-and-limits/) — ce qui
  tourne aujourd'hui, ce qui est au stade de conception, et ce que le produit ne fait
  délibérément pas.
