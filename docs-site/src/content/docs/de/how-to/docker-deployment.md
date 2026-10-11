---
title: Mit Docker bereitstellen
description: >-
  Image aus Docker Hub holen und verifizieren, dann die Control Plane in
  Produktion mit Docker betreiben — gehärtetes Single-Node-SQLite, mandantenfähiges Postgres,
  geplante DR-Backups, Reverse-Proxy-TLS-Terminierung, Upgrades und
  Digest-Pinning.
---

> Deployment-Pakete werden über den Business-Kanal bereitgestellt; ihre Veröffentlichung ist hier nicht bestätigt. Prüfen Sie das Chart-Paket und den Herausgeber anhand der Kanalanleitung, bevor Sie das lokale Chart verwenden. Das Manifest-Beispiel nutzt eine von Business bereitgestellte Datei namens `business-install.yaml`. Die Installation ohne Netz erfordert Enterprise.


> Helm, Kubernetes operators, Terraform, appliance and FIPS/STIG images are Business deployment artifacts. The source paths below are in the Business distribution. Air-gapped installation requires Enterprise.


Dieser Leitfaden richtet sich an Engineers und SREs, die die Olivares-AI-Control-Plane in
Produktion mit Docker bringen. Das gesamte Produkt ist ein einziges Image — die Engine
mit eingebetteter Web-UI — sodass ein einzelner Host die SQLite-Topologie ohne
externe Abhängigkeiten betreiben kann, und ein Postgres-Override Ihnen die mandantenfähige Topologie gibt,
wenn Sie sie brauchen. Container-Images basieren auf Debian 13 slim (mit Node.js 24 für die
Agent-Tools) und laufen als Nicht-Root-Benutzer. Jeder Pfad behält dieselben sicheren Standardwerte: keine Standard-Anmeldedaten,
ein einmaliges Setup-Token und TLS standardmäßig aktiv. Der Host-Port wird auf allen
Schnittstellen veröffentlicht, denn dies ist ein Server — beschränken Sie ihn bewusst, wie unten.

<!-- release -->
:::note[Olivares 0.1]
Das nächste Release ist `0.1`; es ist noch nicht auf GitHub veröffentlicht. Die folgenden Befehle beschreiben die geplanten Artefakte. Bauen Sie bis zur Veröffentlichung aus dem Quellcode und prüfen Sie danach jedes Artefakt vor der Verwendung. Der beobachtete Veröffentlichungsstand steht in `docs/releases/0.1-install-surfaces.json`.
:::
<!-- /release -->

Für die Entscheidungsseiten-Übersicht aller Deployment-Optionen und ihrer Standardwerte siehe
[Die Control Plane selbst hosten](/de/how-to/self-hosting/). Für getrennte Standorte siehe
[In einer Air-Gapped-Umgebung installieren](/de/how-to/air-gap-install/); für Scale-out siehe
den Kubernetes/Helm-Pfad unten.

## 1. Das Image holen und verifizieren

Der primäre Container-Pull ist **Docker Hub**:

<!-- release -->
```bash
docker pull docker.io/olivaresai/olivares:0.1
```
<!-- /release -->

Derselbe Inhalt wird auch auf `ghcr.io/olivaresai/olivares` veröffentlicht — identisch per
Digest, verwendet als Backup und als Build-Registry. Docker Hub begrenzt die Rate **anonymer**
Pulls; ghcr.io begrenzt anonyme Pulls öffentlicher Images nicht — `docker login` oder die
ghcr.io-Koordinate ist daher der Ausweg, wenn ein CI-Knoten oder eine große Flotte an die
Grenze stößt. Tags tragen **kein führendes `v`**: <!-- release -->
`:0.1`<!-- /release --> pinnt ein Release, `:latest` floatet, und <!-- release -->`:0.1-fips`<!-- /release --> / <!-- release -->`:0.1-stig`<!-- /release --> sind
die gehärteten Varianten. Die Basis- und `:latest`-Tags sind multi-arch
(`linux/amd64`, `linux/arm64`); `fips`/`stig` sind ausschließlich `amd64`.

