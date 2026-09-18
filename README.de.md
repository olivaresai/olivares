<div align="center">

<a href="https://olivares.ai"><img src=".github/assets/olivares-banner.png" alt="Olivares AI — Ground Truth für KI im Unternehmen" width="720"></a>

**Sprachen:** [English](./README.md) · [Español](./README.es.md) · [简体中文](./README.zh.md) · [Русский](./README.ru.md) · [日本語](./README.ja.md) · **Deutsch** · [Français](./README.fr.md)

**Betreiben und steuern Sie die KI, die Sie bereits nutzen — auf Ihrer eigenen Infrastruktur, mit einer Ground Truth.**

[Was es ist](#was-es-ist) · [Was es tut](#was-es-tut) · [Installation](#installation) · [Schnellstart](#schnellstart) · [Konsole](#ein-blick-in-die-konsole) · [Editionen](#editionen-und-preise) · [Dokumentation](#dokumentation) · [Sicherheit](#sicherheit) · [olivares.ai](https://olivares.ai)

[![License: AGPL-3.0-only](https://img.shields.io/badge/license-AGPL--3.0--only-blue)](LICENSING.md)
[![SDK & connectors: Apache-2.0](https://img.shields.io/badge/SDK%20%26%20connectors-Apache--2.0-blue)](LICENSING.md)
[![Release: v26.9.0](https://img.shields.io/badge/release-v26.9.0-28282B)](https://github.com/olivaresai/olivares/releases/tag/v26.9.0)
[![Status: beta](https://img.shields.io/badge/status-beta-F08000)](CHANGELOG.md)
[![Contributor Covenant](https://img.shields.io/badge/Contributor%20Covenant-2.1-4baaaa)](CODE_OF_CONDUCT.md)

</div>

> **Beta**, in aktiver Entwicklung. **v26.9.0** wird mit signierten Archiven, nativen Paketen und Container-Images ausgeliefert. Was heute läuft, was on-demand verfügbar ist und was sich im Design-Stadium befindet, steht unter [Ehrlichkeit & Grenzen](docs-site/src/content/docs/start/honesty-and-limits.md).

## Was es ist

Ihr KI-Estate heute sind Coding-Agenten, MCP-Server, Modell-Endpoints, Dienstkonten und geplante Jobs, verteilt über Maschinen, die nie ein System waren. Niemand kann von einem Ort aus sagen, was läuft, wer es gestartet hat, worauf es zugegriffen hat, was es gekostet hat und wer dem zugestimmt hat.

Olivares AI ist **eine self-hosted Go-Binary, Konsole inklusive**, die diesen Estate zusammenhält: Sie gibt der KI, was sie zum Arbeiten braucht (Kontext, Zugriff auf Ressourcen, verwaltete Sitzungen), und gibt Ihnen die Berechtigungen, Richtlinien, Budgets und Nachweise, um ihn zu betreiben. Self-hosted, keine verpflichtende Telemetrie, Air-Gap-Installationen unterstützt.

Claude Code ist auf der tiefsten Ebene integriert (der Hook `PreToolUse`/`PostToolUse`, verwaltete Einstellungen, Start und Stopp aus der Konsole); die offiziellen CLIs von Codex und Grok sind Session-Treiber erster Klasse; gemini-cli, Cursor, opencode, goose, cline, OpenHands, OpenClaw, Hermes und self-hosted Endpoints wie Ollama sind Connectors, jeder mit der Angabe, was er durchsetzen und was er nur beobachten kann. Der AGPL-Build ist das gesamte Produkt, niemals von innen durch Feature-Caps begrenzt; kein Plan zählt Benutzer.

## Was es tut

<div align="center">
<img src=".github/assets/motion-access-map.gif" width="840" alt="Bewegtes Diagramm der Lese-/Schreib-Access-Map: Agenten, Sitzungen und Identitäten links, die Ressourcen, die sie erreichen, rechts, Lesezugriffe in Blau, Schreibzugriffe in Orange, ein beobachteter Schreibzugriff, der nie erlaubt war, als Drift-Fund markiert.">
<br><sub><b>Die Access Map</b> — was jeder Agent in Ihrem Estate liest und schreibt, Erlaubt gegen Beobachtet.</sub>
</div>

- **Sehen Sie es.** Inventar jedes entdeckten Agenten, jeder Sitzung, jedes Modells, MCP-Servers, Tools und jeder Identität; eine Lese-/Schreib-**Access Map** mit einer Permitted-vs-Observed-**Drift**-Ansicht; Live-Sitzungen, der Orchestrierungsgraph, Gesundheit und SLA. Was sie nicht sehen kann, wird als `unknown` markiert, niemals geraten.
- **Führen Sie die Arbeit aus.** Dauerhafte Arbeitselemente mit Eigentümerschaft, Abhängigkeiten, Abnahmekriterien und Entscheidungen; abgegrenzte Leases, sodass zwei Agenten nicht dieselbe Arbeit gleichzeitig halten können; Sitzungen von Claude Code, Codex und Grok, die aus der Konsole gestartet, angehängt, unterbrochen und gestoppt werden; Delegation an autorisierte Peers über A2A.
- **Steuern und durchsetzen.** Eine Cedar-Autorisierungs-Engine und **vier deny-closed Enforcement Points** — der Claude-Code-Hook, ein Inline-Inferenzproxy `/v1/messages`, ein MCP-`tools/call`-Gate und ein A2A-Delegations-Gate —, sodass eine unautorisierte Aktion blockiert, für die Freigabe durch zwei Personen gehalten oder umgeschrieben wird, bevor sie läuft. Budgets, die Ausgaben verweigern oder drosseln, Break-Glass mit Dual Control und ein Estate-**Kill-Switch**, der geschlossen fehlschlägt.
- **Speisen Sie es, gesteuert.** Content-Quellen (SharePoint, Confluence, Google Drive, Notion, Salesforce, Snowflake, S3, Azure AI Search, SAP OData, PostgreSQL, ein auf die Wurzel beschränktes Dateisystem) in gesteuerte Retrieval, Clearance deny-closed zum Zeitpunkt des Retrievals durchgesetzt.
- **Beweisen Sie es.** Ein hash-chained, Ed25519-signiertes Audit-Ledger; versiegelte Nachweise, abgebildet auf **26 Framework-Kataloge** (EU AI Act, NIST AI RMF, ISO 42001, SOC 2, ISO 27001, GDPR…) — selbst bewertete Kontrollfamilien, keine Zertifizierungen; SIEM/ITSM-Push (CEF/LEEF/syslog/OTLP/OCSF); WebAuthn/FIDO2, PIV/CAC, SSO, SCIM, BYOK/CMEK und verifiziertes Recht auf Löschung, konfiguriert pro Deployment.

**30 Module**, eine Konsole, **158 Integrationen** — Zahlen, die aus dem Code abgeleitet und bei jedem Push von [`scripts/check-public-counts.sh`](scripts/check-public-counts.sh) durchgesetzt werden; die Aufschlüsselung steht in [`connectors/README.md`](connectors/README.md), jedes Modul mit seinem Reifegrad im [Modulkatalog](docs-site/src/content/docs/reference/modules/overview.md).

## Installation

Wählen Sie eine Methode: ein Befehl installiert, danach gibt `olivares quickstart` die Konsolen-URL und das einmalige Setup-Token aus. Jedes Release ist mit cosign signiert, mit SLSA-Provenienz und SBOMs; jeder Pfad unten verifiziert, bevor er installiert, und `scripts/verify-release.sh` prüft einen manuellen Download (cosign + SHA-256, [wie](INSTALL.md#verifying-a-release)). Die Engine ist **standardmäßig sicher**: Loopback-Bind, HTTPS beim ersten Start, keine Default-Credentials, ein einmaliges Setup-Token, das beim ersten Start ausgegeben wird.

**1 · Ein Befehl, Linux und macOS** — der verifizierte Installer: erkennt OS und Architektur, verifiziert die signierten Checksums und die Archiv-SHA-256, installiert nur die Binary, führt niemals `sudo` aus.

```sh
curl -fsSL https://olivares.ai/olivares/install.sh | sh
olivares quickstart        # prints the console URL and the one-time setup token
```

Fügen Sie `--user` für einen Benutzerdienst hinzu (systemd-User-Unit oder LaunchAgent), oder führen Sie das verifizierte Skript aus einer privilegierten Shell mit `--system --start` für einen Systemdienst aus. Lieber herunterladen, verifizieren und von Hand ausführen? Der manuelle Binary-Pfad und die Matrix pro OS: [`INSTALL.md`](INSTALL.md).

**2 · Docker** — Multi-Arch, distroless, non-root; auf jeder Host-Schnittstelle veröffentlicht (stellen Sie den `-p`-Zuordnungen `127.0.0.1:` voran, um es lokal zu halten).

```sh
docker run -d --name olivares -p 8443:8443 -p 8444:8444 \
  -v olivares-data:/var/lib/olivares \
  docker.io/olivaresai/olivares \
  serve --listen :8443 --grpc-listen :8444 --data-dir /var/lib/olivares
```

`ghcr.io/olivaresai/olivares` ist per Digest dasselbe Image; in Produktion nach Digest pinnen. FIPS- und STIG-Image-Varianten: [`INSTALL.md`](INSTALL.md#docker).

**3 · Docker Compose** — ein gehärteter Stack, SQLite Single-Node mit optionalem Postgres und Backup.

```sh
git clone --depth 1 https://github.com/olivaresai/olivares.git && cd olivares
docker compose -f deploy/compose/docker-compose.yml up --wait --wait-timeout 120
```

**4 · Kubernetes** — das Helm-Chart aus dem Baum oder ein flaches Helm-freies Manifest; das Chart ist noch nicht in einer OCI-Registry veröffentlicht.

```sh
helm install olivares deploy/helm/olivares -n olivares-system --create-namespace
# or, Helm-free
kubectl create namespace olivares-system && kubectl apply -n olivares-system -f deploy/manifests/install.yaml
```

**5 · Linux-Pakete** — `.deb`, `.rpm`, `.apk` von der [Release-Seite](https://github.com/olivaresai/olivares/releases/tag/v26.9.0): die Binary, eine Beispiel-Env-Datei, ein `olivares`-Benutzer ohne Login und eine gehärtete Unit; der Dienst wird nicht für Sie gestartet.

```sh
sudo dpkg -i olivares_*_linux_amd64.deb        # Debian / Ubuntu   (sudo rpm -i … on RHEL / Fedora / SUSE; sudo apk add --allow-untrusted … on Alpine)
sudo systemctl enable --now olivares           # OpenRC hosts: sudo rc-service olivares start
```

**6 · Homebrew** — macOS und Linux, gegen die signierten Checksums geprüft.

```sh
brew install olivaresai/tap/olivares && olivares quickstart
```

**7 · Aus dem Quellcode** — Go 1.26+, [Task](https://taskfile.dev), pnpm.

```sh
task build && ./bin/olivares quickstart
```

**Air-gapped**: bündeln Sie das signierte Image, das Chart und das Verifikationsmaterial und verifizieren Sie offline mit `scripts/verify-release.sh --key … --offline` ([Anleitung](docs-site/src/content/docs/how-to/air-gap-install.md)). **Windows** ist noch nicht gebaut: führen Sie den Linux-Container oder WSL2 aus ([Plan](INSTALL.md#windows)). Upgrades und Rollback: [Anleitung](docs-site/src/content/docs/how-to/upgrade-and-rollback.md).

## Schnellstart

```sh
# a deterministic demo estate — loopback-only (the demo password is public), no real data
olivares serve --seed-demo --insecure --listen 127.0.0.1:8901 --grpc-listen 127.0.0.1:8902 --data-dir "$(mktemp -d)"
# open http://127.0.0.1:8901 — inventory, work, orchestration, access map + drift, policies, FinOps

# the real thing — TLS on, reachable from your network; create the first administrator with the printed token
olivares quickstart
```

Der Demo-Seed ist nur zum Lernen da (öffentliches Passwort im Quellbaum): niemals auf echte Daten richten. CI durchläuft denselben Pfad mit `task smoke:quickstart` und prüft die Access-Map- und Drift-Zahlen (20 Knoten / 13 Kanten, mit 8 unerwarteten Zugriffen und 2 ungenutzten Grants). Der [vollständige Schnellstart](docs-site/src/content/docs/start/quickstart.md) verdrahtet einen echten pgAudit-Connector.

## Ein Blick in die Konsole

| | |
|---|---|
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/access-map-dark.png"><img src="docs-site/public/console/access-map-light.png" alt="Access Map: was jeder Agent in Ihrem Estate liest und schreibt, Ursprünge links, Ressourcen rechts."></picture><br><sub><b>Access Map</b> — Ursprünge links, Ressourcen rechts, Lesen und Schreiben nach Farbe.</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/access-map-drift-dark.png"><img src="docs-site/public/console/access-map-drift-light.png" alt="Least-Privilege-Drift: unerwartete Zugriffe und ungenutzte Grants über der Access Map."></picture><br><sub><b>Least-Privilege-Drift</b> — beobachtet, aber nicht erlaubt, und Grants, die niemand nutzt.</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/agentops-dark.png"><img src="docs-site/public/console/agentops-light.png" alt="Claude-Code-Sitzungen, die aus der Konsole erstellt, angehängt und gesteuert werden."></picture><br><sub><b>Sitzungen</b> — Sitzungen aus der Konsole erstellen, anhängen und steuern, ohne SSH.</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/work-dark.png"><img src="docs-site/public/console/work-light.png" alt="Arbeit: der dauerhafte sitzungsübergreifende Backlog aus Arbeitselementen und Entscheidungen."></picture><br><sub><b>Arbeit</b> — der dauerhafte sitzungsübergreifende Backlog: Elemente, Eigentümerschaft, Abnahme, Entscheidungen.</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/security-dark.png"><img src="docs-site/public/console/security-light.png" alt="Sicherheit und Forensik: Guardrail-Funde, die Anomalie-Warteschlange und manipulationssichere Forensik."></picture><br><sub><b>Sicherheit &amp; Forensik</b> — Guardrail-Funde, Anomalien, manipulationssichere Forensik.</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/finops-dark.png"><img src="docs-site/public/console/finops-light.png" alt="FinOps: Modellausgaben, Token-Nutzung, Budgets und eine Run-Rate-Projektion."></picture><br><sub><b>FinOps</b> — Ausgaben nach Modell und Agent, Budgets, die verweigern oder drosseln, Run-Rate.</sub> |

Jedes Standbild ist eine Aufnahme des gesäten Demo-Estates, den die laufende Binary ausliefert. Die vollständige Karte der Bildschirme: die [Konsolenreferenz](docs-site/src/content/docs/reference/console.md).

## Editionen und Preise

Der AGPL-Build ist die gesamte Plattform, niemals von innen durch Feature-Caps begrenzt. Kommerzielle Add-ons sind additiver Code darüber, niemals entfernte Funktionen; ein Abonnement ist das Credential zum Herunterladen signierter Modulpakete. Benutzerkonten sind in der self-hosted Engine unbegrenzt, und alle **vier deny-closed Enforcement Points** sind offen.

| Edition | Für wen | Was sie hinzufügt |
|---|---|---|
| **Community** | Jeder. Kostenlos, AGPL-3.0, unbegrenzte Benutzer. | Das vollständige Produkt, self-hosted. Kein Lizenz-Gate auf dem Kern. |
| **Business** | Eine Organisation, die es übernimmt. Preis pro Deployment, niemals pro Sitz. | Dienste und optionale Packs, keine Kernfunktionen: die kommerzielle Lizenz, ein gepflegter signierter Release-Kanal, E-Mail-Support zu Geschäftszeiten und vier optionale Add-ons: **Regulated Operations**, **Compliance Packs**, **AI Runtime Security** und **Identity & Scale** (enthält das Session-Cockpit für die offiziellen Tools). Alle vier zusammen sind **Business Max**. |
| **Cloud** | Teams, die dieselbe Ebene für sich betreiben lassen wollen, prepaid, auf geteilter Infrastruktur. | Eine verwaltete Control Plane mit veröffentlichten Obergrenzen. Kein Cloud-Trial; die kostenlose Option bleibt self-hosted Community. |
| **Enterprise** | Regulierte, multi-entity, großskalige Estates. | Ein Vertrag, per E-Mail vereinbart und auf einem jährlichen Bestellformular unterzeichnet. |

Preise, die Add-on-Matrix und die Kaufbedingungen: [olivares.ai/pricing](https://olivares.ai/pricing). Die Matrix offen/kommerziell/geplant: [`LICENSING.md`](LICENSING.md).

## Architektur

Eine statische Go-Binary bettet die Konsole ein und stellt vier Oberflächen bereit: die REST-API (primär), einen fokussierten gRPC-Spiegel des stabilen Kerns, die `olivares`-CLI und einen Terraform-Provider. Collectors laufen in Ihrer Infrastruktur; der Store ist SQLite oder Postgres mit Row-Level Security, einmal in der Store-API und erneut durch Postgres durchgesetzt. Das vollständige Bild, Arbeitsebene inklusive: [`ARCHITECTURE.md`](ARCHITECTURE.md).

## Dokumentation

[docs.olivares.ai](https://docs.olivares.ai) — getestete Installations-Tutorials (Single Node, Docker Compose, Kubernetes/Helm, air-gapped), Connector-Anleitungen mit echten Konsolen-Captures, ein Cookbook (deny-closed Richtlinien, Budgets, Freigaben, Kill-Switch-Übungen, SIEM-Push), API-Referenz und ein Glossar. Beginnen Sie bei [Was ist Olivares AI](docs-site/src/content/docs/start/what-is-olivares-ai.md). Auf der Website: [Produkt](https://olivares.ai/product) · [Lösungen](https://olivares.ai/solutions) · [so funktioniert es](https://olivares.ai/how-it-works) · [Architektur](https://olivares.ai/architecture) · [Sicherheit](https://olivares.ai/security) · [Vertrauen](https://olivares.ai/trust) · [Vergleich](https://olivares.ai/compare) · [Demo](https://olivares.ai/demo) · [Changelog](https://olivares.ai/changelog) · [Status](https://olivares.ai/status) · [Roadmap](https://olivares.ai/roadmap) · [Marke](https://olivares.ai/brand) · [Presse](https://olivares.ai/press). Releases: [GitHub](https://github.com/olivaresai/olivares/releases) · [`CHANGELOG.md`](CHANGELOG.md).

## Sicherheit

Melden Sie eine Schwachstelle privat über [`SECURITY.md`](SECURITY.md), niemals als öffentliches Issue. Die Engine ist read-first und datensparsam: Die Access Map speichert Kanten, keine Payloads, und das Öffnen ist eine aufgezeichnete Aktion. Eine Lizenzprüfung ruft uns niemals an; der AGPL-Kern macht keinen Lizenz-Call. Advisory-Ablauf: [`docs/security-advisories.md`](docs/security-advisories.md); Lieferkettennachweise: [`docs/openssf-badge.md`](docs/openssf-badge.md).

## Community

[`CONTRIBUTING.md`](CONTRIBUTING.md) (Setup, DCO/CLA, SPDX, die Connector-Grenze) · [`CODE_OF_CONDUCT.md`](CODE_OF_CONDUCT.md) · [`SUPPORT.md`](SUPPORT.md) · [`GOVERNANCE.md`](GOVERNANCE.md) · [`CHANGELOG.md`](CHANGELOG.md) (Keep a Changelog, CalVer `vYY.M.PATCH`).

## Lizenz

`core/`, `modules/` und `web/` sind **AGPL-3.0-only**; `sdk/`, `connectors/` und `clients/` sind **Apache-2.0**, und ein Connector importiert niemals die Engine. Die kommerziellen Add-ons sind separat, optional und geschlossen — ausschließlich mit `-tags enterprise` gebaut, niemals in diesem Repository; kommerzielle Lizenzierung: `enterprise@olivares.ai` — [`LICENSING.md`](LICENSING.md). Beiträge erfordern ein DCO-Sign-off (`git commit -s`) und das [CLA](CLA.md).

> **Keine Gewährleistung, keine Haftung.** Die Software wird **wie besehen** bereitgestellt, **ohne Gewährleistung jeglicher Art** und **ohne Haftung für Datenverlust, Betriebsunterbrechung oder entgangenen Gewinn**. Bei einer Control Plane ist das keine Formalie: Eine Fehlkonfiguration kann legitime Arbeit blockieren oder genau das durchlassen, was Sie stoppen wollten. Es gelten AGPL-3.0-only §§15–16, Apache-2.0 §§7–8 sowie die projekteigene Zusatzbestimmung — [`DISCLAIMER.md`](DISCLAIMER.md).

## Das Projekt unterstützen

Der Kern ist frei und bleibt frei; jedes Release signiert, verifiziert und aktuell zu halten, ist dauerhafte Arbeit. Unterstützen Sie es über GitHub Sponsors — [github.com/sponsors/olivaresai](https://github.com/sponsors/olivaresai) oder [github.com/sponsors/fran-olivares](https://github.com/sponsors/fran-olivares) — oder einmalig über Ko-fi. Sponsoring ist kein Support-Vertrag ([`SUPPORT.md`](SUPPORT.md)); Sponsoren, die namentlich genannt werden möchten, werden in [`SUPPORTERS.md`](SUPPORTERS.md) aufgeführt.

[![ko-fi](https://ko-fi.com/img/githubbutton_sm.svg)](https://ko-fi.com/Z1R625SAD2)

---

<div align="center">

<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/olivares-mark-dark.svg">
  <img src=".github/assets/olivares-mark-light.svg" alt="Olivares AI" width="44">
</picture>

<sub><strong>Ground Truth für KI im Unternehmen.</strong> · <a href="https://olivares.ai">olivares.ai</a> · <a href="LICENSING.md">AGPL-3.0 + commercial</a></sub>

</div>
