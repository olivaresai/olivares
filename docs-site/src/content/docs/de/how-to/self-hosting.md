---
title: Olivares AI selbst hosten
description: >-
  Betreiben Sie Olivares AI selbst — als einzelne Binärdatei, mit Docker Compose
  oder Kubernetes — mit sicheren Standardeinstellungen: keine Standard-Anmeldedaten,
  ein einmaliger Setup-Token, TLS standardmäßig aktiviert, keine verpflichtende Telemetrie
  und standardmäßig kein Egress der Control Plane. Ihren Perimeter überschreitet nur, was
  Sie dafür konfigurieren, etwa Aufrufe an Ihre Modell-APIs und die von Ihnen eingerichteten
  SIEM-/Webhook-Ausgaben.
---

Olivares AI ist **self-host-first**. Das gesamte Produkt ist eine statische Binärdatei
mit eingebetteter Web-UI, sodass die einfachste Bereitstellung eine einzige Datei ist;
für Multi-Node und Produktion existieren Compose- und Kubernetes-Wege. Jeder Weg teilt
dieselben sicheren Standardeinstellungen — keine Standard-Anmeldedaten, ein einmaliger
Setup-Token, TLS standardmäßig aktiviert — sowie keine verpflichtende Telemetrie und
standardmäßig kein Egress der Control Plane. Ihren Perimeter überschreitet nur, was **Sie**
dafür konfigurieren: Aufrufe an Ihre Modell-APIs, die von Ihnen eingerichteten
SIEM-/Webhook-Ausgaben und ein externer Embedding-Anbieter, falls Sie einen bereitstellen.

Dieser Leitfaden ist die **Entscheidungsseite** zur Bereitstellung — die Optionen und
ihre sicheren Standardeinstellungen auf einen Blick. Für die schrittweise Installation
jedes Szenarios führen die Erste-Schritte-Tutorials jeden Weg von Anfang bis Ende durch:
[Single Node (systemd)](/tutorials/getting-started/single-node/) ·
[Docker Compose](/tutorials/getting-started/docker-compose/) ·
[Kubernetes/Helm](/tutorials/getting-started/kubernetes/) ·
[air-gapped](/tutorials/getting-started/air-gapped/). Um die Artefakte zuerst
kryptografisch zu verifizieren, siehe [Überprüfen, was Sie heruntergeladen haben](/how-to/verify-a-release/);
für getrennte Standorte siehe
[Installation in einer air-gapped-Umgebung](/how-to/air-gap-install/).

## Sichere Standardeinstellungen (alle Wege)

| Standard | Verhalten |
|---|---|
| **Anmeldedaten** | keine. Der erste Start gibt einen **einmaligen, nur einmal verwendbaren Setup-Token** aus (`olst_…`); damit erstellen Sie den ersten Administrator. |
| **TLS** | standardmäßig aktiviert. `--insecure` (Klartext) ist nur für die lokale Entwicklung auf localhost gedacht. |
| **Bind** | die Binärdatei bindet standardmäßig an **loopback**; geben Sie sie bewusst frei. |
| **Lizenz** | Im offenen (AGPL-)Binary wird die Lizenz **offline** validiert (Ed25519) und dient nur der Attestierung — sie sperrt oder degradiert das offene Produkt niemals, und das ändert sich nicht. Kommerzielle Add-ons sind ein Recht für die bezahlte Laufzeit, das als **Zugang per Abonnement zu den Enterprise-Repositorys** bereitgestellt wird (das SUSE/Novell-Modell): Für den Bezug der Add-ons und ihrer Updates — einschließlich Sicherheitsupdates — ist dieses Entitlement erforderlich. Air-gapped-Umgebungen werden wie bei SUSE über einen lokalen Mirror versorgt, für den das Entitlement weiterhin gilt. |
| **Telemetry-home** | aus. Die Engine führt beim Start keine verpflichtenden ausgehenden Aufrufe durch. |

## Option 1 — einzelne Binärdatei

Erstellen Sie das eine statische Artefakt (reiner Go-SQLite-Store, also keine C-Toolchain) und führen Sie es aus:

```bash
task build                      # compiles ./bin/olivares with the web embedded
./bin/olivares serve \
  --listen 127.0.0.1:8443 \
  --grpc-listen 127.0.0.1:8444 \
  --data-dir /var/lib/olivares
```

Beim ersten Start gibt die Engine das Setup-Banner aus:

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

Erstellen Sie den ersten Administrator und melden Sie sich dann an:

```bash
curl -fsS -X POST https://localhost:8443/v1/setup \
  -H 'Content-Type: application/json' \
  -d '{"token":"<olst_ token>","email":"you@example.com","password":"<strong-password>"}'

curl -fsS -X POST https://localhost:8443/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"email":"you@example.com","password":"<strong-password>"}'
```

Das Datenverzeichnis enthält die SQLite-Datenbank, den Signierschlüssel des Audit-Ledgers
und das TLS-Material — sichern und schützen Sie es.

