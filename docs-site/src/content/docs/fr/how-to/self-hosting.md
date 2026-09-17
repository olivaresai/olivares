---
title: Héberger Olivares AI soi-même
description: >-
  Faites tourner Olivares AI vous-même — binaire unique, Docker Compose ou
  Kubernetes — avec des valeurs par défaut sécurisées : aucun identifiant par
  défaut, un jeton d'amorçage à usage unique et TLS activé par défaut, sans
  télémétrie obligatoire ni sortie du plan de contrôle par défaut. Ne franchit votre
  périmètre que ce que vous configurez à cette fin, des appels à vos API de modèles
  aux sorties SIEM/webhook que vous raccordez.
---

Olivares AI est conçu **pour l'auto-hébergement avant tout**. Le produit entier tient dans
un seul binaire statique avec l'interface web embarquée, si bien que le déploiement le plus
simple se résume à un seul fichier ; les chemins Compose et Kubernetes existent pour le
multi-nœud et la production. Tous les chemins partagent les mêmes valeurs par défaut
sécurisées — aucun identifiant par défaut, un jeton d'amorçage à usage unique, TLS activé
par défaut —, sans télémétrie obligatoire ni sortie du plan de contrôle par défaut. Ne
franchit votre périmètre que ce que **vous** configurez à cette fin : les appels à vos API
de modèles, les sorties SIEM/webhook que vous raccordez et, si vous en provisionnez un,
un fournisseur externe d'embeddings.

Ce guide est la **page de décision** du déploiement — les options et leurs valeurs par défaut
sécurisées d'un coup d'œil. Pour l'installation pas à pas de chaque scénario, les tutoriels de
prise en main parcourent chaque chemin de bout en bout :
[nœud unique (systemd)](/tutorials/getting-started/single-node/) ·
[Docker Compose](/tutorials/getting-started/docker-compose/) ·
[Kubernetes/Helm](/tutorials/getting-started/kubernetes/) ·
[air-gapped](/tutorials/getting-started/air-gapped/). Pour vérifier d'abord les artefacts
cryptographiquement, voir [Vérifier ce que vous avez téléchargé](/how-to/verify-a-release/) ;
pour les sites déconnectés, voir
[Installer dans un environnement air-gapped](/how-to/air-gap-install/).

## Valeurs par défaut sécurisées (tous les chemins)

| Valeur par défaut | Comportement |
|---|---|
| **Identifiants** | aucun. Au premier démarrage, un **jeton d'amorçage à usage unique** (`olst_…`) est affiché ; vous créez le premier administrateur avec lui. |
| **TLS** | activé par défaut. `--insecure` (texte en clair) est réservé au développement en local. |
| **Liaison** | le binaire se lie à la **boucle locale (loopback)** par défaut ; exposez-le délibérément. |
| **Licence** | Dans le binaire ouvert (AGPL), la licence est validée **hors ligne** (Ed25519) et sert uniquement d'attestation — elle ne conditionne ni ne dégrade jamais le produit ouvert, et cela ne change pas. Les add-ons commerciaux sont un droit à terme payé fourni sous la forme d'un **accès par abonnement aux dépôts enterprise** (le modèle SUSE/Novell) : l'obtention des add-ons et la réception de leurs mises à jour — mises à jour de sécurité comprises — exigent ce droit. Les environnements air-gapped sont desservis comme chez SUSE, au moyen d'un miroir local qui reste soumis à ce droit. |
| **Télémétrie de retour** | désactivée. Le moteur n'effectue aucun appel sortant obligatoire au démarrage. |

## Option 1 — binaire unique

Compilez l'unique artefact statique (magasin SQLite en Go pur, donc aucune chaîne d'outils C) et lancez-le :

```bash
task build                      # compiles ./bin/olivares with the web embedded
./bin/olivares serve \
  --listen 127.0.0.1:8443 \
  --grpc-listen 127.0.0.1:8444 \
  --data-dir /var/lib/olivares
```

Au premier démarrage, le moteur affiche la bannière d'amorçage :

