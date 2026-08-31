---
title: Mit Docker bereitstellen
description: >-
  Image aus Docker Hub holen und verifizieren, dann die Control Plane in
  Produktion mit Docker betreiben — gehärtetes Single-Node-SQLite, mandantenfähiges Postgres,
  geplante DR-Backups, Reverse-Proxy-TLS-Terminierung, Upgrades und
  Digest-Pinning.
---

Dieser Leitfaden richtet sich an Engineers und SREs, die die Olivares-AI-Control-Plane in
Produktion mit Docker bringen. Das gesamte Produkt ist ein einziges Distroless-Image — die Engine
mit eingebetteter Web-UI — sodass ein einzelner Host die SQLite-Topologie ohne
externe Abhängigkeiten betreiben kann, und ein Postgres-Override Ihnen die mandantenfähige Topologie gibt,
wenn Sie sie brauchen. Jeder Pfad behält dieselben sicheren Standardwerte: keine Standard-Anmeldedaten,
ein einmaliges Setup-Token, TLS standardmäßig aktiv und der Host-Port an Loopback gebunden.

:::note[Beta — es ist noch kein Release veröffentlicht]
Olivares AI ist **Beta**. Die Image-Koordinaten unten lösen sich erst **nach Veröffentlichung
des ersten Release (CalVer `26.8.0`)** auf; bis dahin haben die Registries nichts zum Pullen.
Behandeln Sie dies als die Deployment-Form, die Sie verwenden werden, nicht als produktionsreife Garantie.
:::

Für die Entscheidungsseiten-Übersicht aller Deployment-Optionen und ihrer Standardwerte siehe
[Die Control Plane selbst hosten](/de/how-to/self-hosting/). Für getrennte Standorte siehe
[In einer Air-Gapped-Umgebung installieren](/de/how-to/air-gap-install/); für Scale-out siehe
den Kubernetes/Helm-Pfad unten.

## 1. Das Image holen und verifizieren

Der primäre Container-Pull ist **Docker Hub**:

```bash
docker pull docker.io/olivaresai/olivares:26.8.0
```

Derselbe Inhalt wird auch auf `ghcr.io/olivaresai/olivares` veröffentlicht — identisch per
Digest, verwendet als Backup und als Build-Registry. Docker Hub begrenzt die Rate **anonymer**
Pulls; ghcr.io begrenzt anonyme Pulls öffentlicher Images nicht — `docker login` oder die
ghcr.io-Koordinate ist daher der Ausweg, wenn ein CI-Knoten oder eine große Flotte an die
Grenze stößt. Tags tragen **kein führendes `v`**:
`:26.8.0` pinnt ein Release, `:latest` floatet, und `:26.8.0-fips` / `:26.8.0-stig` sind
die gehärteten Varianten. Die Basis- und `:latest`-Tags sind multi-arch
(`linux/amd64`, `linux/arm64`); `fips`/`stig` sind ausschließlich `amd64`.

Eine Control Plane ist ein Sicherheitsprodukt, also verifizieren Sie vor dem Ausführen. Das Signieren ist
**keyless** (Sigstore) gegen die GitHub-Actions-Identität des Projekts und funktioniert
identisch gegen beide Registries — die Signaturen und Attestierungen werden per
`cosign copy` zu Docker Hub kopiert, sodass der Digest derselbe ist:

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