Eine Control Plane ist ein Sicherheitsprodukt, also verifizieren Sie vor dem Ausführen. Das Signieren ist
**keyless** (Sigstore) gegen die GitHub-Actions-Identität des Projekts und funktioniert
identisch gegen beide Registries — die Signaturen und Attestierungen werden per
`cosign copy` zu Docker Hub kopiert, sodass der Digest derselbe ist:

<!-- release -->
```bash
IMAGE=docker.io/olivaresai/olivares          # fallback: ghcr.io/olivaresai/olivares (same digest)
DIGEST="$(crane digest "$IMAGE:0.1")"
REF="$IMAGE@$DIGEST"

cosign verify "$REF" \
  --certificate-identity-regexp '^https://github\.com/olivaresai/olivares/\.github/workflows/release\.yml@refs/tags/[0-9]+\.[0-9]+$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
cosign verify-attestation "$REF" --type spdxjson \
  --certificate-identity-regexp '^https://github\.com/olivaresai/olivares/\.github/workflows/release\.yml@refs/tags/[0-9]+\.[0-9]+$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```
<!-- /release -->

Die vollständige Kette — Checksums-Signatur, SBOM, OpenVEX, SLSA-Provenance — finden Sie in
[Verifizieren, was Sie heruntergeladen haben](/de/how-to/verify-a-release/). Sobald verifiziert, deployen Sie über den
**Digest**, den Sie verifiziert haben, niemals über einen veränderbaren Tag (siehe [§8](#8-für-die-produktion-per-digest-pinnen)).

## 2. Single Node, SQLite

### Mit `docker run` (gehärtet)

Der Standardbefehl des Images bindet `0.0.0.0` **innerhalb des Containers**, sodass Sie es mit einem
Ingress davorschalten können; das Host-seitige Port-Mapping entscheidet über die Exposition. Unten veröffentlicht es auf allen Host-Schnittstellen — mit `-p 127.0.0.1:8443:8443` bleibt die Konsole auf dem Host. Führen Sie es
als Non-Root, read-only und mit gedroppten Capabilities aus:

<!-- release -->
```bash
docker volume create olivares-data

docker run -d --name olivares \
  --user 65532:65532 \
  --read-only \
  --tmpfs /tmp \
  --cap-drop ALL \
  --security-opt no-new-privileges \
  -v olivares-data:/var/lib/olivares \
  -p 8443:8443 \
  -p 8444:8444 \
  docker.io/olivaresai/olivares:0.1 \
  serve \
    --listen=0.0.0.0:8443 \
    --grpc-listen=0.0.0.0:8444 \
    --data-dir=/var/lib/olivares \
    --checkpoint-interval=1h
```
<!-- /release -->

| Flag | Warum |
|---|---|
| `--user 65532:65532` | läuft als die in das Image eingebackene Non-Root-`nonroot`-UID |
| `--read-only` | das Root-Dateisystem ist unveränderlich; nur das Daten-Volume und `/tmp` sind beschreibbar |
| `--tmpfs /tmp` | ein beschreibbares Scratch-tmpfs, erforderlich, weil das Rootfs read-only ist |
| `--cap-drop ALL` | die Engine benötigt keine Linux-Capabilities |
| `--security-opt no-new-privileges` | blockiert Privilege-Eskalation über setuid-Binaries |
| `-v olivares-data:/var/lib/olivares` | persistiert das Datenverzeichnis (siehe [§5](#5-betriebshinweise)) |
| `-p 8443:8443` | veröffentlicht HTTPS (REST + Web-UI) **auf allen Host-Schnittstellen** |
| `-p 8444:8444` | veröffentlicht gRPC (Ingest / ControlPlane-API) auf allen Host-Schnittstellen |

Lesen Sie das einmalige Setup-Token aus den Logs und erstellen Sie den ersten Administrator:

```bash
docker exec olivares olivares first-boot   # the console address(es) + setup state
docker logs olivares | sed -n '/FIRST-BOOT SETUP/,/========================/p'

curl -fsS -k -X POST https://127.0.0.1:8443/v1/setup \
  -H 'Content-Type: application/json' \
  -d '{"token":"<olst_ token>","email":"you@example.com","password":"<strong-password>"}'
```

`-k` akzeptiert das selbstsignierte Zertifikat, das die Engine beim ersten Boot ausstellt; ersetzen Sie es
durch ein echtes Zertifikat über einen Reverse-Proxy ([§6](#6-reverse-proxy--tls-terminierung))
oder Ihr eigenes TLS-Material. Das Token wird **einmal** angezeigt und ist einmalig verwendbar.

### Mit Docker Compose

Das Repository liefert einen Compose-Stack, der das Volume, die veröffentlichten Ports
und dieselben Härtungs-Flags wie oben verdrahtet:

```bash
docker compose -f deploy/compose/docker-compose.yml up -d

# Where the console answers, and whether first setup is still pending:
docker compose -f deploy/compose/docker-compose.yml exec olivares olivares first-boot

# Read the one-time first-boot setup token:
docker compose -f deploy/compose/docker-compose.yml logs olivares | sed -n '/FIRST-BOOT SETUP/,/========================/p'

# What is running, and how to stop it. Every command needs the -f: this file lives in
# deploy/compose/, so a bare `docker compose ps` answers "no configuration file provided".
docker compose -f deploy/compose/docker-compose.yml ps
docker compose -f deploy/compose/docker-compose.yml down

# Then open https://localhost:8443 (self-signed TLS by default)
```

Die Basisdatei setzt das Image standardmäßig auf `docker.io/olivaresai/olivares:latest` (Docker Hub); für ein
verifizierbares Produktions-Deployment setzen Sie `OLIVARES_IMAGE` in `deploy/compose/.env` auf eine
Digest-gepinnte Referenz (siehe [§8](#8-für-die-produktion-per-digest-pinnen)). Daten persistieren im
`olivares-data`-Volume.

## 3. Mandantenfähiges Postgres

Für die mandantenfähige Topologie legen Sie das Postgres-Override über die Basisdatei.
Setzen Sie zuerst drei unterschiedliche Passwörter. Verwenden Sie für diese
Compose-Demo nur `A-Z a-z 0-9 . _ ~ -`, dann fahren Sie den Stack hoch:

```bash
cp deploy/compose/.env.example deploy/compose/.env   # set three distinct passwords in deploy/compose/.env:
# POSTGRES_SUPERUSER_PASSWORD, OLIVARES_DB_PASSWORD, OLIVARES_ADMIN_PASSWORD
docker compose -f deploy/compose/docker-compose.yml \
               -f deploy/compose/docker-compose.postgres.yml up -d
```

Das Override fährt `postgres:16-alpine` hoch, provisioniert die **least-privilege**
Rolle `olivares_app` und die Datenbank `olivares` bei der ersten Initialisierung (führt das kanonische
`deploy/postgres/01-app-role.sql` über `initdb/10-app-role.sh` aus) und richtet die Engine
mit `--engine=postgres` auf diese Nicht-Superuser-Rolle aus. Dies macht den FORCE-RLS-Tenant-
Backstop real: Die Engine **weigert sich zu starten** gegen eine Superuser-/`BYPASSRLS`-Rolle.

Das Override stellt außerdem `olivares_admin` bereit, eine separate Rolle mit
`NOSUPERUSER BYPASSRLS` und ausschließlich Lesezugriff auf die Engine-Tabellen.
Die Engine verwendet sie über `--admin-dsn` für mandantenübergreifende
Lesezugriffe, auch bei der Ersteinrichtung.

:::caution[`sslmode=disable` ist nur für die In-Network-Demo]
Der DSN im Override verwendet `sslmode=disable`, weil sich beide Container ein Docker-
Netzwerk teilen. **Produktion verwendet TLS mit `sslmode=verify-full`.** Für ein gehärtetes Deployment
bevorzugen Sie das Helm-Chart mit einem DSN-Secret und einem managed (oder Ihrem eigenen) Postgres — siehe
[§8](#8-für-die-produktion-per-digest-pinnen).
:::

## 4. Disaster-Recovery-Backups

Das Backup-Profil erzeugt geplante, Ledger-Kontinuitäts-sichere DR-Bundles: den Store-
Snapshot plus die Signaturschlüssel, verschlüsselt unter Ihrer KEK, mit einem Manifest der
Tenant-spezifischen Chain-Tips.

Bewahren Sie die Passphrase in einer privaten Datei außerhalb des Checkouts und
des Images auf und halten Sie eine Kopie an einem sicheren Ort außerhalb dieses
Hosts vor: Ohne sie lässt sich kein Bundle wiederherstellen. Geben Sie dem
Backup-Container schreibgeschützten Zugriff darauf (das Image läuft mit UID `65532`):

```bash
sudo install -d -o 65532 -g 65532 -m 0700 /srv/olivares-dr
sudo install -o 65532 -g 65532 -m 0400 /path/to/private-passphrase /srv/olivares-dr/dr-pass

BACKUP_TS="$(date -u +%Y%m%dT%H%M%SZ)" \
docker compose -f deploy/compose/docker-compose.yml \
               -f deploy/compose/docker-compose.backup.yml \
               -f - --profile backup run --rm backup <<'YAML'
services:
  backup:
    volumes:
      - /srv/olivares-dr/dr-pass:/run/secrets/dr-pass:ro
YAML
```

Der Job teilt sich das Daten-Volume der Engine, schreibt das Bundle in das `olivares-backups`-
Volume und überlässt die Aufbewahrung dem Host: Bereinigen Sie alte
Bundles mit einem Host-Cron (`find <backups> -name '*.drbundle' -mtime +14 -delete`). Verpacken Sie
den Lauf in Host-Cron für ein geplantes RPO und **spiegeln Sie das `olivares-backups`-Volume
offsite** — ein Backup auf demselben Host ist keine Disaster Recovery. Wiederherstellen und verifizieren mit:

```bash
olivares dr restore --in <bundle> --data-dir <dir> --passphrase-file /path/to/private-passphrase
```

Das vollständige RPO/RTO-, Key-Custody- und DR-Drill-Verfahren liegt beim DR-
Runbook des Repositorys; der übergeordnete Walkthrough ist [Sichern und wiederherstellen](/de/how-to/backup-and-restore/).

## 5. Betriebshinweise

**Prüfen Sie die Health vom Host aus, nicht vom Container.** Das Image
definiert absichtlich keinen In-Container-`HEALTHCHECK`.
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
der auf Loopback beschränkten Engine (`OLIVARES_BIND=127.0.0.1`) einen Reverse-Proxy voran, der TLS mit einem
operatorbereitgestellten Zertifikat terminiert (von Ihrer CA oder ACME), und lassen den Proxy das einzige
sein, was im Netzwerk exponiert ist.

Weil die Engine selbst TLS spricht, verbindet sich der Proxy über HTTPS auf dem
beschränkten Port mit ihr. Ein minimaler nginx-Server-Block:

```nginx
server {
  listen 443 ssl;
  server_name olivares.example.com;

  ssl_certificate     /etc/ssl/olivares/fullchain.pem;   # operator-provided cert
  ssl_certificate_key /etc/ssl/olivares/privkey.pem;

  location / {
    proxy_pass         https://127.0.0.1:8443;   # engine restricted to loopback, own TLS
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
      tls_insecure_skip_verify   # the engine's cert is self-signed
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
docker pull docker.io/olivaresai/olivares:<new-version>

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

Veränderbare Tags (<!-- release -->`:0.1`<!-- /release -->, `:latest`) sind für die Evaluierung. In Produktion pinnen Sie den
**Digest**, den Sie verifiziert haben — ein Digest ist unveränderlich und ist genau das, was Sie abgesegnet haben:

```bash
docker run ... docker.io/olivaresai/olivares@sha256:<digest> serve ...
```

Für Compose setzen Sie die Digest-Referenz in `deploy/compose/.env`:

```bash
OLIVARES_IMAGE=docker.io/olivaresai/olivares@sha256:<digest>
```

Für Scale-out und Multi-Node verwenden Sie das Chart aus `./business-chart` und
pinnen das veröffentlichte Image per Digest. Die OCI-Veröffentlichung des Charts ist unbestätigt (`publication-unverified`): Aus diesem Repository wurde es nie veröffentlicht, und die Registry-Seite ist nicht beobachtbar.
Siehe [Die Control Plane selbst hosten](/de/how-to/self-hosting/) für den Quellbefehl und
[In einer Air-Gapped-Umgebung installieren](/de/how-to/air-gap-install/) für vollständig
getrennte Standorte.
