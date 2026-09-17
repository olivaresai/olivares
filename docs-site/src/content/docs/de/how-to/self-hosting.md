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

### Benutzerdefiniertes Datenverzeichnis (`layout: custom`)

Das Standard-Native-Layout ist `/var/lib/olivares`. Der signierte Service-Adapter
(`install.sh --data-dir`, `scripts/install-service.sh`) lässt ein
**benutzerdefiniertes** Datenverzeichnis nach **Form** zu, nicht nach Allowlist.
Das Ownership-Manifest zeichnet `"layout": "custom"` auf (`CHANGELOG.md`
`[26.9.0]` Added; `INSTALL.md`).

SDD 04 §6: jedes konfigurierbare Feld erklärt Owner, Schema, akzeptierte Quellen
und Validator. Hier besitzt der Adapter das dedizierte Verzeichnis; der Operator
besitzt das Elternverzeichnis. Das sind die eigenen Ablehnungstexte des Adapters
(`scripts/install-service.sh`):

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

`install-agentops.sh` verwendet dieselbe Zwei-Ebenen-Regel für `OLIVARES_DATA_DIR`:
`OLIVARES_DATA_DIR must name a dedicated directory at least two levels deep
(for example /srv/olivares), not a top-level directory`. Es beachtet `OLIVARES_DATA_DIR` und ein ausdrücklich gewähltes
`OLIVARES_WORKSPACE_DIR`.

Ein Pfad unter `/home`, `/root` oder `/run/user` wird mit `ProtectHome=tmpfs` und
`BindPaths=` genau für dieses Verzeichnis gerendert. Einer unter `/tmp` oder
`/var/tmp` behält `PrivateTmp=true` und erhält `BindPaths=` nur für dieses
Verzeichnis (`sandbox_access` in `scripts/install-service.sh`).

`olivares uninstall` lässt dieses benutzerdefinierte Verzeichnis nur zu, wenn die
Unit an ihrem indexierten Pfad die Engine damit ausführt, oder wenn ein Preserve
bereits einen Uninstall-Zeugen neben der Service-Konfiguration hinterlassen hat.
Diagnostizieren Sie das aufgezeichnete AgentOps-Layout mit `olivares doctor` —
siehe [Fehlerbehebung](/how-to/troubleshooting/#agentops-layout-check).

Für Paketinstallationen bleibt der Standard `/var/lib/olivares`. Siehe
[Aus einem Paket installieren](/how-to/install-from-packages/). Unter macOS siehe
[Mit Homebrew installieren](/how-to/install-from-homebrew/).

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

Das Helm-Chart in `deploy/helm/olivares` stellt die control plane als **Core-StatefulSet** bereit
(Single-Writer; sein Datenverzeichnis enthält den Signierschlüssel des Audit-Ledgers und
das TLS-Material) und, für die verteilte Topologie, ein **Collectors-DaemonSet**, das
Beobachtungen über **gRPC + mTLS** an den Core schickt. Das Engine-Release v26.9.0
veröffentlicht das Chart nicht in einer OCI-Registry; noch kein unabhängiger
`chart-v*`-Tag hat den Workflow ausgeführt. Installieren Sie aus dem Checkout und
pinnen Sie das veröffentlichte Container-Image per Digest.

```bash
helm install olivares \
  deploy/helm/olivares \
  --set image.repository=docker.io/olivaresai/olivares \
  --set image.digest=<sha256-digest>
```

> Wenn ein Chart veröffentlicht wird, signiert `release-chart.yml` dessen OCI-Manifest mit
> cosign und erzeugt keine GPG-`.prov`-Ebene. Dieses spätere Artefakt muss per Digest geprüft
> werden; die Quellinstallation ist kein signierter OCI-Download. Siehe `deploy/helm/README.md`.

Das Chart zieht das Container-Image von Docker Hub (`docker.io/olivaresai/olivares`); dasselbe
Image liegt auch unter `ghcr.io/olivaresai/olivares`, per Digest identisch; zeigen Sie
`image.repository` dorthin, wenn die Rate-Begrenzung **anonymer** Pulls von Docker Hub
stört (ghcr.io wendet sie auf öffentliche Images nicht an). Das Chart kommt aus
`deploy/helm/olivares`, bis eine eigene Chart-Version veröffentlicht wird.

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
