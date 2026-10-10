<div align="center">

<a href="https://olivares.ai"><img src=".github/assets/olivares-banner.png" alt="Olivares AI — Ground truth for enterprise AI" width="720"></a>

**Sprachen:** [English](./README.md) · [Español](./README.es.md) · [简体中文](./README.zh.md) · [Русский](./README.ru.md) · [日本語](./README.ja.md) · **Deutsch** · [Français](./README.fr.md)

**Betreiben Sie die KI, die Ihr Team bereits nutzt, mit derselben Kontrolle wie über Ihre übrige Infrastruktur.**

[Was es tut](#was-es-tut) · [Installation](#installation) · [Konsole](#ein-blick-in-die-konsole) · [Editionen](#editionen-und-preise) · [Dokumentation](#dokumentation) · [Community](#community) · [olivares.ai](https://olivares.ai)

[![License: AGPL-3.0-only](https://img.shields.io/badge/license-AGPL--3.0--only-blue)](LICENSING.md)
[![SDK & connectors: Apache-2.0](https://img.shields.io/badge/SDK%20%26%20connectors-Apache--2.0-blue)](LICENSING.md) <!-- release -->
[![Next release: 0.1](https://img.shields.io/badge/release-0.1-28282B)](https://github.com/olivaresai/olivares/releases/tag/0.1)<!-- /release -->
[![Status: beta](https://img.shields.io/badge/status-beta-F08000)](CHANGELOG.md)
[![Contributor Covenant](https://img.shields.io/badge/Contributor%20Covenant-2.1-4baaaa)](CODE_OF_CONDUCT.md)

</div>

Das nächste Release ist <!-- release -->`0.1`<!-- /release -->; es ist noch nicht auf GitHub veröffentlicht. Die folgenden Befehle beschreiben die geplanten Artefakte. Bauen Sie bis zur Veröffentlichung aus dem Quellcode und prüfen Sie danach jedes Artefakt vor der Verwendung. Der beobachtete Veröffentlichungsstand steht in <!-- release -->`docs/releases/0.1-install-surfaces.json`<!-- /release -->. Kubernetes OCI: `publication-unverified`.

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

**Docker Compose.** The container qualification job exercises this installation path.
Set the release image explicitly so a cached `:latest` image cannot select an
older release.

<!-- release -->
```sh
set -e
cd /path/to/your/project   # the host folder your sessions will work on
export OLIVARES_PROJECT_DIR="$PWD"
git clone --depth 1 https://github.com/olivaresai/olivares.git "$HOME/olivares"
export OLIVARES_IMAGE=docker.io/olivaresai/olivares:0.1
# On Linux hosts whose AppArmor policy mediates user namespace creation:
if [ -r /sys/kernel/security/apparmor/features/namespaces/mask ] &&
   grep -qw userns_create /sys/kernel/security/apparmor/features/namespaces/mask; then
  sudo install -m 0644 "$HOME/olivares/deploy/apparmor/olivares-sessions.conf" /etc/apparmor.d/olivares-sessions
  sudo apparmor_parser -r /etc/apparmor.d/olivares-sessions
  export OLIVARES_APPARMOR_PROFILE=olivares-sessions
fi
docker compose -f "$HOME/olivares/deploy/compose/docker-compose.yml" up --wait --wait-timeout 120
docker compose -f "$HOME/olivares/deploy/compose/docker-compose.yml" exec olivares \
  olivares first-boot --data-dir /var/lib/olivares --new-token
```
<!-- /release -->

Open the console address printed by `first-boot --new-token` and use the replacement
one-time setup token it prints to create the first administrator. This invalidates
the previous setup token and is available only before the first administrator exists.
The stack uses SQLite and a persistent data volume.
The AppArmor step needs an AppArmor 4 parser and runs on the Docker daemon host.
See [session confinement on AppArmor hosts](deploy/compose/README.md#session-confinement-on-apparmor-hosts).
It publishes ports on every host interface by default; set `OLIVARES_BIND=127.0.0.1`
to restrict access to this host.

Sessions in the container work on one host folder, mounted at `/project`: the absolute
path in `OLIVARES_PROJECT_DIR`, set before `up`. Without it, `/project` is an empty Docker
volume, never the directory you run Compose from. A session there can change everything in
that folder: never set the variable to your home directory. In the console, choose **Change folder**
on the New session form and enter `/project`. On a Linux host the container user
(UID 65532) needs write access; see
[work on a host project folder](deploy/compose/README.md#work-on-a-host-project-folder).

Gate coverage is not a passing release result: see the `qualify-compose-ready` job in
[container qualification](.github/workflows/compose-ready.yml). The release must also
pass its first-hour journey before it is qualified.

Other installation methods are **not qualified** by the first-hour gate. Their commands
and limits are in [INSTALL.md](INSTALL.md#installation-qualification), including the
shell installer, standalone Docker, Kubernetes, native packages, Homebrew, source builds
and offline installs. [Verify release artifacts](INSTALL.md#verifying-a-release) before
running them; see [upgrading and uninstalling](INSTALL.md#upgrading--uninstalling) for an
existing installation.

- Helm, Kubernetes-Operator und Terraform-Provider gehören zur Business-Distribution. [Editions](https://olivares.ai/pricing).
Install the chart from source; see [Kubernetes installation](INSTALL.md#kubernetes).

## Ein Blick in die Konsole

| | |
|---|---|
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/access-map-dark.png"><img src="docs-site/public/console/access-map-light.png" alt="Access map: what each agent reads and writes across your estate, origins on the left, resources on the right."></picture><br><sub><b>Access Map</b> — wer was liest und schreibt.</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/access-map-drift-dark.png"><img src="docs-site/public/console/access-map-drift-light.png" alt="Least-privilege drift: unexpected accesses and unused grants overlaid on the access map."></picture><br><sub><b>Drift</b> — Zugriffe, die niemand erlaubt hat, und Berechtigungen, die niemand nutzt.</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/agentops-dark.png"><img src="docs-site/public/console/agentops-light.png" alt="Claude Code sessions created, attached to and governed from the console."></picture><br><sub><b>Sitzungen</b> — Agentensitzungen im Browser starten, betreten und stoppen.</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/work-dark.png"><img src="docs-site/public/console/work-light.png" alt="Work: the durable cross-session backlog of work items and decisions."></picture><br><sub><b>Arbeit</b> — Aufgaben, Verantwortliche und Entscheidungen, die eine Sitzung überdauern.</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/security-dark.png"><img src="docs-site/public/console/security-light.png" alt="Security and forensics: guardrail findings, the anomaly queue and tamper-evident forensics."></picture><br><sub><b>Sicherheit</b> — blockierte Aktionen, Anomalien und ein manipulationssicheres Protokoll, in dem jede Änderung erkennbar ist.</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/finops-dark.png"><img src="docs-site/public/console/finops-light.png" alt="FinOps: model spend, token usage, budgets and a run-rate projection."></picture><br><sub><b>Ausgaben</b> — Kosten pro Modell und Agent, Budgets und Prognosen.</sub> |

Alle Ansichten: die [Konsolenreferenz](docs-site/src/content/docs/reference/console.md).

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