```text
=== FIRST-BOOT SETUP ===
No accounts exist yet. Open the console and create the first administrator
with this one-time token — setup also creates your first organization and
makes that administrator its owner:

  Console:  https://127.0.0.1:8443
  Token:    olst_…

The console serves HTTPS with a self-signed certificate on first boot — your
browser will warn once; that is expected.

Passkeys will not work at that address:
a browser will not run a passkey ceremony at an IP address. Reach the
console by a host name.
On this machine the same console also answers at
  https://localhost:8443
and at that address the relying party is derived from the name, which the
verifier accepts.

The token is shown ONCE and is
single-use. Prefer the API? POST /v1/setup {"token":"…","email":"…",
"password":"…"} — add "organization":"…" to name it (default: "Default
Organization"). The reply carries the new organization's tenant_id.
========================
```

Créez le premier administrateur, puis connectez-vous :

```bash
curl -fsS -X POST https://localhost:8443/v1/setup \
  -H 'Content-Type: application/json' \
  -d '{"token":"<olst_ token>","email":"you@example.com","password":"<strong-password>"}'

curl -fsS -X POST https://localhost:8443/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"email":"you@example.com","password":"<strong-password>"}'
```

Le répertoire de données contient la base SQLite, la clé de signature de l'audit et le matériel
TLS — sauvegardez-le et protégez-le.

### Répertoire de données personnalisé (`layout: custom`)

La disposition native par défaut est `/var/lib/olivares`. L’adaptateur de service
signé (`install.sh --data-dir`, `scripts/install-service.sh`) admet un répertoire
de données **personnalisé** par **forme**, pas par liste d’autorisation. Le
manifeste de propriété enregistre `"layout": "custom"` (`CHANGELOG.md`
`[26.9.0]` Added ; `INSTALL.md`).

SDD 04 §6 : chaque champ configurable déclare propriétaire, schéma, sources
acceptées et validateur. Ici l’adaptateur possède le répertoire dédié ; l’opérateur
possède le parent. Ce sont les chaînes de refus de l’adaptateur lui-même
(`scripts/install-service.sh`) :

| Condition | What the adapter prints and exits 1 |
|---|---|
| Path is not `/*/*` (a top-level directory) | `custom data directory must be a dedicated directory at least two levels deep (for example /srv/olivares), not a top-level directory: $data_dir` |
| The data directory would contain the binary, config or unit | `custom data directory $data_dir must not contain the installed path $path` |
| Any path component is a symbolic link | `path component is a symbolic link ($prefix -> …); pass the resolved path instead of provisioning through a link: $1` |
| Parent of a new custom directory does not exist | `parent of the custom data directory does not exist; create it with the intended owner first: $(dirname -- "$data_target")` |
| Existing system directory mode is not 0700 or 0750 | `existing system data directory mode is $data_mode; require 0700 or 0750` |
| Path is under `/dev`, `/proc` or `/sys` | `data directory $data_dir is under an API file system (/dev, /proc, /sys): those hold kernel and device interfaces rather than durable state…; choose a real directory` |
| Path under `/tmp` or `/var/tmp` on systemd older than 235 | `data directory $data_dir is under /tmp or /var/tmp and this host runs systemd $running: creating a BindPaths= destination inside the private /tmp needs systemd 235 or later…` |
| A BindPaths= path contains `:` | `$2 $1 contains ':' and this location can only be reached with BindPaths=, whose value uses ':' to separate source from destination; choose a path without it` |

`install-agentops.sh` applique la même règle à deux niveaux pour `OLIVARES_DATA_DIR` :
`OLIVARES_DATA_DIR must name a dedicated directory at least two levels deep
(for example /srv/olivares), not a top-level directory`. Il honore `OLIVARES_DATA_DIR` et un `OLIVARES_WORKSPACE_DIR`
choisi explicitement.

Un chemin sous `/home`, `/root` ou `/run/user` est rendu avec `ProtectHome=tmpfs`
et `BindPaths=` pour exactement ce répertoire. Un chemin sous `/tmp` ou `/var/tmp`
conserve `PrivateTmp=true` et reçoit `BindPaths=` pour ce répertoire seul
(`sandbox_access` dans `scripts/install-service.sh`).

