---
title: "Votre première heure avec Olivares AI"
description: >-
  Installez le plan de contrôle, ouvrez la console, connectez un agent de
  code, exécutez une session gouvernée qui autorise une action et en refuse
  une autre, et lisez les preuves. Trois formes : local (SQLite), équipe
  (Postgres et Docker), hybride.
---

Cette page est la première heure **sur cet arbre**. Ce n’est pas une maquette.
Chaque commande de la forme locale est celle que `task smoke:first-hour`
exécute contre `./bin/olivares`. Lorsqu’une forme a besoin de Docker ou de
Postgres, la page le dit.

Le produit imprime l’étape suivante. `olivares quickstart` nomme
l’enregistrement de passkey, `olivares agent tool detect`, l’inventaire, le
PEP de hook et `olivares doctor` après le jeton de configuration.
`olivares doctor` signale `first-hour-coding-agent`, `first-hour-hook-pep` et
`first-hour-next-step`. Ces contrôles sont facultatifs. Ils ne font pas
échouer une installation saine.

## Ce qu’est cette heure

Installer → console → connecter **un** agent de code → le voir dans
l’inventaire → session gouvernée qui **autorise une action et en refuse une
autre** → lire les preuves.

Cette page utilise le **PEP de hooks Claude Code** déjà livré dans cet arbre
(`olivares claude-hook`). Lancer un CLI officiel comme processus de session
est la couture du runtime Community. Cette heure ne duplique pas ce pilote. Elle
n’envoie pas de tour de modèle.

`--seed-demo` n’est pas cette heure.

## Forme 1 — Local (SQLite, cette boîte)

Ce conteneur **n’a pas Docker**. PostgreSQL **n’est pas en cours
d’exécution**. La forme locale utilise SQLite intégré et HTTP en boucle
locale. C’est la forme que le test de fumée rejoue.

### 1. Construire et démarrer

```bash
task build
./bin/olivares version
DATA="$(mktemp -d)"
./bin/olivares serve --insecure \
  --listen 127.0.0.1:8443 --grpc-listen 127.0.0.1:8444 \
  --data-dir "$DATA"
```

Le test de fumée utilise `serve --insecure` pour que `curl` n’ait pas à
faire confiance à un certificat auto-signé, et il passe explicitement
`--listen 127.0.0.1:8443` : cette liaison en boucle locale est celle du test
de fumée, pas la valeur par défaut du produit. Un opérateur interactif exécute
`olivares quickstart` (TLS activé, aucun identifiant par défaut, un token de
configuration à usage unique). Son adresse d’écoute par défaut est `:8443` —
**toutes les interfaces**, car c’est un serveur
(`cmd/olivares/binddefaults.go`) ; passez `--listen 127.0.0.1:8443` pour la
restreindre à cette machine. Mesuré sur cette boîte le 2026-09-17,
`quickstart --quiet` a imprimé le jeton en **3 s**.

Le panneau d’accueil imprime :

```text
     (HTTPS with a self-signed certificate on first boot — your browser will
      warn once; that is expected for a local install.)
  2. Complete setup with this one-time token (shown once, single-use):

         olst_…
```

Pour la liaison joker par défaut, la bannière affiche
`https://localhost:8443` puis liste, sous le token, toutes les autres adresses
auxquelles cet hôte répond — elles servent à atteindre la console depuis une
autre machine. Si la bannière a défilé, `olivares first-boot` réaffiche la ou
les adresses de la console et l’état de la configuration initiale. Ouvrez
`https://localhost:PORT` avant d’enregistrer une passkey : le navigateur refuse
une adresse IP comme relying party WebAuthn, et le produit le dit déjà dans le
conseil d’adresse (`cmd/olivares/consoleaddr.go`).

### 2. Créer l’administrateur et le tenant

Une seule commande termine la configuration sur le moteur en cours d’exécution, et
c’est celle qu’affiche le panneau de démarrage. Le jeton est lu sur l’entrée standard
afin qu’il n’apparaisse jamais dans la table des processus :

```bash
# collez le jeton olst_… que le moteur a affiché au démarrage
olivares auth bootstrap --server https://127.0.0.1:8443 \
  --ca-cert <data-dir>/tls.crt \
  --setup-token-file - \
  --email admin@local --password-file ./admin.pw \
  --organization "First hour" --save-context
```

`--save-context` ouvre la session et l’enregistre, si bien que la commande suivante est
déjà authentifiée. Mesuré le 2026-09-18 sur un répertoire de données vierge : 263 ms.

La même chose via l’API, si vous scriptez directement contre les endpoints :

