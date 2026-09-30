<div align="center">

<a href="https://olivares.ai"><img src=".github/assets/olivares-banner.png" alt="Olivares AI — Ground Truth für KI im Unternehmen" width="720"></a>

**Sprachen:** [English](./README.md) · [Español](./README.es.md) · [简体中文](./README.zh.md) · [Русский](./README.ru.md) · [日本語](./README.ja.md) · **Deutsch** · [Français](./README.fr.md)

**Betreiben und steuern Sie die KI, die Sie bereits nutzen, auf Ihrer eigenen Infrastruktur.**

[Was es ist](#was-es-ist) · [Was es tut](#was-es-tut) · [Installation](#installation) · [Schnellstart](#schnellstart) · [Konsole](#ein-blick-in-die-konsole) · [Editionen](#editionen-und-preise) · [Dokumentation](#dokumentation) · [Sicherheit](#sicherheit) · [olivares.ai](https://olivares.ai)

[![License: AGPL-3.0-only](https://img.shields.io/badge/license-AGPL--3.0--only-blue)](LICENSING.md)
[![SDK & connectors: Apache-2.0](https://img.shields.io/badge/SDK%20%26%20connectors-Apache--2.0-blue)](LICENSING.md)
[![Release: 26.10.0](https://img.shields.io/badge/release-26.10.0-28282B)](https://github.com/olivaresai/olivares/releases/tag/26.10.0)
[![Status: beta](https://img.shields.io/badge/status-beta-F08000)](CHANGELOG.md)
[![Contributor Covenant](https://img.shields.io/badge/Contributor%20Covenant-2.1-4baaaa)](CODE_OF_CONDUCT.md)

</div>

> **Beta.** 26.10.0 wird mit signierten Archiven, nativen Paketen und Container-Images ausgeliefert. [Ehrlichkeit & Grenzen](docs-site/src/content/docs/start/honesty-and-limits.md) zeigt, was heute läuft, was auf Anfrage läuft und was noch Entwurf ist.

## Was es ist

Olivares AI ist eine selbst gehostete Steuerungsebene für KI-Agenten: ein einzelnes Go-Binary mit integrierter Konsole. Es gibt Agenten Kontext, Zugriff auf Ressourcen und verwaltete Sitzungen, und es gibt Ihnen Berechtigungen, Richtlinien, Budgets und Nachweise. Es gibt keine verpflichtende Telemetrie, und Air-Gapped-Installationen werden unterstützt.

Claude Code wird über seinen `PreToolUse`/`PostToolUse`-Hook, verwaltete Einstellungen sowie Start und Stopp aus der Konsole angebunden. Die offiziellen CLIs von Codex und Grok laufen als verwaltete Sitzungen. gemini-cli, Cursor, opencode, goose, cline, OpenHands, OpenClaw, Hermes und selbst gehostete Endpunkte wie Ollama sind Konnektoren; jeder gibt an, was er durchsetzt und was er nur beobachtet.

## Was es tut

<div align="center">
<img src=".github/assets/motion-access-map.gif" width="840" alt="Bewegtes Diagramm der Lese-/Schreib-Access-Map: Agenten, Sitzungen und Identitäten links, die Ressourcen, die sie erreichen, rechts, Lesezugriffe in Blau, Schreibzugriffe in Orange, ein beobachteter Schreibzugriff, der nie erlaubt war, als Drift-Fund markiert.">
<br><sub><b>Die Access Map</b> — was jeder Agent in Ihrem Estate liest und schreibt, Erlaubt gegen Beobachtet.</sub>
</div>

- **Sehen.** Ein Inventar der Agenten, Sitzungen, Modelle, MCP-Server, Werkzeuge und Identitäten, die Konnektoren beobachten; eine Lese-/Schreib-**Zugriffskarte** mit einer **Drift**-Ansicht von Erlaubt und Beobachtet; Live-Sitzungen, der Orchestrierungsgraph, Zustand und SLA. Zugriff, den es nicht einordnen kann, erscheint als `unknown`.
- **Arbeit steuern.** Arbeitselemente mit Verantwortlichen, Abhängigkeiten, Abnahmekriterien und Entscheidungen; eingezäunte Leases, damit nicht zwei Agenten dasselbe Element halten; Sitzungen von Claude Code, Codex und Grok, die aus der Konsole gestartet, angehängt, unterbrochen und gestoppt werden; Delegation an autorisierte Peers über A2A.
- **Regeln und durchsetzen.** Eine Cedar-Autorisierungs-Engine und **vier deny-closed Enforcement Points**: der Claude-Code-Hook, ein Inline-Inferenz-Proxy für `/v1/messages`, ein MCP-`tools/call`-Gate und ein A2A-Delegations-Gate. Eine nicht autorisierte Aktion wird blockiert, bis zur Freigabe durch zwei Personen angehalten oder vor der Ausführung umgeschrieben. Budgets verweigern oder drosseln Ausgaben, Break-Glass braucht zwei Personen, und der **Kill-Switch** schlägt geschlossen fehl.
- **Kontrolliert versorgen.** SharePoint, Confluence, Google Drive, Notion, Salesforce, Snowflake, S3, Azure AI Search, SAP OData, PostgreSQL und ein auf sein Wurzelverzeichnis beschränktes Dateisystem speisen einen kontrollierten Abruf; die Freigabe wird zum Zeitpunkt des Abrufs geprüft.
- **Nachweisen.** Ein per Hash verkettetes, mit Ed25519 signiertes Audit-Ledger; versiegelte Nachweise, zugeordnet zu **26 Framework-Katalogen** (EU AI Act, NIST AI RMF, ISO 42001, SOC 2, ISO 27001, DSGVO und weitere) als selbst bewertete Kontrollfamilien, nicht als Zertifizierungen; Übertragung an SIEM und ITSM (CEF, LEEF, Syslog, OTLP, OCSF); WebAuthn/FIDO2, PIV/CAC, SSO, SCIM, BYOK/CMEK und nachweisbares Recht auf Löschung, je Deployment konfiguriert.

**31 Module**, eine Konsole, **159 Integrationen**, aus dem Code gezählt von [`scripts/check-public-counts.sh`](scripts/check-public-counts.sh). Die Aufschlüsselung steht in [`connectors/README.md`](connectors/README.md), jedes Modul mit seinem Reifegrad im [Modulkatalog](docs-site/src/content/docs/reference/modules/overview.md).

## Installation

Wählen Sie eine Methode. Danach gibt `olivares quickstart` die Konsolen-URL und ein einmaliges Setup-Token aus. Releases sind mit cosign signiert und enthalten SLSA-Provenienz und SBOMs; jede Methode unten prüft vor der Installation, und `scripts/verify-release.sh` prüft einen manuellen Download (cosign und SHA-256, [Anleitung](INSTALL.md#verifying-a-release)). Die Engine startet mit HTTPS, ohne Standardzugangsdaten und mit einem einmaligen Setup-Token.

**1 · Ein Befehl, Linux und macOS.** Das Installationsskript erkennt Betriebssystem und Architektur, prüft die signierten Prüfsummen und den SHA-256 des Archivs, installiert nur das Binary und führt nie `sudo` aus.

```sh
curl -fsSL https://olivares.ai/olivares/install.sh | sh
olivares quickstart        # prints the console URL and the one-time setup token
```

Fügen Sie `--user` für einen Benutzerdienst hinzu (systemd-Benutzereinheit oder LaunchAgent), oder führen Sie das Skript aus einer privilegierten Shell mit `--system --start` für einen Systemdienst aus. Der manuelle Weg und die Matrix pro Betriebssystem: [`INSTALL.md`](INSTALL.md).

**2 · Docker.** Multi-Arch, distroless, ohne Root. Die Ports werden auf allen Host-Schnittstellen veröffentlicht; stellen Sie den `-p`-Zuordnungen `127.0.0.1:` voran, um sie lokal zu halten.

```sh
docker run -d --name olivares -p 8443:8443 -p 8444:8444 \
  -v olivares-data:/var/lib/olivares \
  docker.io/olivaresai/olivares \
  serve --listen :8443 --grpc-listen :8444 --data-dir /var/lib/olivares
```

`ghcr.io/olivaresai/olivares` ist dasselbe Image; in Produktion per Digest pinnen. FIPS- und STIG-Varianten: [`INSTALL.md`](INSTALL.md#docker).

**3 · Docker Compose.** Ein gehärteter Stack: SQLite auf einem Knoten, optional mit Postgres und Backup.

```sh
git clone --depth 1 https://github.com/olivaresai/olivares.git && cd olivares
docker compose -f deploy/compose/docker-compose.yml up --wait --wait-timeout 120
```

**4 · Kubernetes.** Das Helm-Chart aus diesem Repository oder ein flaches Manifest ohne Helm. Das Chart hat noch kein OCI-Release (`publication-unverified`).

```sh
helm install olivares deploy/helm/olivares -n olivares-system --create-namespace
# or, Helm-free
kubectl create namespace olivares-system && kubectl apply -n olivares-system -f deploy/manifests/install.yaml
```

**5 · Linux-Pakete.** `.deb`, `.rpm` und `.apk` auf der [Release-Seite](https://github.com/olivaresai/olivares/releases/tag/26.10.0): das Binary, eine Beispiel-env-Datei, ein `olivares`-Benutzer ohne Login und eine gehärtete Unit. Der Dienst startet bei der Installation nicht.

```sh
sudo dpkg -i olivares_*_linux_amd64.deb        # Debian / Ubuntu   (sudo rpm -i … on RHEL / Fedora / SUSE; sudo apk add --allow-untrusted … on Alpine)
sudo systemctl enable --now olivares           # OpenRC hosts: sudo rc-service olivares start
```

**6 · Homebrew.** macOS und Linux, gegen die signierten Prüfsummen geprüft.

```sh
brew install olivaresai/tap/olivares && olivares quickstart
```

**7 · Aus dem Quellcode.** Go 1.26+, [Task](https://taskfile.dev) und pnpm.

```sh
task build && ./bin/olivares quickstart
```

**Air-Gapped:** Bündeln Sie das signierte Image, das Chart und das Prüfmaterial, und prüfen Sie offline mit `scripts/verify-release.sh --key … --offline` ([Anleitung](docs-site/src/content/docs/how-to/air-gap-install.md)). **Windows** hat noch keinen nativen Build: Nutzen Sie den Linux-Container oder WSL2 ([Plan](INSTALL.md#windows)). Upgrade und Rollback: [Anleitung](docs-site/src/content/docs/how-to/upgrade-and-rollback.md).

## Schnellstart

```sh
# a deterministic demo estate — loopback-only (the demo password is public), no real data
olivares serve --seed-demo --insecure --listen 127.0.0.1:8901 --grpc-listen 127.0.0.1:8902 --data-dir "$(mktemp -d)"
# open http://127.0.0.1:8901 — inventory, work, orchestration, access map + drift, policies, FinOps

# the real thing — TLS on, reachable from your network; create the first administrator with the printed token
olivares quickstart
```

Das Demo-Passwort ist öffentlich: Verwenden Sie die Demo nicht mit echten Daten. Der [vollständige Schnellstart](docs-site/src/content/docs/start/quickstart.md) bindet eine echte pgAudit-Quelle an.

## Ein Blick in die Konsole

| | |
|---|---|
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/access-map-dark.png"><img src="docs-site/public/console/access-map-light.png" alt="Access Map: was jeder Agent in Ihrem Estate liest und schreibt, Ursprünge links, Ressourcen rechts."></picture><br><sub><b>Access Map</b> — Ursprünge links, Ressourcen rechts, Lesen und Schreiben nach Farbe.</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/access-map-drift-dark.png"><img src="docs-site/public/console/access-map-drift-light.png" alt="Least-Privilege-Drift: unerwartete Zugriffe und ungenutzte Grants über der Access Map."></picture><br><sub><b>Least-Privilege-Drift</b> — beobachtet, aber nicht erlaubt, und Grants, die niemand nutzt.</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/agentops-dark.png"><img src="docs-site/public/console/agentops-light.png" alt="Claude-Code-Sitzungen, die aus der Konsole erstellt, angehängt und gesteuert werden."></picture><br><sub><b>Sitzungen</b> — Sitzungen aus der Konsole erstellen, anhängen und steuern, ohne SSH.</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/work-dark.png"><img src="docs-site/public/console/work-light.png" alt="Arbeit: der dauerhafte sitzungsübergreifende Backlog aus Arbeitselementen und Entscheidungen."></picture><br><sub><b>Arbeit</b> — der dauerhafte sitzungsübergreifende Backlog: Elemente, Eigentümerschaft, Abnahme, Entscheidungen.</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/security-dark.png"><img src="docs-site/public/console/security-light.png" alt="Sicherheit und Forensik: Guardrail-Funde, die Anomalie-Warteschlange und manipulationssichere Forensik."></picture><br><sub><b>Sicherheit &amp; Forensik</b> — Guardrail-Funde, Anomalien, manipulationssichere Forensik.</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/finops-dark.png"><img src="docs-site/public/console/finops-light.png" alt="FinOps: Modellausgaben, Token-Nutzung, Budgets und eine Run-Rate-Projektion."></picture><br><sub><b>FinOps</b> — Ausgaben nach Modell und Agent, Budgets, die verweigern oder drosseln, Run-Rate.</sub> |

Die Ansichten stammen aus der Demo auf dem laufenden Binary. Alle Ansichten: die [Konsolenreferenz](docs-site/src/content/docs/reference/console.md).

## Editionen und Preise

Community ist das vollständige Produkt unter AGPL-3.0, mit unbegrenzten Benutzern und allen vier deny-closed Enforcement Points. Business und Enterprise fügen kommerziellen Code hinzu, der nur mit `-tags enterprise` gebaut wird; aus Community wird nichts entfernt oder begrenzt.

| Edition | Preis | Enthält |
|---|---|---|
| **Community** | Kostenlos, AGPL-3.0 | Das vollständige selbst gehostete Produkt. Unbegrenzte Benutzer, ein aktiver Identitätsanbieter. |
| **Business** | 129 USD/Monat oder 1.290 USD/Jahr | Die kommerzielle Lizenz, der signierte Release-Kanal, E-Mail-Support zu Geschäftszeiten sowie **Regulated Operations**, **AI Runtime Security**, **Compliance Packs** und **Identity & Scale**. Unbegrenzte Benutzer; eine juristische Person; bis zu zwei Produktions-Deployments mit je einem Staging-Deployment; bis zu fünf aktive Identitätsanbieter. |
| **Enterprise** | Vertrag | Weitere juristische Personen, Deployments und Identitätsanbieter, Air-Gapped-Mirrors, individuelles LTS und Support-Bedingungen, mit einem jährlichen Bestellformular. |

Kaufbedingungen: [olivares.ai/pricing](https://olivares.ai/pricing). Was offen und was kommerziell ist: [`LICENSING.md`](LICENSING.md).

## Architektur

Ein statisches Go-Binary enthält die Konsole und stellt vier Schnittstellen bereit: die REST-API, einen gRPC-Spiegel des stabilen Kerns, die `olivares`-CLI und einen Terraform-Provider. Collectors laufen in Ihrer Infrastruktur. Der Speicher ist SQLite oder Postgres mit Row-Level-Security, durchgesetzt in der Store-API und erneut von Postgres. Details einschließlich der Arbeitsebene: [`ARCHITECTURE.md`](ARCHITECTURE.md).

## Dokumentation

[docs.olivares.ai](https://docs.olivares.ai) — getestete Installations-Tutorials (Single Node, Docker Compose, Kubernetes/Helm, air-gapped), Connector-Anleitungen mit echten Konsolen-Captures, ein Cookbook (deny-closed Richtlinien, Budgets, Freigaben, Kill-Switch-Übungen, SIEM-Push), API-Referenz und ein Glossar. Beginnen Sie bei [Was ist Olivares AI](docs-site/src/content/docs/start/what-is-olivares-ai.md). Auf der Website: [Produkt](https://olivares.ai/product) · [Lösungen](https://olivares.ai/solutions) · [so funktioniert es](https://olivares.ai/how-it-works) · [Architektur](https://olivares.ai/architecture) · [Sicherheit](https://olivares.ai/security) · [Vertrauen](https://olivares.ai/trust) · [Vergleich](https://olivares.ai/compare) · [Demo](https://olivares.ai/demo) · [Changelog](https://olivares.ai/changelog) · [Status](https://olivares.ai/status) · [Roadmap](https://olivares.ai/roadmap) · [Marke](https://olivares.ai/brand) · [Presse](https://olivares.ai/press). Releases: [GitHub](https://github.com/olivaresai/olivares/releases) · [`CHANGELOG.md`](CHANGELOG.md).

## Sicherheit

Melden Sie eine Schwachstelle vertraulich über [`SECURITY.md`](SECURITY.md), nicht als öffentliches Issue. Die Zugriffskarte speichert Kanten, keine Inhalte, und ihr Öffnen wird auditiert. Die Lizenzprüfung erfolgt offline; der AGPL-Kern führt keinen Lizenzaufruf aus. Advisories: [`docs/security-advisories.md`](docs/security-advisories.md); Lieferketten-Nachweise: [`docs/openssf-badge.md`](docs/openssf-badge.md).

## Community

[`CONTRIBUTING.md`](CONTRIBUTING.md) (Setup, DCO/CLA, SPDX, die Connector-Grenze) · [`CODE_OF_CONDUCT.md`](CODE_OF_CONDUCT.md) · [`SUPPORT.md`](SUPPORT.md) · [`GOVERNANCE.md`](GOVERNANCE.md) · [`CHANGELOG.md`](CHANGELOG.md) (Keep a Changelog, CalVer `YY.M.PATCH`).

## Lizenz

`core/`, `modules/` und `web/` stehen unter **AGPL-3.0-only**; `sdk/`, `connectors/` und `clients/` unter **Apache-2.0**, und ein Konnektor importiert nie die Engine. Der kommerzielle Code wird nur mit `-tags enterprise` gebaut und liegt nicht in diesem Repository. Kommerzielle Lizenzierung: `enterprise@olivares.ai` — [`LICENSING.md`](LICENSING.md). Beiträge brauchen ein DCO-Sign-off (`git commit -s`) und die [CLA](CLA.md).

> **Keine Gewährleistung.** Die Software wird **ohne Mängelgewähr** bereitgestellt, **ohne jede Gewährleistung** und **ohne Haftung für Datenverlust, Betriebsunterbrechung oder entgangenen Gewinn**. Es gelten AGPL-3.0-only §§15–16, Apache-2.0 §§7–8 und die ergänzende Bedingung dieses Projekts — [`DISCLAIMER.md`](DISCLAIMER.md).

## Das Projekt unterstützen

Unterstützen Sie das Projekt über GitHub Sponsors — [github.com/sponsors/olivaresai](https://github.com/sponsors/olivaresai) oder [github.com/sponsors/fran-olivares](https://github.com/sponsors/fran-olivares) — oder einmalig über Ko-fi. Sponsoring ist kein Supportvertrag ([`SUPPORT.md`](SUPPORT.md)); Sponsoren, die genannt werden möchten, stehen in [`SUPPORTERS.md`](SUPPORTERS.md).

[![ko-fi](https://ko-fi.com/img/githubbutton_sm.svg)](https://ko-fi.com/Z1R625SAD2)

---

<div align="center">

<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/olivares-mark-dark.svg">
  <img src=".github/assets/olivares-mark-light.svg" alt="Olivares AI" width="44">
</picture>

<sub><strong>Ground Truth für KI im Unternehmen.</strong> · <a href="https://olivares.ai">olivares.ai</a> · <a href="LICENSING.md">AGPL-3.0 + commercial</a></sub>

</div>
