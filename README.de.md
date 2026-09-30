<div align="center">

<a href="https://olivares.ai"><img src=".github/assets/olivares-banner.png" alt="Olivares AI — Ground truth for enterprise AI" width="720"></a>

**Sprachen:** [English](./README.md) · [Español](./README.es.md) · [简体中文](./README.zh.md) · [Русский](./README.ru.md) · [日本語](./README.ja.md) · **Deutsch** · [Français](./README.fr.md)

**Betreiben Sie die KI, die Ihr Team bereits nutzt, mit derselben Kontrolle wie über Ihre übrige Infrastruktur.**

[Was es tut](#was-es-tut) · [Installation](#installation) · [Konsole](#ein-blick-in-die-konsole) · [Editionen](#editionen-und-preise) · [Dokumentation](#dokumentation) · [Community](#community) · [olivares.ai](https://olivares.ai)

[![License: AGPL-3.0-only](https://img.shields.io/badge/license-AGPL--3.0--only-blue)](LICENSING.md)
[![SDK & connectors: Apache-2.0](https://img.shields.io/badge/SDK%20%26%20connectors-Apache--2.0-blue)](LICENSING.md)
[![Release: 26.10](https://img.shields.io/badge/release-26.10-28282B)](https://github.com/olivaresai/olivares/releases/tag/26.10.0)
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
- **Nachweise liefern, wenn sie gebraucht werden.** Jede Entscheidung landet in einem signierten Protokoll, das nachträglich nicht geändert werden kann. Ihr Sicherheitsteam und Ihre Auditoren beziehen ihre Berichte daraus; die Nachweise sind 26 Framework-Katalogen zugeordnet.

Es funktioniert mit Ihren vorhandenen Tools: Claude Code, Codex, Grok, Cursor, gemini-cli, opencode, OpenHands und lokalen Modellen über Ollama. **31 Module** und **159 Integrationen**, alle in der kostenlosen Edition: [alle Module](docs-site/src/content/docs/reference/modules/overview.md) · [alle Konnektoren](connectors/README.md).

## Installation

Wählen Sie eine Methode und kopieren Sie den zugehörigen Block. Zum Schluss gibt `olivares quickstart` die Konsolenadresse und ein einmaliges Token zum Anlegen des ersten Administrators aus. Jede Version ist signiert, und jede Methode prüft den Download vor der Installation ([Download selbst prüfen](INSTALL.md#verifying-a-release)).

**Linux und macOS, ein Befehl.** Erkennt Ihr System, prüft die Version, installiert nur die Binärdatei und verwendet nie `sudo`.

```sh
curl -fsSL https://olivares.ai/olivares/install.sh | sh
olivares quickstart
```

**Docker.** Multi-Arch, distroless, ohne Root. Lauscht auf allen Host-Schnittstellen; setzen Sie vor jedes `-p` ein `127.0.0.1:`, um den Zugriff lokal zu halten.

```sh
docker run -d --name olivares -p 8443:8443 -p 8444:8444 \
  -v olivares-data:/var/lib/olivares \
  docker.io/olivaresai/olivares \
  serve --listen :8443 --grpc-listen :8444 --data-dir /var/lib/olivares
```

**Docker Compose.** SQLite auf einem Knoten, optional mit Postgres und Backups.

```sh
git clone --depth 1 https://github.com/olivaresai/olivares.git && cd olivares
docker compose -f deploy/compose/docker-compose.yml up --wait --wait-timeout 120
```

**Kubernetes.** Das Helm-Chart aus diesem Repository (noch keine OCI-Veröffentlichung des Charts: `publication-unverified`).

```sh
git clone --depth 1 https://github.com/olivaresai/olivares.git && cd olivares
helm install olivares deploy/helm/olivares -n olivares-system --create-namespace
```

Ohne Helm:

```sh
git clone --depth 1 https://github.com/olivaresai/olivares.git && cd olivares
kubectl create namespace olivares-system && kubectl apply -n olivares-system -f deploy/manifests/install.yaml
```

**Debian und Ubuntu.** Das Paket legt einen Benutzer `olivares` ohne Anmeldemöglichkeit und einen gehärteten Dienst an; Sie starten ihn.

```sh
curl -fsSLO https://github.com/olivaresai/olivares/releases/download/26.10.0/olivares_26.10.0_linux_amd64.deb
sudo dpkg -i olivares_26.10.0_linux_amd64.deb && sudo systemctl enable --now olivares
```

**RHEL, Fedora und SUSE.**

```sh
curl -fsSLO https://github.com/olivaresai/olivares/releases/download/26.10.0/olivares_26.10.0_linux_amd64.rpm
sudo rpm -i olivares_26.10.0_linux_amd64.rpm && sudo systemctl enable --now olivares
```

**Alpine.**

```sh
curl -fsSLO https://github.com/olivaresai/olivares/releases/download/26.10.0/olivares_26.10.0_linux_amd64.apk
sudo apk add --allow-untrusted olivares_26.10.0_linux_amd64.apk && sudo rc-service olivares start
```

Verwenden Sie auf ARM-Servern `arm64` statt `amd64`. Alle Dateien der Version finden Sie auf der [Release-Seite](https://github.com/olivaresai/olivares/releases/tag/26.10.0).

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

## Ein Blick in die Konsole

| | |
|---|---|
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/access-map-dark.png"><img src="docs-site/public/console/access-map-light.png" alt="Access map: what each agent reads and writes across your estate, origins on the left, resources on the right."></picture><br><sub><b>Access Map</b> — wer was liest und schreibt.</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/access-map-drift-dark.png"><img src="docs-site/public/console/access-map-drift-light.png" alt="Least-privilege drift: unexpected accesses and unused grants overlaid on the access map."></picture><br><sub><b>Drift</b> — Zugriffe, die niemand erlaubt hat, und Berechtigungen, die niemand nutzt.</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/agentops-dark.png"><img src="docs-site/public/console/agentops-light.png" alt="Claude Code sessions created, attached to and governed from the console."></picture><br><sub><b>Sitzungen</b> — Agentensitzungen im Browser starten, betreten und stoppen.</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/work-dark.png"><img src="docs-site/public/console/work-light.png" alt="Work: the durable cross-session backlog of work items and decisions."></picture><br><sub><b>Arbeit</b> — Aufgaben, Verantwortliche und Entscheidungen, die eine Sitzung überdauern.</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/security-dark.png"><img src="docs-site/public/console/security-light.png" alt="Security and forensics: guardrail findings, the anomaly queue and tamper-evident forensics."></picture><br><sub><b>Sicherheit</b> — blockierte Aktionen, Anomalien und ein unveränderbares Protokoll.</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/finops-dark.png"><img src="docs-site/public/console/finops-light.png" alt="FinOps: model spend, token usage, budgets and a run-rate projection."></picture><br><sub><b>Ausgaben</b> — Kosten pro Modell und Agent, Budgets und Prognosen.</sub> |

Alle Ansichten: die [Konsolenreferenz](docs-site/src/content/docs/reference/console.md).

## Editionen und Preise

Community ist das vollständige Produkt, kostenlos und Open Source. Business ergänzt, was ein Unternehmen für den Produktionsbetrieb braucht. Enterprise richtet sich an Unternehmensgruppen mit größeren oder regulierten IT-Umgebungen.

| | **Community** | **Business** | **Enterprise** |
|---|---|---|---|
| **Preis** | Kostenlos, AGPL-3.0 | 129 USD/Monat oder 1.290 USD/Jahr | Jahresvertrag |
| **Leistungsumfang** | Das vollständige Produkt: unbegrenzt viele Benutzer und alle vier deny-closed Enforcement Points | Alles aus Community sowie Regulated Operations, AI Runtime Security, Compliance Packs und Identity & Scale, die kommerzielle Lizenz, signierte Updates und E-Mail-Support | Alles aus Business sowie mehr Unternehmen, Deployments und Identitätsanbieter, Offline-Mirrors und mit Ihnen vereinbarte Supportbedingungen |
| **Geltungsbereich** | Ein aktiver Identitätsanbieter | Ein Unternehmen, zwei Produktionsdeployments mit jeweils einer Staging-Umgebung, fünf Identitätsanbieter | Im Vertrag vereinbart |

**Regulated Operations** bewahrt Aufzeichnungen so lange auf, wie das Gesetz es verlangt, mit Legal Hold und unveränderbaren Archiven. **AI Runtime Security** filtert, was Agenten senden, empfangen und ausführen. **Compliance Packs** liefert fertige Nachweise für ISO 42001, DORA und NIS 2. **Identity & Scale** verbindet mehrere Identitätsanbieter gleichzeitig und wächst mit größeren Deployments.

[olivares.ai/pricing](https://olivares.ai/pricing) · [Was offen und was kommerziell ist](LICENSING.md)

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
