---
title: Déployer avec Docker
description: >-
  Récupérez et vérifiez l'image depuis Docker Hub, puis exécutez le control plane
  en production avec Docker — SQLite mono-nœud durci, Postgres multi-locataire,
  sauvegardes DR planifiées, terminaison TLS par reverse-proxy, mises à niveau et
  épinglage par digest.
---

Ce guide s'adresse aux ingénieurs et SRE qui mettent le control plane Olivares AI en
production avec Docker. Tout le produit est une unique image distroless — le moteur
avec l'interface web embarquée — de sorte qu'un seul hôte peut exécuter la topologie
SQLite sans dépendance externe, et un override Postgres vous donne la topologie
multi-locataire quand vous en avez besoin. Chaque chemin conserve les mêmes valeurs
par défaut sécurisées : aucune crédential par défaut, un token de configuration à
usage unique, TLS activé par défaut, et le port de l'hôte lié à loopback.

:::note[Beta — aucune release n'est encore publiée]
Olivares AI est en **beta**. Les coordonnées d'image ci-dessous ne se résolvent
**qu'après la publication de la première release (CalVer `26.8.0`)** ; jusque-là, les
registres n'ont rien à récupérer. Considérez ceci comme la forme de déploiement que
vous utiliserez, non comme une garantie prête pour la production.
:::

Pour la vue page-de-décision de toutes les options de déploiement et de leurs valeurs
par défaut, voir [Auto-héberger le control plane](/how-to/self-hosting/). Pour les
sites déconnectés, voir [Installer dans un environnement air-gapped](/how-to/air-gap-install/) ;
pour le scale-out, voir le chemin Kubernetes/Helm ci-dessous.

## 1. Récupérer et vérifier l'image

Le pull de conteneur primaire est **Docker Hub** :

```bash
docker pull docker.io/olivaresai/olivares:26.8.0
```

Le même contenu est également publié sur `ghcr.io/olivaresai/olivares` — identique par
digest, utilisé comme sauvegarde et comme registre de build. Docker Hub applique une limite
de débit aux pulls **anonymes** ; ghcr.io n'impose aucune limite sur les pulls anonymes
d'images publiques — `docker login` ou la coordonnée ghcr.io est donc la porte de sortie si un
nœud de CI ou une flotte importante atteint le plafond. Les tags ne portent
**aucun `v` initial** : `:26.8.0` épingle une release, `:latest` flotte, et
`:26.8.0-fips` / `:26.8.0-stig` sont les variantes durcies. Les tags de base et
`:latest` sont multi-arch (`linux/amd64`, `linux/arm64`) ; `fips`/`stig` sont
`amd64`-only.

Un control plane est un produit de sécurité, donc vérifiez avant d'exécuter. La
signature est **keyless** (Sigstore) contre l'identité GitHub Actions du projet, et
fonctionne de manière identique contre l'un ou l'autre registre — les signatures et
attestations sont copiées vers Docker Hub par `cosign copy`, donc le digest est le
même :

```bash
IMAGE=docker.io/olivaresai/olivares          # fallback: ghcr.io/olivaresai/olivares (same digest)
DIGEST="$(crane digest "$IMAGE:26.8.0")"
REF="$IMAGE@$DIGEST"

cosign verify "$REF" \
  --certificate-identity-regexp '^https://github\.com/olivaresai/olivares/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
cosign verify-attestation "$REF" --type spdxjson \
  --certificate-identity-regexp '^https://github\.com/olivaresai/olivares/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

La chaîne complète — signature des checksums, SBOM, OpenVEX, provenance SLSA — est
dans [Vérifier ce que vous avez téléchargé](/how-to/verify-a-release/). Une fois
vérifié, déployez par le **digest** que vous avez vérifié, jamais par un tag mutable
(voir [§8](#8-épingler-par-digest-pour-la-production)).

## 2. Mono-nœud, SQLite

### Avec `docker run` (durci)

La commande par défaut de l'image se lie à `0.0.0.0` **à l'intérieur du conteneur**
pour que vous puissiez la placer derrière un ingress ; le mapping de port côté hôte
ci-dessous épingle l'exposition à loopback. Exécutez-la en non-root, en lecture seule,
avec toutes les capabilities retirées :

```bash
docker volume create olivares-data

docker run -d --name olivares \
  --user 65532:65532 \
  --read-only \
  --tmpfs /tmp \
  --cap-drop ALL \
  --security-opt no-new-privileges \
  -v olivares-data:/var/lib/olivares \
  -p 127.0.0.1:8443:8443 \
  -p 127.0.0.1:8444:8444 \
  docker.io/olivaresai/olivares:26.8.0 \
  serve \
    --listen=0.0.0.0:8443 \
    --grpc-listen=0.0.0.0:8444 \
    --data-dir=/var/lib/olivares \
    --checkpoint-interval=1h
```

| Flag | Pourquoi |
|---|---|
| `--user 65532:65532` | exécuter sous l'UID non-root `nonroot` intégré à l'image distroless |
| `--read-only` | le système de fichiers racine est immuable ; seuls le volume de données et `/tmp` sont accessibles en écriture |
| `--tmpfs /tmp` | un tmpfs scratch accessible en écriture, requis car le rootfs est en lecture seule |
| `--cap-drop ALL` | le moteur n'a besoin d'aucune capability Linux |
| `--security-opt no-new-privileges` | bloquer l'escalade de privilèges via les binaires setuid |
| `-v olivares-data:/var/lib/olivares` | persister le répertoire de données (voir [§5](#5-notes-dexploitation)) |
| `-p 127.0.0.1:8443:8443` | publier HTTPS (REST + interface web) sur **loopback uniquement** |
| `-p 127.0.0.1:8444:8444` | publier gRPC (ingestion / API ControlPlane) sur loopback uniquement |

Lisez le token de configuration à usage unique dans les logs et créez le premier
administrateur :

```bash
docker logs olivares | sed -n '/FIRST-BOOT SETUP/,/========================/p'

curl -fsS -k -X POST https://127.0.0.1:8443/v1/setup \
  -H 'Content-Type: application/json' \
  -d '{"token":"<olst_ token>","email":"you@example.com","password":"<strong-password>"}'
```

`-k` accepte le certificat auto-signé que le moteur génère au premier démarrage ;
remplacez-le par un vrai certificat via un reverse proxy ([§6](#6-reverse-proxy--terminaison-tls))
ou votre propre matériel TLS. Le token est affiché **une seule fois** et est à usage
unique.

### Avec Docker Compose

Le dépôt fournit une stack Compose qui câble le volume, le mapping de port loopback et
les mêmes flags de durcissement que ci-dessus :

```bash
docker compose -f deploy/compose/docker-compose.yml up -d

# Read the one-time first-boot setup token:
docker compose -f deploy/compose/docker-compose.yml logs olivares | sed -n '/FIRST-BOOT SETUP/,/========================/p'

# Then open https://localhost:8443 (self-signed TLS by default)
```

Le fichier de base met par défaut l'image à `docker.io/olivaresai/olivares:latest` (Docker Hub) ;
pour un déploiement de production vérifiable, définissez `OLIVARES_IMAGE` dans
`deploy/compose/.env` à une référence épinglée par digest (voir
[§8](#8-épingler-par-digest-pour-la-production)). Les données persistent dans le volume
`olivares-data`.

## 3. Postgres multi-locataire

Pour la topologie multi-locataire, superposez l'override Postgres au-dessus du fichier
de base. Définissez d'abord les deux mots de passe, puis montez la stack :

```bash
cp deploy/compose/.env.example deploy/compose/.env   # set POSTGRES_SUPERUSER_PASSWORD + OLIVARES_DB_PASSWORD
docker compose -f deploy/compose/docker-compose.yml \
               -f deploy/compose/docker-compose.postgres.yml up -d
```

L'override monte `postgres:16-alpine`, provisionne le rôle **moindre-privilège**
`olivares_app` et la base `olivares` à la première initialisation (en exécutant le
`deploy/postgres/01-app-role.sql` canonique via `initdb/10-app-role.sh`), et pointe le
moteur vers ce rôle non-superutilisateur avec `--engine=postgres`. Cela rend le filet
de sécurité FORCE-RLS par locataire réel : le moteur **refuse de démarrer** contre un
rôle superutilisateur/`BYPASSRLS`.

:::caution[`sslmode=disable` est réservé à la démo intra-réseau]
Le DSN de l'override utilise `sslmode=disable` car les deux conteneurs partagent un
réseau Docker. **La production utilise TLS avec `sslmode=verify-full`.** Pour un
déploiement durci, préférez le chart Helm avec un Secret DSN et un Postgres managé (ou
le vôtre) — voir [§8](#8-épingler-par-digest-pour-la-production).
:::

## 4. Sauvegardes de reprise après sinistre

Le profil de backup produit des bundles DR planifiés et sûrs pour la continuité du
ledger : le snapshot du store plus les clés de signature, chiffrés sous votre KEK, avec
un manifeste des pointes de chaîne par locataire. Écrivez votre passphrase dans un
fichier conservé **hors du dépôt et de l'image**, puis exécutez le profil `backup`
en one-shot :

```bash
printf 'a strong DR passphrase' > deploy/compose/dr-pass
# the host stamps the bundle name (the distroless image has no `date`):
BACKUP_TS="$(date -u +%Y%m%dT%H%M%SZ)" \
docker compose -f deploy/compose/docker-compose.yml \
               -f deploy/compose/docker-compose.backup.yml \
               --profile backup run --rm backup
```

Le job partage le volume de données du moteur, écrit le bundle vers le volume
`olivares-backups` et — puisque l'image est distroless — laisse la rétention à l'hôte :
purgez les anciens bundles avec un cron de l'hôte
(`find <backups> -name '*.drbundle' -mtime +14 -delete`). Encapsulez l'exécution dans
un cron de l'hôte pour un RPO planifié et **mirorisez le volume `olivares-backups`
hors site** — une sauvegarde sur le même hôte n'est pas une reprise après sinistre.
Restaurez et vérifiez avec :

```bash
olivares dr restore --in <bundle> --data-dir <dir> --passphrase-file dr-pass
```

La procédure complète RPO/RTO, custody de clés et exercice DR vit avec le runbook DR du
dépôt ; la présentation de plus haut niveau est [Sauvegarder et restaurer](/how-to/backup-and-restore/).

## 5. Notes d'exploitation

**Sondez la santé depuis l'hôte, pas depuis le conteneur.** L'image est **distroless** —
elle n'a ni shell ni `curl`, donc il n'y a intentionnellement aucun `HEALTHCHECK`
in-container. Le moteur expose `/livez` et `/readyz` sur le port HTTPS ; sondez-les
depuis l'hôte (ou votre orchestrateur) :

```bash
# liveness — process is up; no dependency checks, so a store outage never restart-loops:
curl -fsS -k https://127.0.0.1:8443/livez

# readiness — store ping (and HA leadership): 200 when serving, 503 when the store is down:
curl -fsS -k https://127.0.0.1:8443/readyz
```

La joignabilité de `/readyz` est le signal de disponibilité — câblez-le dans votre
supervision externe (voir [Superviser avec Prometheus](/how-to/monitor-with-prometheus/)).

**Le token de configuration n'apparaît qu'une fois, dans les logs.** Le premier
démarrage imprime un token `olst_…` à usage unique dans la sortie du conteneur.
Capturez-le avec `docker logs olivares | sed -n '/FIRST-BOOT SETUP/,/========================/p'` (ou l'équivalent
Compose) avant que le buffer ne tourne ; il est consommé quand vous créez le premier
administrateur.

**Sauvegardez le répertoire de données.** `/var/lib/olivares` (le volume
`olivares-data`) contient le **store SQLite, la clé de signature d'audit et le matériel
TLS**. Le perdre fait perdre l'identité de signature du ledger et casse la continuité
de l'audit, donc protégez et sauvegardez le volume — utilisez le profil DR de
[§4](#4-sauvegardes-de-reprise-après-sinistre), pas une copie ad hoc d'un store en activité.

## 6. Reverse proxy / terminaison TLS

D'emblée, le moteur sert son propre certificat **auto-signé**, ce qui convient pour
l'évaluation mais pas pour des clients qui valident la confiance. En production, placez
le moteur lié à loopback derrière un reverse proxy qui termine TLS avec un certificat
fourni par l'opérateur (depuis votre CA ou ACME), et faites du proxy la seule chose
exposée sur le réseau.

Parce que le moteur parle lui-même TLS, le proxy s'y connecte en HTTPS sur le port
loopback. Un bloc serveur nginx minimal :

```nginx
server {
  listen 443 ssl;
  server_name olivares.example.com;

  ssl_certificate     /etc/ssl/olivares/fullchain.pem;   # operator-provided cert
  ssl_certificate_key /etc/ssl/olivares/privkey.pem;

  location / {
    proxy_pass         https://127.0.0.1:8443;   # engine's own TLS on loopback
    proxy_ssl_verify   off;                       # engine cert is self-signed
    proxy_set_header   Host              $host;
    proxy_set_header   X-Forwarded-For   $proxy_add_x_forwarded_for;
    proxy_set_header   X-Forwarded-Proto $scheme;
  }
}
```

L'équivalent avec Caddy, qui provisionne automatiquement un certificat public :

```caddy
olivares.example.com {
  reverse_proxy https://127.0.0.1:8443 {
    transport http {
      tls_insecure_skip_verify   # engine cert is self-signed on loopback
    }
  }
}
```

Gardez les ports hôte du moteur liés à `127.0.0.1` (les valeurs par défaut ci-dessus)
pour que seul le proxy soit joignable. Le port d'ingestion gRPC (`8444`) est destiné
aux collecteurs ; exposez-le délibérément, avec son propre chemin TLS, uniquement si
vous exécutez la topologie distribuée.

## 7. Mises à niveau

Le volume de données persiste à travers les remplacements de conteneur, donc une mise
à niveau consiste à : sauvegarder, récupérer le nouveau tag épinglé, recréer le
conteneur.

```bash
# 1. Back up first (see §4).
# 2. Pull the new release and re-verify it (see §1):
docker pull docker.io/olivaresai/olivares:26.8.1