## Option 2 — Docker Compose (Single Node, SQLite)

Das Repository liefert einen Compose-Stack mit:

```bash
docker compose -f deploy/compose/docker-compose.yml up -d

# Read the one-time first-boot setup token from the logs:
docker compose -f deploy/compose/docker-compose.yml logs olivares | sed -n '/FIRST-BOOT SETUP/,/========================/p'

# Then open https://localhost:8443 (self-signed TLS by default)
```

Für ein mandantenfähiges Postgres-Backend setzen Sie die Passwörter und legen das
Postgres-Override darüber:

```bash
cp deploy/compose/.env.example deploy/compose/.env     # set the two passwords
docker compose -f deploy/compose/docker-compose.yml \
               -f deploy/compose/docker-compose.postgres.yml up -d
```

:::note[Der Container-Standard bindet innerhalb des Containers]
Der Standardbefehl des Containers bindet `0.0.0.0` *innerhalb des Containers*, sodass Sie
ihn hinter Ihrem Ingress vorschalten können; der Compose-Stack mappt den Host-Port auf
`127.0.0.1`. Es gibt kein nacktes `docker run`-Rezept — verwenden Sie Compose (oder das
Helm-Chart), damit Datenvolume, Ports und Erste-Start-Ablauf korrekt verdrahtet sind.
:::

## Option 3 — Kubernetes (Helm)

Das signierte Helm-Chart stellt die control plane als **Core-StatefulSet** bereit
(Single-Writer; sein Datenverzeichnis enthält den Signierschlüssel des Audit-Ledgers und
das TLS-Material) und, für die verteilte Topologie, ein **Collectors-DaemonSet**, das
Beobachtungen über **gRPC + mTLS** an den Core schickt. Beim Release wird das Chart in
eine OCI-Registry veröffentlicht und cosign-signiert, sodass Sie bei der Installation
verifizieren und per Digest pinnen. (Das erste Release ist noch ein **Entwurf**: bis ein
`chart-v*`-Tag geschnitten ist, ist der Registry-Pfad leer, daher ist der untenstehende
Befehl der Weg, den Sie nutzen werden, sobald ein Release veröffentlicht ist.)

```bash
helm install olivares \
  oci://ghcr.io/olivaresai/charts/olivares \
  --version <chart-version> \
  --set image.repository=docker.io/olivaresai/olivares \
  --set image.digest=<sha256-digest>
```

> Das veröffentlichte Chart ist **cosign-signiert über das OCI-Manifest**, nicht GPG-signiert: die
> Release-Pipeline erzeugt keine `.prov`-Ebene, daher kann `helm --verify` es nicht prüfen. Mit
> `cosign verify` gegen die Identität `release-chart.yml@refs/tags/chart-v*` verifizieren — siehe
> `deploy/helm/README.md`.

Das Chart zieht das Container-Image von Docker Hub (`docker.io/olivaresai/olivares`); dasselbe
Image liegt auch unter `ghcr.io/olivaresai/olivares`, per Digest identisch; zeigen Sie
`image.repository` dorthin, wenn die Rate-Begrenzung **anonymer** Pulls von Docker Hub
stört (ghcr.io wendet sie auf öffentliche Images nicht an). Das
**Chart**-Artefakt selbst bleibt unter `oci://ghcr.io/olivaresai/charts/olivares`.

Stellen Sie immer **per Digest** bereit, niemals über einen veränderlichen Tag. Für ein
vollständig getrenntes Cluster spiegeln Sie zuerst das Bundle — siehe
[Air-Gap-Installation](/how-to/air-gap-install/).

## Eine Topologie wählen

| Topologie | Wann | Store | Event-Bus |
|---|---|---|---|
| **Einzelne Binärdatei** | Single Node, Labor, kleine estate, air-gap | SQLite (eingebettet) | in-process |
| **Verteilt** | Multi-Host, Skalierung, mandantenfähig | Postgres + RLS | in-process + **NATS-Bridge** (`OLIVARES_BUS_CONFIG`; die knotenübergreifende Zustellung ist ehrlicherweise at-most-once) |
| **Air-gapped** | kein Egress erlaubt | SQLite oder Postgres | in-process (NATS-Bridge optional innerhalb des Perimeters) |

Die **Data Plane (Collectors) läuft immer auf Ihrer Infrastruktur** — die control plane
ist das Einzige, bei dem Sie wählen, wo Sie es hosten. Der
[Architekturüberblick](/explanation/architecture/overview/) erklärt die Abwägungen.

## Echte Quellen anbinden

Eine frische Installation hat eine leere estate. Verdrahten Sie echte Quellen (Postgres
pgAudit, CloudTrail, OpenTelemetry von Agenten, eBPF), damit sich die access map füllt —
siehe [eine Quelle anbinden](/how-to/connect-a-source/) und
[Claude Code anbinden](/how-to/connect-claude-code/). Für die Konfigurationsoberfläche
siehe die [Konfigurationsreferenz](/reference/configuration/).
