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
browser will warn once; that is expected. The token is shown ONCE and is
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

Le chart Helm signé déploie le control plane sous forme de **StatefulSet du cœur (core)**
(écrivain unique ; son répertoire de données contient la clé de signature de l'audit et le matériel
TLS) et, pour la topologie distribuée, d'un **DaemonSet de collecteurs** qui pousse les observations
vers le cœur via **gRPC + mTLS**. Au moment de la release, le chart est publié sur un registre OCI et
signé avec cosign, de sorte que vous le vérifiez à l'installation et l'épinglez par empreinte (digest).
(La première release est encore un **brouillon** : tant qu'un tag `chart-v*` n'a pas été créé, le chemin
du registre est vide, donc la commande ci-dessous est le chemin que vous utiliserez une fois qu'une
release sera publiée.)

```bash
helm install olivares \
  oci://ghcr.io/olivaresai/charts/olivares \
  --version <chart-version> \
  --set image.repository=docker.io/olivaresai/olivares \
  --set image.digest=<sha256-digest>
```

> Le chart publié est **signé avec cosign sur le manifeste OCI**, pas avec GPG : la pipeline de
> release n'émet aucune couche `.prov`, donc `helm --verify` ne peut pas le vérifier. Vérifiez avec
> `cosign verify` contre l'identité `release-chart.yml@refs/tags/chart-v*` — voir
> `deploy/helm/README.md`.

Le chart tire l'image conteneur depuis Docker Hub (`docker.io/olivaresai/olivares`) ; la même image se trouve
également sur `ghcr.io/olivaresai/olivares`, identique par empreinte ; pointez-y
`image.repository` si la limite de débit des pulls **anonymes** de Docker Hub vous gêne
(ghcr.io ne l'applique pas aux images publiques). L'artefact **chart** lui-même
reste sur `oci://ghcr.io/olivaresai/charts/olivares`.

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
