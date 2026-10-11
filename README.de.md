<div align="center">

<a href="https://olivares.ai"><img src=".github/assets/olivares-banner.png" alt="Olivares AI — Ground truth for enterprise AI" width="720"></a>

**Sprachen:** [English](./README.md) · [Español](./README.es.md) · [简体中文](./README.zh.md) · [Русский](./README.ru.md) · [日本語](./README.ja.md) · **Deutsch** · [Français](./README.fr.md)

**Betreiben Sie die KI, die Ihr Team bereits nutzt, mit derselben Kontrolle wie über Ihre übrige Infrastruktur.**

[Was es tut](#was-es-tut) · [Installation](#installation) · [Editionen](#editionen-und-preise) · [Dokumentation](#dokumentation) · [Community](#community) · [olivares.ai](https://olivares.ai)

[![License: AGPL-3.0-only](https://img.shields.io/badge/license-AGPL--3.0--only-blue)](LICENSING.md)
[![SDK & connectors: Apache-2.0](https://img.shields.io/badge/SDK%20%26%20connectors-Apache--2.0-blue)](LICENSING.md) <!-- release -->
[![Next release: 0.1](https://img.shields.io/badge/release-0.1-28282B)](https://github.com/olivaresai/olivares/releases/tag/0.1)<!-- /release -->
[![Status: beta](https://img.shields.io/badge/status-beta-F08000)](CHANGELOG.md)
[![Contributor Covenant](https://img.shields.io/badge/Contributor%20Covenant-2.1-4baaaa)](CODE_OF_CONDUCT.md)

</div>


Ihre Entwickler arbeiten mit Claude Code und Codex. Agenten rufen MCP-Server, Modelle und interne APIs auf, und geplante Jobs laufen selbstständig. Jede Komponente hat eigene Protokolle und Berechtigungen. Deshalb gibt es auf einfache Fragen keine schnelle Antwort: Welcher Agent hat diese Datei geändert? Wer hat es genehmigt? Was hat uns KI diesen Monat gekostet?

Olivares AI beantwortet diese Fragen an einem Ort. Es verbindet sich mit den Agenten und Tools, die Sie bereits nutzen, zeigt deren Aktionen, prüft Ihre Regeln vor der Ausführung und führt über alles ein signiertes Protokoll. Es ist ein einzelnes Programm auf Ihren eigenen Servern. Das vollständige Produkt ist kostenlos und Open Source.

<div align="center">
<img src=".github/assets/motion-access-map.gif" width="840" alt="Motion diagram of the read/write access map: agents, sessions and identities on the left, the resources they reach on the right, reads in blue, writes in orange, one observed write that was never permitted flagged as a drift finding.">
<br><sub><b>Die Access Map</b> — was jeder Agent liest und schreibt, und der eine Schreibzugriff, den niemand erlaubt hat.</sub>
</div>

## Was es tut

- **Wissen, was läuft.** Alle Agenten, Sitzungen, Modelle, MCP-Server und Tools in einem Inventar. Die Access Map zeigt, was jeder liest und schreibt, und markiert Zugriffe, die keine Regel erlaubt.
- **Aktionen stoppen, bevor sie Schaden anrichten.** Olivares AI hat **vier deny-closed Enforcement Points**, die jede Aktion vor der Ausführung prüfen: in Claude Code, im Modell-Proxy, bei jedem MCP-Tool-Aufruf und zwischen Agenten. Eine riskante Aktion wartet auf eine zweite Person, eine verbotene wird nicht ausgeführt. Ein Schalter stoppt alle Agenten zugleich. Wenn eine Prüfung keine Entscheidung treffen kann, wird die Aktion nicht ausgeführt.
- **KI-Ausgaben kontrollieren.** Budgets pro Team, Agent oder Modell warnen, drosseln oder stoppen Ausgaben, bevor die Rechnung kommt.
- **Agenten sicher auf das Wissen Ihres Unternehmens zugreifen lassen.** Verbinden Sie SharePoint, Confluence, Google Drive, Notion, Salesforce, Snowflake, S3 und PostgreSQL. Jeder Agent sieht nur das, was die Person dahinter sehen darf.
- **Arbeit über Sitzungen hinweg fortsetzen.** Aufgaben, Verantwortliche und Entscheidungen bleiben erhalten, wenn eine Sitzung endet. Starten, betreten und stoppen Sie Sitzungen von Claude Code, Codex und Grok im Browser, ohne SSH.
- **Nachweise liefern, wenn sie gebraucht werden.** Jede Entscheidung landet in einem signierten Protokoll, in dem jede nachträgliche Änderung erkennbar ist. Business Compliance Packs ordnet Nachweise 26 Framework-Katalogen zu und erstellt Berichte für Ihr Sicherheitsteam und Ihre Auditoren. Community behält gespeicherte Nachweise und deren JSON/CSV-Exporte.

Es funktioniert mit Ihren vorhandenen Tools: Claude Code, Codex, Grok, Cursor, gemini-cli, opencode, OpenHands und lokalen Modellen über Ollama. **32 Module** und **136 Integrationen**: [alle Module](docs-site/src/content/docs/reference/modules/overview.md) · [alle Konnektoren](connectors/README.md).

Community behält lokale Observability, gespeicherte Einstellungen und den Backup-Export. SIEM/ITSM-Versand, externe Telemetrieübertragung und Posture-Export gehören zur Business-Basisedition.

## Installation

Wählen Sie eine Methode und kopieren Sie den zugehörigen Block. Zum Schluss gibt `olivares quickstart` die Konsolenadresse und ein einmaliges Token zum Anlegen des ersten Administrators aus. Jede Version ist signiert, und jede Methode prüft den Download vor der Installation ([Download selbst prüfen](INSTALL.md#verifying-a-release)).

**Linux und macOS, ein Befehl.** Erkennt Ihr System, prüft die Version, installiert nur die Binärdatei und verwendet nie `sudo`.

```sh
curl -fsSL https://olivares.ai/olivares/install.sh | sh
olivares quickstart
```

**Docker.** Multi-Arch. Container-Images basieren auf Debian 13 slim (mit Node.js 24 für die Agent-Tools) und laufen als Nicht-Root-Benutzer. Lauscht auf allen Host-Schnittstellen; setzen Sie vor jedes `-p` ein `127.0.0.1:`, um den Zugriff lokal zu halten.

<!-- release -->
```sh
docker run -d --name olivares -p 8443:8443 -p 8444:8444 \
  -v olivares-data:/var/lib/olivares \
  docker.io/olivaresai/olivares:0.1 \
  serve --listen :8443 --grpc-listen :8444 --data-dir /var/lib/olivares
```
<!-- /release -->

**Docker Compose.** SQLite auf einem Knoten, optional mit Postgres und Backups.

```sh
git clone --depth 1 https://github.com/olivaresai/olivares.git && cd olivares
docker compose -f deploy/compose/docker-compose.yml up --wait --wait-timeout 120
```

Sitzungen arbeiten in einem Host-Ordner: Setzen Sie `OLIVARES_PROJECT_DIR` vor `up` auf dessen absoluten Pfad, und Compose bindet ihn unter `/project` ein. Auf Hosts, deren AppArmor-Richtlinie User-Namespaces einschränkt (Ubuntu 24.04 und neuer), laden Sie zuerst das Sitzungsprofil: siehe [Docker Compose](INSTALL.md#docker-compose).

**Debian und Ubuntu.** Das Paket legt einen Benutzer `olivares` ohne Anmeldemöglichkeit und einen gehärteten Dienst an; Sie starten ihn.

```sh
curl -fsSLO https://github.com/olivaresai/olivares/releases/download/0.1/olivares_0.1_linux_amd64.deb
sudo dpkg -i olivares_0.1_linux_amd64.deb && sudo systemctl enable --now olivares
```

**RHEL, Fedora und SUSE.**

```sh
curl -fsSLO https://github.com/olivaresai/olivares/releases/download/0.1/olivares_0.1_linux_amd64.rpm
sudo rpm -i olivares_0.1_linux_amd64.rpm && sudo systemctl enable --now olivares
```

**Alpine.**

```sh
curl -fsSLO https://github.com/olivaresai/olivares/releases/download/0.1/olivares_0.1_linux_amd64.apk
sudo apk add --allow-untrusted olivares_0.1_linux_amd64.apk && sudo rc-service olivares start
```

Verwenden Sie auf ARM-Servern `arm64` statt `amd64`. Alle Dateien der Version finden Sie auf der [Release-Seite](https://github.com/olivaresai/olivares/releases/tag/0.1).

**Homebrew.** macOS und Linux.

```sh
brew install olivaresai/tap/olivares && olivares quickstart
```

**Aus dem Quellcode.** Go 1.26+, [Task](https://taskfile.dev) und pnpm.

```sh
git clone --depth 1 https://github.com/olivaresai/olivares.git && cd olivares
task build && ./bin/olivares quickstart
```

**Netze ohne Internetzugang:** Stellen Sie das signierte Image, das Chart und das Prüfmaterial zusammen und [installieren Sie in einer abgeschotteten Umgebung](docs-site/src/content/docs/how-to/air-gap-install.md). Für **Windows** gibt es noch keinen nativen Build: Verwenden Sie das Docker-Image oder WSL2. Updates und Rollback: [Anleitung](docs-site/src/content/docs/how-to/upgrade-and-rollback.md). Alle Optionen im Detail: [`INSTALL.md`](INSTALL.md).

**Zuerst mit Demodaten ausprobieren**, nur auf Ihrem eigenen Rechner (das Demo-Passwort ist öffentlich):

```sh
olivares serve --seed-demo --insecure --listen 127.0.0.1:8901 --grpc-listen 127.0.0.1:8902 --data-dir "$(mktemp -d)"
```

Öffnen Sie anschließend http://127.0.0.1:8901.

Helm, der Kubernetes-Operator, Terraform, die Appliance und die FIPS/STIG-Bereitstellungsartefakte werden mit Business ausgeliefert; die Installation ohne Netzverbindung erfordert Enterprise. Siehe [Editionen](docs/editions.md).

## Editionen und Preise

Community ist das vollständige Produkt, kostenlos und Open Source. Business ergänzt, was ein Unternehmen für den Produktionsbetrieb braucht. Enterprise richtet sich an Unternehmensgruppen mit größeren oder regulierten IT-Umgebungen.

| | **Community** | **Business** | **Enterprise** |
|---|---|---|---|
| **Preis** | Kostenlos, AGPL-3.0 | 129 USD/Monat oder 1.290 USD/Jahr | Jahresvertrag |
| **Leistungsumfang** | Das vollständige Produkt: unbegrenzt viele Benutzer und alle vier deny-closed Enforcement Points | Alles aus Community sowie Regulated Operations, AI Runtime Security, Compliance Packs und Identity & Scale, die kommerzielle Lizenz, signierte Updates und E-Mail-Support | Alles aus Business sowie mehr Unternehmen, Deployments und Identitätsanbieter, Offline-Mirrors und mit Ihnen vereinbarte Supportbedingungen |
| **Geltungsbereich** | Ein aktiver Identitätsanbieter | Ein Unternehmen, eine gleichzeitig aktive Instanz | Im Vertrag vereinbart |

**Regulated Operations** ergänzt regulatorische Mindestaufbewahrungsfristen, den Abgleich von Legal Holds auf Archiven und WORM-Archive auf Azure und GCS. **AI Runtime Security** ergänzt eine tiefere Prüfung dessen, was Agenten senden, empfangen und ausführen. **Compliance Packs** erstellt Entwürfe des DORA-Informationsregisters und des ISO/IEC-42001-Pakets für Ihre Prüfer. **Identity & Scale** verbindet mehrere Identitätsanbieter gleichzeitig und wächst mit größeren Deployments.

[olivares.ai/pricing](https://olivares.ai/pricing) · [Was jede Edition enthält](docs/editions.md) · [Was offen und was kommerziell ist](LICENSING.md)

## Architektur

Eine Go-Binärdatei mit integrierter Konsole. Sie stellt eine REST-API, eine gRPC-API, die Befehlszeile `olivares` und einen Terraform-Provider bereit. Collectors laufen in Ihrem Netzwerk; die Daten bleiben in SQLite oder PostgreSQL auf Ihren Servern. [Wie alles zusammenpasst](ARCHITECTURE.md).

## Dokumentation

[docs.olivares.ai](https://docs.olivares.ai) bietet Installationsanleitungen, eine Anleitung für jeden Konnektor, Beispiele für gängige Richtlinien und die API-Referenz. Beginnen Sie mit [Was ist Olivares AI](docs-site/src/content/docs/start/what-is-olivares-ai.md). Was heute läuft und was noch geplant ist: [Ehrlichkeit & Grenzen](docs-site/src/content/docs/start/honesty-and-limits.md). Versionen: [GitHub](https://github.com/olivaresai/olivares/releases) · [`CHANGELOG.md`](CHANGELOG.md).

Auf der Website: [Produkt](https://olivares.ai/product) · [Lösungen](https://olivares.ai/solutions) · [so funktioniert es](https://olivares.ai/how-it-works) · [Architektur](https://olivares.ai/architecture) · [Sicherheit](https://olivares.ai/security) · [Vertrauen](https://olivares.ai/trust) · [Vergleich](https://olivares.ai/compare) · [Demo](https://olivares.ai/demo) · [Changelog](https://olivares.ai/changelog) · [Status](https://olivares.ai/status) · [Roadmap](https://olivares.ai/roadmap) · [Marke](https://olivares.ai/brand) · [Presse](https://olivares.ai/press).

## Sicherheit

Eine Schwachstelle gefunden? Melden Sie sie vertraulich über [`SECURITY.md`](SECURITY.md). Olivares AI zeichnet auf, welcher Agent auf welche Ressource zugegriffen hat, nicht deren Inhalt. Auch das Öffnen dieser Aufzeichnung wird protokolliert. Lizenzen werden offline geprüft; der Open-Source-Kern nimmt nie Kontakt zu uns auf.

## Community

Beiträge sind willkommen. [`CONTRIBUTING.md`](CONTRIBUTING.md) erklärt die Einrichtung, den Sign-off und die Einbindung von Konnektoren. [Verhaltenskodex](CODE_OF_CONDUCT.md) · [Support](SUPPORT.md) · [Governance](GOVERNANCE.md) · [Änderungsprotokoll](CHANGELOG.md).

## Das Projekt unterstützen

Olivares AI wird offen entwickelt. Wenn es Ihnen hilft, unterstützen Sie die Entwicklung über GitHub Sponsors — [olivaresai](https://github.com/sponsors/olivaresai) oder [fran-olivares](https://github.com/sponsors/fran-olivares) — oder spendieren Sie uns einen Kaffee auf Ko-fi. Sponsoren, die genannt werden möchten, stehen in [`SUPPORTERS.md`](SUPPORTERS.md). Sponsoring ist kein Supportvertrag ([`SUPPORT.md`](SUPPORT.md)).

[![ko-fi](https://ko-fi.com/img/githubbutton_sm.svg)](https://ko-fi.com/Z1R625SAD2)

## Lizenz

Engine, Module und Konsole stehen unter **AGPL-3.0-only**; SDK, Konnektoren und Clients unter **Apache-2.0**. Kommerzieller Code wird separat gebaut und ist nicht in diesem Repository enthalten; kommerzielle Lizenzen: `enterprise@olivares.ai`. Beiträge brauchen ein DCO-Sign-off (`git commit -s`) und die [CLA](CLA.md).

> Bereitgestellt **ohne Mängelgewähr**, ohne jede Gewährleistung und ohne Haftung für Datenverlust, Betriebsunterbrechung oder entgangenen Gewinn. Siehe [`DISCLAIMER.md`](DISCLAIMER.md).

---

<div align="center">

<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/olivares-mark-dark.svg">
  <img src=".github/assets/olivares-mark-light.svg" alt="Olivares AI" width="44">
</picture>

<sub><strong>Ground Truth für KI im Unternehmen.</strong> · <a href="https://olivares.ai">olivares.ai</a> · <a href="LICENSING.md">AGPL-3.0 + commercial</a></sub>

</div>