`olivares uninstall` n’admet ce répertoire personnalisé que lorsque l’unité à son
chemin indexé exécute le moteur avec lui, ou lorsqu’un preserve a déjà laissé un
témoin de désinstallation à côté de la configuration du service. Diagnostiquez
la disposition AgentOps enregistrée avec `olivares doctor` — voir
[Dépannage](/how-to/troubleshooting/#agentops-layout-check).

Pour les installations par paquet, la valeur par défaut reste `/var/lib/olivares`.
Voir [Installer depuis un paquet](/how-to/install-from-packages/). Sous macOS, voir
[Installer avec Homebrew](/how-to/install-from-homebrew/).

## Option 2 — Docker Compose (nœud unique, SQLite)

Le dépôt fournit une pile Compose :

```bash
docker compose -f deploy/compose/docker-compose.yml up -d

# Read the one-time first-boot setup token from the logs:
docker compose -f deploy/compose/docker-compose.yml logs olivares | sed -n '/FIRST-BOOT SETUP/,/========================/p'

# Then open https://localhost:8443 (self-signed TLS by default)
```

Pour un backend Postgres multi-tenant, définissez les mots de passe et superposez la surcharge
Postgres :

```bash
cp deploy/compose/.env.example deploy/compose/.env     # set the two passwords
docker compose -f deploy/compose/docker-compose.yml \
               -f deploy/compose/docker-compose.postgres.yml up -d
```

:::note[Par défaut, le conteneur se lie à l'intérieur du conteneur]
La commande par défaut du conteneur se lie à `0.0.0.0` *à l'intérieur du conteneur*, afin que
vous puissiez le placer derrière votre ingress ; la pile Compose mappe le port hôte vers
`127.0.0.1`. Il n'existe pas de recette `docker run` nue — utilisez Compose (ou le chart Helm)
pour que le volume de données, les ports et le flux de premier démarrage soient correctement câblés.
:::

## Option 3 — Kubernetes (Helm)

Le chart Helm dans `deploy/helm/olivares` déploie le control plane sous forme de **StatefulSet du cœur (core)**
(écrivain unique ; son répertoire de données contient la clé de signature de l'audit et le matériel
TLS) et, pour la topologie distribuée, d'un **DaemonSet de collecteurs** qui pousse les observations
vers le cœur via **gRPC + mTLS**. La release moteur v26.9.0 ne publie pas le chart dans
un registre OCI : aucun tag indépendant `chart-v*` n'a encore exécuté ce workflow.
Installez le chart relu depuis un checkout et épinglez l'image publiée par digest.

```bash
helm install olivares \
  deploy/helm/olivares \
  --set image.repository=docker.io/olivaresai/olivares \
  --set image.digest=<sha256-digest>
```

> Lorsqu'un chart sera publié, `release-chart.yml` signera son manifeste OCI avec cosign sans
> couche GPG `.prov`. Cet artefact futur devra être vérifié par digest ; l'installation depuis
> les sources n'est pas présentée comme un téléchargement OCI signé. Voir `deploy/helm/README.md`.

Le chart tire l'image conteneur depuis Docker Hub (`docker.io/olivaresai/olivares`) ; la même image se trouve
également sur `ghcr.io/olivaresai/olivares`, identique par empreinte ; pointez-y
`image.repository` si la limite de débit des pulls **anonymes** de Docker Hub vous gêne
(ghcr.io ne l'applique pas aux images publiques). Le chart vient de
`deploy/helm/olivares` jusqu'à sa propre publication.

Déployez toujours **par empreinte (digest)**, jamais par un tag mutable. Pour un cluster totalement
déconnecté, mirroirez d'abord le bundle — voir [installation air-gap](/how-to/air-gap-install/).

## Choisir une topologie

| Topologie | Quand | Magasin | Bus d'événements |
|---|---|---|---|
| **Binaire unique** | nœud unique, labo, petit estate, air-gap | SQLite (embarqué) | en cours de processus |
| **Distribuée** | multi-hôte, mise à l'échelle, multi-tenant | Postgres + RLS | en cours de processus + **pont NATS** (`OLIVARES_BUS_CONFIG`; la livraison inter-nœuds est honnêtement au-plus-une-fois) |
| **Air-gapped** | aucune sortie réseau autorisée | SQLite ou Postgres | en cours de processus (pont NATS optionnel à l'intérieur du périmètre) |

Le **plan de données (collecteurs) tourne toujours sur votre infrastructure** — le control plane est
la seule chose dont vous choisissez l'hébergement. La
[vue d'ensemble de l'architecture](/explanation/architecture/overview/) explique les compromis.

## Connecter de vraies sources

Une installation fraîche a un estate vide. Câblez de vraies sources (pgAudit de Postgres, CloudTrail,
OpenTelemetry depuis les agents, eBPF) pour que l'access map se peuple — voir
[connecter une source](/how-to/connect-a-source/) et
[connecter Claude Code](/how-to/connect-claude-code/). Pour la surface de configuration, voir la
[référence de configuration](/reference/configuration/).