Die vollständige Kette — Checksums-Signatur, SBOM, OpenVEX, SLSA-Provenance — finden Sie in
[Verifizieren, was Sie heruntergeladen haben](/de/how-to/verify-a-release/). Sobald verifiziert, deployen Sie über den
**Digest**, den Sie verifiziert haben, niemals über einen veränderbaren Tag (siehe [§8](#8-für-die-produktion-per-digest-pinnen)).

## 2. Single Node, SQLite

### Mit `docker run` (gehärtet)

Der Standardbefehl des Images bindet `0.0.0.0` **innerhalb des Containers**, sodass Sie es mit einem
Ingress davorschalten können; das Host-seitige Port-Mapping unten begrenzt die Exposition auf Loopback. Führen Sie es
als Non-Root, read-only und mit gedroppten Capabilities aus:

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

| Flag | Warum |
|---|---|
| `--user 65532:65532` | läuft als die in das Distroless-Image eingebackene Non-Root-`nonroot`-UID |
| `--read-only` | das Root-Dateisystem ist unveränderlich; nur das Daten-Volume und `/tmp` sind beschreibbar |
| `--tmpfs /tmp` | ein beschreibbares Scratch-tmpfs, erforderlich, weil das Rootfs read-only ist |
| `--cap-drop ALL` | die Engine benötigt keine Linux-Capabilities |
| `--security-opt no-new-privileges` | blockiert Privilege-Eskalation über setuid-Binaries |
| `-v olivares-data:/var/lib/olivares` | persistiert das Datenverzeichnis (siehe [§5](#5-betriebshinweise)) |
| `-p 127.0.0.1:8443:8443` | veröffentlicht HTTPS (REST + Web-UI) **nur an Loopback** |
| `-p 127.0.0.1:8444:8444` | veröffentlicht gRPC (Ingest / ControlPlane-API) nur an Loopback |

Lesen Sie das einmalige Setup-Token aus den Logs und erstellen Sie den ersten Administrator:

```bash
docker logs olivares | sed -n '/FIRST-BOOT SETUP/,/========================/p'

curl -fsS -k -X POST https://127.0.0.1:8443/v1/setup \
  -H 'Content-Type: application/json' \
  -d '{"token":"<olst_ token>","email":"you@example.com","password":"<strong-password>"}'
```

`-k` akzeptiert das selbstsignierte Zertifikat, das die Engine beim ersten Boot ausstellt; ersetzen Sie es
durch ein echtes Zertifikat über einen Reverse-Proxy ([§6](#6-reverse-proxy--tls-terminierung))
oder Ihr eigenes TLS-Material. Das Token wird **einmal** angezeigt und ist einmalig verwendbar.

### Mit Docker Compose

Das Repository liefert einen Compose-Stack, der das Volume, das Loopback-Port-Mapping
und dieselben Härtungs-Flags wie oben verdrahtet:

```bash
docker compose -f deploy/compose/docker-compose.yml up -d

# Read the one-time first-boot setup token:
docker compose -f deploy/compose/docker-compose.yml logs olivares | sed -n '/FIRST-BOOT SETUP/,/========================/p'

# Then open https://localhost:8443 (self-signed TLS by default)
```

Die Basisdatei setzt das Image standardmäßig auf `docker.io/olivaresai/olivares:latest` (Docker Hub); für ein
verifizierbares Produktions-Deployment setzen Sie `OLIVARES_IMAGE` in `deploy/compose/.env` auf eine
Digest-gepinnte Referenz (siehe [§8](#8-für-die-produktion-per-digest-pinnen)). Daten persistieren im
`olivares-data`-Volume.

## 3. Mandantenfähiges Postgres

Für die mandantenfähige Topologie legen Sie das Postgres-Override über die Basisdatei.
Setzen Sie zuerst die zwei Passwörter, dann fahren Sie den Stack hoch:

```bash
cp deploy/compose/.env.example deploy/compose/.env   # set POSTGRES_SUPERUSER_PASSWORD + OLIVARES_DB_PASSWORD
docker compose -f deploy/compose/docker-compose.yml \
               -f deploy/compose/docker-compose.postgres.yml up -d
```

Das Override fährt `postgres:16-alpine` hoch, provisioniert die **least-privilege**
Rolle `olivares_app` und die Datenbank `olivares` bei der ersten Initialisierung (führt das kanonische
`deploy/postgres/01-app-role.sql` über `initdb/10-app-role.sh` aus) und richtet die Engine
mit `--engine=postgres` auf diese Nicht-Superuser-Rolle aus. Dies macht den FORCE-RLS-Tenant-
Backstop real: Die Engine **weigert sich zu starten** gegen eine Superuser-/`BYPASSRLS`-Rolle.

:::caution[`sslmode=disable` ist nur für die In-Network-Demo]
Der DSN im Override verwendet `sslmode=disable`, weil sich beide Container ein Docker-
Netzwerk teilen. **Produktion verwendet TLS mit `sslmode=verify-full`.** Für ein gehärtetes Deployment
bevorzugen Sie das Helm-Chart mit einem DSN-Secret und einem managed (oder Ihrem eigenen) Postgres — siehe
[§8](#8-für-die-produktion-per-digest-pinnen).
:::

## 4. Disaster-Recovery-Backups

Das Backup-Profil erzeugt geplante, Ledger-Kontinuitäts-sichere DR-Bundles: den Store-
Snapshot plus die Signaturschlüssel, verschlüsselt unter Ihrer KEK, mit einem Manifest der
Tenant-spezifischen Chain-Tips. Schreiben Sie Ihre Passphrase in eine Datei, die **außerhalb von Repo und
Image** gehalten wird, dann führen Sie das einmalige `backup`-Profil aus:

```bash
printf 'a strong DR passphrase' > deploy/compose/dr-pass
# the host stamps the bundle name (the distroless image has no `date`):
BACKUP_TS="$(date -u +%Y%m%dT%H%M%SZ)" \
docker compose -f deploy/compose/docker-compose.yml \
               -f deploy/compose/docker-compose.backup.yml \
               --profile backup run --rm backup
```

Der Job teilt sich das Daten-Volume der Engine, schreibt das Bundle in das `olivares-backups`-
Volume und überlässt — weil das Image distroless ist — die Aufbewahrung dem Host: Bereinigen Sie alte
Bundles mit einem Host-Cron (`find <backups> -name '*.drbundle' -mtime +14 -delete`). Verpacken Sie
den Lauf in Host-Cron für ein geplantes RPO und **spiegeln Sie das `olivares-backups`-Volume
offsite** — ein Backup auf demselben Host ist keine Disaster Recovery. Wiederherstellen und verifizieren mit:

```bash
olivares dr restore --in <bundle> --data-dir <dir> --passphrase-file dr-pass
```

Das vollständige RPO/RTO-, Key-Custody- und DR-Drill-Verfahren liegt beim DR-
Runbook des Repositorys; der übergeordnete Walkthrough ist [Sichern und wiederherstellen](/de/how-to/backup-and-restore/).

## 5. Betriebshinweise

**Prüfen Sie die Health vom Host aus, nicht vom Container.** Das Image ist **distroless** — es
hat keine Shell und kein `curl`, sodass es absichtlich keinen In-Container-`HEALTHCHECK` gibt.
Die Engine stellt `/livez` und `/readyz` auf dem HTTPS-Port bereit; prüfen Sie sie vom Host aus
(oder von Ihrem Orchestrator):

```bash
# liveness — process is up; no dependency checks, so a store outage never restart-loops:
curl -fsS -k https://127.0.0.1:8443/livez

# readiness — store ping (and HA leadership): 200 when serving, 503 when the store is down:
curl -fsS -k https://127.0.0.1:8443/readyz
```

Die Erreichbarkeit von `/readyz` ist das Verfügbarkeitssignal — verdrahten Sie es in Ihr externes
Monitoring (siehe [Mit Prometheus überwachen](/de/how-to/monitor-with-prometheus/)).

**Das Setup-Token erscheint nur einmal, in den Logs.** Der erste Boot gibt ein einmalig verwendbares
`olst_…`-Token in der Container-Ausgabe aus. Erfassen Sie es mit
`docker logs olivares | sed -n '/FIRST-BOOT SETUP/,/========================/p'` (oder dem Compose-Äquivalent), bevor
der Buffer rotiert; es wird verbraucht, wenn Sie den ersten Administrator erstellen.

**Sichern Sie das Datenverzeichnis.** `/var/lib/olivares` (das `olivares-data`-Volume) enthält
den **SQLite-Store, den Audit-Signaturschlüssel und das TLS-Material**. Sein Verlust verliert die
Signatur-Identität des Ledgers und bricht die Audit-Kontinuität, also schützen und sichern Sie das
Volume — verwenden Sie das DR-Profil in [§4](#4-disaster-recovery-backups), keine Ad-hoc-Kopie
eines laufenden Stores.

## 6. Reverse-Proxy / TLS-Terminierung

Out of the box stellt die Engine ihr eigenes **selbstsigniertes** Zertifikat bereit, was für
die Evaluierung in Ordnung ist, aber nicht für Clients, die Vertrauen validieren. In Produktion stellen Sie
der Loopback-gebundenen Engine einen Reverse-Proxy voran, der TLS mit einem
operatorbereitgestellten Zertifikat terminiert (von Ihrer CA oder ACME), und lassen den Proxy das einzige
sein, was im Netzwerk exponiert ist.

Weil die Engine selbst TLS spricht, verbindet sich der Proxy über HTTPS auf dem
Loopback-Port mit ihr. Ein minimaler nginx-Server-Block:

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

Das Äquivalent mit Caddy, das automatisch ein öffentliches Zertifikat provisioniert:

```caddy
olivares.example.com {
  reverse_proxy https://127.0.0.1:8443 {
    transport http {
      tls_insecure_skip_verify   # engine cert is self-signed on loopback
    }
  }
}
```

Halten Sie die Host-Ports der Engine an `127.0.0.1` gebunden (die Standardwerte oben), sodass nur der
Proxy erreichbar ist. Der gRPC-Ingest-Port (`8444`) ist für Collectors; exponieren Sie ihn
bewusst, mit eigenem TLS-Pfad, nur wenn Sie die verteilte Topologie betreiben.

## 7. Upgrades

Das Daten-Volume persistiert über Container-Ersetzungen hinweg, sodass ein Upgrade lautet: sichern,
den neuen gepinnten Tag pullen, den Container neu erstellen.

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

Das Neuerstellen des Containers berührt das benannte Volume nicht, sodass der Store, der Signaturschlüssel
und das TLS-Material übernommen werden. **Sichern Sie immer vor dem Upgrade** und verifizieren Sie das neue
Image erneut, bevor Sie es neu erstellen.

## 8. Für die Produktion per Digest pinnen

Veränderbare Tags (`:26.8.0`, `:latest`) sind für die Evaluierung. In Produktion pinnen Sie den
**Digest**, den Sie verifiziert haben — ein Digest ist unveränderlich und ist genau das, was Sie abgesegnet haben:

```bash
docker run ... docker.io/olivaresai/olivares@sha256:<digest> serve ...
```

Für Compose setzen Sie die Digest-Referenz in `deploy/compose/.env`:

```bash
OLIVARES_IMAGE=docker.io/olivaresai/olivares@sha256:<digest>
```

Für Scale-out und Multi-Node verwenden Sie das Helm-Chart — veröffentlicht als OCI-Artefakt unter
`oci://ghcr.io/olivaresai/charts/olivares`, cosign-signiert und per Image-Digest gepinnt.
Siehe [Die Control Plane selbst hosten](/de/how-to/self-hosting/) für den Chart-Befehl und
[In einer Air-Gapped-Umgebung installieren](/de/how-to/air-gap-install/) für vollständig
getrennte Standorte.