```bash
BASE=http://127.0.0.1:8443
curl -sf -X POST "$BASE/v1/setup" -H 'Content-Type: application/json' \
  -d '{"token":"olst_…","email":"admin@local","password":"correct-horse-battery-staple"}'
TOKEN=$(curl -sf -X POST "$BASE/v1/auth/login" -H 'Content-Type: application/json' \
  -d '{"email":"admin@local","password":"correct-horse-battery-staple"}' \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])')
TENANT=$(curl -sf -X POST "$BASE/v1/system/orgs" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"First hour","slug":"first-hour"}' \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["tenant_id"])')
```

### 3. Connecter un agent de code et le voir dans l’inventaire

```bash
./bin/olivares agent tool detect -o json
curl -sf -X POST "$BASE/v1/agents" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  -H 'Content-Type: application/json' \
  -d '{"name":"claude-code-local","kind":"claude-code"}'
curl -sf "$BASE/v1/agents" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT"
./bin/olivares agent managed-settings --out ./managed-settings.json
```

`POST /v1/agents` **n’exige pas** AAL3. Créer des sources, connecteurs,
espaces de travail et secrets **si**.

### 4. Session gouvernée : autoriser Read, refuser Bash

Redémarrez avec `OLIVARES_HOOK_PEP_CONFIG` et une politique deny-closed.
Puis :

```bash
export OLIVARES_HOOK_PEP_URL=http://127.0.0.1:8447/
export OLIVARES_HOOK_PEP_TOKEN="$TOKEN"
export OLIVARES_HOOK_PEP_TENANT="$TENANT"
printf '%s\n' '{"session_id":"sess-first-hour","hook_event_name":"PreToolUse","tool_name":"Read","tool_input":{"file_path":"/repo/README.md"}}' \
  | ./bin/olivares claude-hook
printf '%s\n' '{"session_id":"sess-first-hour","hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"rm -rf /"}}' \
  | ./bin/olivares claude-hook
```

### 5. Lire les preuves

```bash
curl -sf "$BASE/v1/audit?action=hook.tool.allow&limit=100" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT"
curl -sf "$BASE/v1/audit?action=hook.tool.deny&limit=100" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT"
./bin/olivares doctor --mode user
task smoke:first-hour
```

## Forme 2 — Équipe (Postgres et Docker)

Ce conteneur **n’a pas Docker**. PostgreSQL **n’est pas en cours
d’exécution** ici. Ne traitez pas les commandes ci-dessous comme mesurées
sur cette boîte.

```bash
cp deploy/compose/.env.example deploy/compose/.env
docker compose -f deploy/compose/docker-compose.yml \
               -f deploy/compose/docker-compose.postgres.yml up --wait --wait-timeout 120
```

Sans le pool `olivares_admin`, `POST /v1/setup` répond
`501 cross_tenant_admin_pool_not_configured`. Voir
[Déployer avec Docker](/how-to/docker-deployment/).

## Forme 3 — Hybride (plan local, agent sur un poste)

Gardez le plan de contrôle en SQLite local. Exécutez l’agent sur un poste
qui a déjà `claude`. Une session CLI officielle en direct (PTY) relève du runtime Community, pas de
cette page.

## La barrière AAL3 (toujours vraie)

Créer des sources, connecteurs, espaces de travail et secrets est **refusé**
jusqu’à AAL3. PIV/CAC répond **501** sur une installation standard. Ouvrez
`https://localhost:PORT`, puis **Identity → Privileged login**.

## 3. Lancer une session Claude Code depuis la console

Sans source d’identifiants d’inférence, les lancements stream-json sont
deny-closed :

```text
session runtime: no inference credential source configured; stream-json launches are deny-closed
```

(`cmd/olivares/sessionruntime.go`). Définissez **une** de
`OLIVARES_SESSION_RUNTIME_WIF` ou `OLIVARES_SESSION_RUNTIME_TOKEN_FILE`.
Depuis la v26.10 ce n'est plus le seul chemin : enregistrez l'identifiant dans
la console et liez-le à un profil. Voir
[Ajouter un fournisseur et lancer un agent](/fr/how-to/add-a-provider/) et
[Exploiter une session fournisseur](/how-to/operate-provider-sessions/).

## Pages connexes

- [Honnêteté et limites](/start/honesty-and-limits/)
- [Auto-héberger Olivares AI](/how-to/self-hosting/)
- [Exploiter une session fournisseur](/how-to/operate-provider-sessions/)
- [PEP des hooks Claude Code](/how-to/connectors/claude-code-hooks-pep/)
- [Déployer avec Docker](/how-to/docker-deployment/)