# docker run:
docker stop olivares && docker rm olivares
# re-run the §2 command with the new tag — the olivares-data volume is reused.

# Compose: set OLIVARES_IMAGE to the new digest in .env, then:
docker compose -f deploy/compose/docker-compose.yml up -d
```

Recréer le conteneur ne touche pas au volume nommé, donc le store, la clé de signature
et le matériel TLS sont conservés. **Sauvegardez toujours avant de mettre à niveau**,
et re-vérifiez la nouvelle image avant de recréer.

## 8. Épingler par digest pour la production

Les tags mutables (`:26.8.0`, `:latest`) sont pour l'évaluation. En production, épinglez
le **digest** que vous avez vérifié — un digest est immuable et correspond exactement à
ce que vous avez validé :

```bash
docker run ... docker.io/olivaresai/olivares@sha256:<digest> serve ...
```

Pour Compose, définissez la référence par digest dans `deploy/compose/.env` :

```bash
OLIVARES_IMAGE=docker.io/olivaresai/olivares@sha256:<digest>
```

Pour le scale-out et le multi-nœud, utilisez le chart Helm — publié comme artefact OCI
à `oci://ghcr.io/olivaresai/charts/olivares`, signé par cosign, et épinglé par digest
d'image. Voir [Auto-héberger le control plane](/how-to/self-hosting/) pour la commande
du chart et [Installer dans un environnement air-gapped](/how-to/air-gap-install/) pour
les sites entièrement déconnectés.
