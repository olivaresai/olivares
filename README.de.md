<div align="center">

<a href="https://olivares.ai"><img src=".github/assets/olivares-banner.png" alt="Olivares AI — Ground Truth für KI im Unternehmen" width="720"></a>

**Sprachen:** [English](./README.md) · [Español](./README.es.md) · [简体中文](./README.zh.md) · [Русский](./README.ru.md) · [日本語](./README.ja.md) · **Deutsch** · [Français](./README.fr.md)

**Integrieren, verwalten und sichern Sie die von Ihnen betriebene KI — mit einem einzigen self-hosted Binary.**

[Installation](#install) · [Schnellstart](#quickstart) · [Beispiele](examples/) · [Dokumentation](#documentation) · [Sicherheit](#security) · [Mitwirken](CONTRIBUTING.md) · [olivares.ai](https://olivares.ai)

[![License: AGPL-3.0-only](https://img.shields.io/badge/license-AGPL--3.0--only-blue)](LICENSING.md)
[![SDK & connectors: Apache-2.0](https://img.shields.io/badge/SDK%20%26%20connectors-Apache--2.0-blue)](LICENSING.md)
[![Status: beta](https://img.shields.io/badge/status-beta-F08000)](CHANGELOG.md)
[![Contributor Covenant](https://img.shields.io/badge/Contributor%20Covenant-2.1-4baaaa)](CODE_OF_CONDUCT.md)

</div>

> **Beta**, in aktiver Entwicklung. Das erste getaggte Release, **v26.8.0**, wird mit signierten Archiven, nativen Paketen und Container-Images ausgeliefert. APIs und die Moduloberfläche können sich vor 1.0 noch ändern; was heute läuft, was on-demand verfügbar ist und was sich im Design-Stadium befindet, ist unter [Ehrlichkeit & Grenzen](docs-site/src/content/docs/start/honesty-and-limits.md) und, pro Modul, im [Modulkatalog](docs-site/src/content/docs/reference/modules/overview.md) angegeben.

## Was es ist

Was Sie heute betreiben, ist ein Estate — Coding-Agenten, MCP-Server, Modell-Endpunkte, Service-Accounts, geplante Jobs — verteilt über Rechner, die nie ein einziges System waren. Olivares AI ist ein einzelnes self-hosted Go-Binary mit eingebetteter Konsole, das alles zusammenhält: Es gibt der KI, was sie zum Arbeiten benötigt (Kontext, Ressourcenzugriff, verwaltete Sessions), und liefert Ihnen die Berechtigungen, Richtlinien, Budgets und Audit-Belege, mit denen Sie wissen, was läuft, wer es gestartet hat, worauf es zugegriffen hat, was es gekostet hat und wer dem zugestimmt hat.

**Multi-Provider by design.** Claude Code ist auf der tiefsten Ebene integriert — der `PreToolUse`/`PostToolUse`-Hook, Managed Settings, Starten und Stoppen über die Konsole, Modellzugriff pro Subjekt — mit Codex und Grok Build daneben als erstklassige Befehlsoberflächen sowie gemini-cli, Cursor, opencode, goose, cline, OpenHands, OpenClaw und Hermes als jeweils eigene Connectors, die angeben, was sie durchsetzen und was sie nur beobachten können. Ollama und andere self-hosted Endpunkte werden über den lokalen Connector inventarisiert, der bewusst schreibgeschützt ist.

**Wer es betreibt.** Derselbe Build in jeder Größenordnung: ein Heimserver (eine Binary, SQLite, an Loopback gebunden); ein Freelancer mit einem Tenant pro Kunde und Budgets, die Ausgaben verweigern, bevor sie auf der Rechnung erscheinen; ein Engineering-Team mit gemeinsamen Arbeitselementen, SSO und einem Audit-Trail, den niemand von Hand zusammenstellen muss; ein reguliertes Unternehmen mit Postgres samt Row-Level Security, HA, air-gapped Installationen und WORM-Archivierung. Der offene Build ist die gesamte Plattform, und die kommerziellen Add-ons sind additiver Code darauf, niemals aus dem offenen Produkt entfernte Funktionen; SSO, HA, WORM und Budgets, die tatsächlich verweigern, müssen Sie provisionieren und sind keine Standardeinstellungen beim ersten Start.

Es gibt keine verpflichtende Telemetrie und standardmäßig keinen Egress der Control Plane: Ihren Perimeter überschreitet, was Sie dafür konfigurieren — Aufrufe an Ihre Modell-APIs, die von Ihnen eingerichteten SIEM-/Webhook-Ausgaben, ein Embedding-Anbieter, wenn Sie einen provisionieren. Collectors lesen aus den Systemen, die Sie bereits betreiben, sodass ein ausfallender Collector niemals im Datenpfad der Produktion steht.

<div align="center">
<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/04-environments-dark.svg">
  <img src=".github/assets/04-environments-light.svg" width="840" alt="Eine Binary in jeder Größenordnung, vom Heimserver bis zum regulierten Unternehmen; wo sie läuft und worauf sie zugreift.">
</picture>
<sub>Derselbe offene Build vom Homelab bis zum regulierten Unternehmen.</sub>
</div>

## Was es tut

<div align="center">
<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/access-map-dark.png">
  <img src="docs-site/public/console/access-map-light.png" width="840" alt="Access Map: Was jeder Agent in Ihrem Estate liest und schreibt, Ursprünge links, Ressourcen rechts.">
</picture>
<sub><b>Access Map</b> — was jeder Agent in Ihrem Estate liest und schreibt, Lesen und Schreiben farblich codiert.</sub>
</div>

- **Sehen Sie es.** Inventar jedes entdeckten Agenten, jeder Session, jedes Modells, jedes MCP-Servers, jedes Tools und jeder Identität; eine **Access Map (R/RW-Map)** dessen, worauf jedes davon tatsächlich zugreift, mit einer Permitted-vs-Observed-**Drift**-Ansicht; Live-Sessions, der Orchestrierungsgraph, Health und SLA. Was nicht sichtbar ist, wird als `unknown` markiert, niemals erraten.
- **Die Arbeit ausführen.** Dauerhafte Arbeitselemente mit Eigentümerschaft, Abhängigkeiten, Akzeptanzkriterien und Entscheidungen; abgegrenzte Leases, sodass zwei Agenten — oder zwei Menschen — nicht gleichzeitig dasselbe Arbeitselement innehaben können; Sessions, die über die Konsole gestartet, angehängt und gestoppt werden; Delegation an autorisierte Peers über A2A. Shadow-Modus und endgültige Arbeitsautorität sind nicht gebaut und als nicht vorhanden aufgeführt: [Die Arbeitsebene](docs-site/src/content/docs/explanation/work-plane.md).
- **Steuern und durchsetzen.** Eine Cedar-Autorisierungs-Engine und **vier deny-closed Enforcement Points** — der Hook von Claude Code, ein Inline-`/v1/messages`-Inferenz-Proxy, ein MCP-`tools/call`-Gate und ein A2A-Delegation-Gate —, sodass eine unautorisierte Aktion blockiert, für eine Vier-Augen-Freigabe zurückgehalten oder, im Hook, vor ihrer Ausführung umgeschrieben wird; ein Punkt wird nur gezählt, solange ein Test seinen unkonfigurierten Pfad ausführt und die Verweigerung bestätigt. Budgets, die Ausgaben verweigern oder drosseln, Break-Glass mit Dual Control und ein Estate-**Kill-Switch**, der fail-closed arbeitet.
- **Gesteuert speisen.** Content-Quellen (SharePoint, Confluence, Google Drive, Notion, Salesforce, Snowflake, S3, Azure AI Search, SAP OData, PostgreSQL, ein auf ein Root-Verzeichnis beschränktes Filesystem) speisen ein gesteuertes Retrieval: lexikalisches Zero-Egress-Retrieval out of the box, modellgestütztes semantisches Retrieval, wenn Sie einen Embedder provisionieren, und Clearance, die zum Retrieval-Zeitpunkt deny-closed durchgesetzt wird.
- **Beweisen Sie es.** Ein hash-chained, Ed25519-signiertes Audit-Ledger; versiegelte Nachweise, abgebildet auf **26 Framework-Kataloge** (EU AI Act, NIST AI RMF, ISO 42001, SOC 2, ISO 27001, GDPR…) — selbst bewertete Kontrollfamilien, keine Zertifizierungen; SIEM/ITSM-Push (CEF/LEEF/syslog/OTLP/OCSF). Pro Deployment konfiguriert: menschliche und nicht-menschliche Identitäten (WebAuthn/FIDO2, PIV/CAC, Single-IdP-SSO, SCIM-Abgleich, Föderation von Agenten-Identitäten), Inline-Guardrails, DLP, BYOK/CMEK-Verschlüsselung und das Recht auf Löschung mit verifiziertem Key-Shredding.

**30 Module**, eine Konsole, **158 Integrationen** — Zahlen, die aus dem Code abgeleitet und bei jedem Push von [`scripts/check-public-counts.sh`](scripts/check-public-counts.sh) durchgesetzt werden. Eine Integration ist ein Connector-Verzeichnis mit Go-Code, und zwölf davon sind gemeinsam genutzte Bibliothekspakete: [`connectors/README.md`](connectors/README.md) enthält die Aufschlüsselung. Jedes Modul mit seinem Reifegrad: der [Modulkatalog](docs-site/src/content/docs/reference/modules/overview.md); die verdrahteten Connectors nach Coverage-Stufe: die [Connector-Referenz](docs-site/src/content/docs/reference/connectors.md).

<div align="center">
<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/03-agent-communication-dark.svg">
  <img src=".github/assets/03-agent-communication-light.svg" width="840" alt="Wie Agenten zusammenarbeiten: eine dauerhafte Arbeitsebene aus Arbeitselementen, abgegrenzten Leases und bereichsgebundenen Nachrichten; Delegation durch ein Durchsetzungstor; Shadow-Modus und endgültige Arbeitsautorität gestrichelt dargestellt, weil sie nicht gebaut sind.">
</picture>
<sub>Agenten teilen sich eine dauerhafte Arbeitsebene. Was nicht gebaut ist, wird als abwesend gezeichnet.</sub>
</div>

## Ein Blick in die Konsole

| | |
|---|---|
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/agentops-dark.png"><img src="docs-site/public/console/agentops-light.png" alt="Sessions von Claude Code, die über die Konsole erstellt, angehängt und gesteuert werden."></picture><br><sub><b>Claude Code</b> — Sessions ohne SSH über die Konsole erstellen, anhängen und steuern.</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/work-dark.png"><img src="docs-site/public/console/work-light.png" alt="Arbeit: der dauerhafte sessionübergreifende Backlog aus Arbeitselementen und Entscheidungen."></picture><br><sub><b>Arbeit</b> — der dauerhafte sessionübergreifende Backlog: Elemente, Eigentümerschaft, Akzeptanzkriterien, Entscheidungen.</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/orchestration-dark.png"><img src="docs-site/public/console/orchestration-light.png" alt="Orchestrierung und A2A: der aus beobachteten Signalen abgeleitete Agent-zu-Agent-Delegationsgraph."></picture><br><sub><b>Orchestrierung &amp; A2A</b> — wer an wen delegiert, abgeleitet aus beobachteten Signalen.</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/inventory-dark.png"><img src="docs-site/public/console/inventory-light.png" alt="Inventar: alle im Estate entdeckten Agenten, Sessions, MCP-Server, Modelle und Identitäten."></picture><br><sub><b>Inventar</b> — alle entdeckten Agenten, Sessions, MCP-Server, Modelle und Identitäten.</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/access-map-drift-dark.png"><img src="docs-site/public/console/access-map-drift-light.png" alt="Least-Privilege-Drift: unerwartete Zugriffe und ungenutzte Grants auf der Access Map."></picture><br><sub><b>Least-Privilege-Drift</b> — beobachtet, aber nicht erlaubt, sowie Grants, die niemand nutzt.</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/security-dark.png"><img src="docs-site/public/console/security-light.png" alt="Sicherheit und Forensik: Guardrail-Befunde, die Anomalie-Warteschlange und manipulationsnachweisbare Forensik."></picture><br><sub><b>Sicherheit &amp; Forensik</b> — Guardrail-Befunde, Anomalien, manipulationsnachweisbare Forensik.</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/killswitch-dark.png"><img src="docs-site/public/console/killswitch-light.png" alt="Kill-Switch: der Not-Aus für das Estate mit Wiederherstellung unter Dual Control."></picture><br><sub><b>Kill-Switch</b> — ein Klick stoppt jede gesteuerte Aktuierungsoberfläche; die Wiederherstellung erfordert zwei Benutzerkonten.</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/session-viewer-dark.png"><img src="docs-site/public/console/session-viewer-light.png" alt="Viewer für Session-Aufzeichnungen: Agentenaktivität und Governance-Nachweise auf einer Zeitachse, Kette verifiziert."></picture><br><sub><b>Session-Aufzeichnung</b> — Agentenaktivität und Governance-Nachweise auf einer Zeitachse, Kette verifiziert.</sub> |

Jedes Standbild ist ein Capture des bestückten Demo-Estates, ausgeliefert von der laufenden Binary (`bash scripts/docs-captures.sh` regeneriert den Rohsatz). Die vollständige Übersicht der Ansichten: die [Konsolenreferenz](docs-site/src/content/docs/reference/console.md).

## Installation

Jedes Release wird unter einer cosign-signierten Vertrauenskette ausgeliefert, die nach Artefakttyp verifiziert wird: ein cosign-signiertes Checksums-Manifest für die darin aufgeführten Archive, Pakete und SBOMs pro Archiv, ein SPDX-SBOM-Sidecar mit einer in-toto-Attestierung pro Archiv, cosign-Signaturen für das Container-Image mit dessen eigener SBOM-Attestierung sowie OpenVEX-Erklärungen und SLSA-Build-Provenance für den Satz. Bei einem Sicherheitsprodukt ist die Lieferkette Teil des Vertrauensmodells: [Verifizieren Sie es](docs/RELEASE-VERIFICATION.md), bevor Sie es ausführen.

**Bequemer HTTPS-Pfad.** Der Skripttext wird über HTTPS übertragen und von der Pipe nicht vorab verifiziert; sobald er läuft, erkennt er Ihr Betriebssystem und Ihre Architektur, verlangt `cosign`, verifiziert das signierte Checksums-Manifest und den SHA-256-Wert des Archivs, installiert ausschließlich die Binary und ruft niemals `sudo` auf. Fixieren Sie die Version, wenn Sie das Skript an eine Shell weiterleiten:

```sh
curl -fsSL https://raw.githubusercontent.com/olivaresai/olivares/main/scripts/install.sh | sh -s -- --version v26.8.0
olivares quickstart        # TLS on, loopback-only, no default credentials; prints the console URL + a one-time setup token
```

**Pfad für hohe Vertrauensanforderungen.** Laden Sie zuerst herunter, verifizieren Sie und führen Sie erst dann aus: Die Archive, Pakete und das Checksums-Manifest sind auf der [Release-Seite](https://github.com/olivaresai/olivares/releases/tag/v26.8.0) verfügbar, und [`scripts/verify-release.sh`](scripts/verify-release.sh) verifiziert, was vorhanden ist, und gibt an, was übersprungen wurde — standardmäßig keyless, auf einem vom Netz getrennten Host mit `--key … --offline`. Der [Vertrauensvertrag des Installers](docs/RELEASE-INSTALLER.md) beschreibt beide Pfade; der signierte, versionierte Installer mit seinem Opt-in-Service-Adapter wird ab dem ersten Release-Cut nach seiner Aufnahme ausgeliefert, und v26.8.0 liegt davor.

| Pfad | Das erhalten Sie |
|---|---|
| **Linux-Pakete** — `.deb`, `.rpm`, `.apk` | die Binary, eine gehärtete systemd-Unit, eine Beispiel-Env-Datei und einen `olivares`-Dienstbenutzer ohne Login; der Dienst wird nicht für Sie gestartet |
| **Container** — `docker.io/olivaresai/olivares:26.8.0` | distroless, non-root, Tags ohne `v`-Präfix; `ghcr.io/olivaresai/olivares` ist per Digest dasselbe Image. Das Standard-Image ist Multi-Arch (amd64/arm64); die Varianten `-fips` und `-stig` sind nur für amd64 verfügbar |
| **Homebrew** — `brew install olivaresai/tap/olivares` | die Release-Binary unter macOS und Linux, gegen die signierten Checksums geprüft, mit aufgehobener Gatekeeper-Quarantäne; die darwin-Builds sind noch nicht von Apple notarisiert |
| **Kubernetes** — [`deploy/helm/olivares`](deploy/helm/olivares) oder [`deploy/manifests/install.yaml`](deploy/manifests/install.yaml) | die Quelle des Helm-Charts und ein flaches, Helm-freies Manifest im Tree; das Chart ist **noch nicht in einer OCI-Registry veröffentlicht** |
| **Aus dem Quellcode** — `task build` (Go 1.26+, [Task](https://taskfile.dev), pnpm) | `./bin/olivares quickstart`, derselbe standardmäßig sichere Erststart |

Die Engine ist **secure by default**: Sie bindet an Loopback, stellt beim ersten Start HTTPS mit einem selbstsignierten Zertifikat bereit, enthält keine Standard-Zugangsdaten und gibt ein Einmal-Setup-Token aus; in einem Container oder Pod lauscht der Prozess in seinem eigenen Netzwerk, und das Host-Mapping oder der Service hält ihn privat. **Windows** ist noch nicht gebaut — führen Sie den Linux-Container oder WSL2 aus ([Plan](INSTALL.md#windows)). Die Matrix pro Betriebssystem und das Produktions-Setup: [`INSTALL.md`](INSTALL.md); die Deployment-Anleitungen (Compose, Kubernetes, air-gapped) und [Upgrades](docs-site/src/content/docs/how-to/upgrade-and-rollback.md): [`docs-site/`](docs-site/).

## Schnellstart

Erkunden Sie ein synthetisches Estate oder starten Sie es für den echten Einsatz. Beide verwenden dieselbe Binary.

```sh
# a deterministic demo estate — loopback-only, no real data
olivares serve --seed-demo --insecure --listen 127.0.0.1:8901 --grpc-listen 127.0.0.1:8902 --data-dir "$(mktemp -d)"
# open http://127.0.0.1:8901 — inventory, work, orchestration, access map + drift, policies, FinOps

# the real thing — TLS on, loopback; create the first administrator with the printed token
olivares quickstart
```

Der Demo-Seed dient nur zum Lernen (Passwort aus dem öffentlichen Quellbaum): Richten Sie ihn niemals auf echte Daten. CI durchläuft denselben Pfad mit `task smoke:quickstart` und prüft die Access-Map- und Drift-Zahlen (20 Knoten / 13 Kanten, mit 8 unerwarteten Zugriffen und 2 ungenutzten Grants), sodass diese Seite nicht unbemerkt vom Code abweichen kann. Der [vollständige Schnellstart](docs-site/src/content/docs/start/quickstart.md) verdrahtet einen echten pgAudit-Connector und verlinkt die Installationspfade für die Produktion.

## Editionen

<div align="center">
<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/05-editions-dark.svg">
  <img src=".github/assets/05-editions-light.svg" width="840" alt="Editionen nach Zusammensetzung: Der AGPL-Core ist die gesamte Plattform, die Add-ons sind additiver Code darauf, Cloud Standard ist der Managed Service.">
</picture>
<sub>Editionen nach Zusammensetzung. Paketierung und Preise auf Anfrage.</sub>
</div>

Der AGPL-Build ist die gesamte Plattform und wird von innen niemals durch Feature-Caps begrenzt; die kommerziellen Add-ons sind additiver Code, niemals aus dem offenen Produkt entfernte Funktionen. Ein Abonnement ist das Credential zum Herunterladen signierter Modulpakete — ein Distributionsmodell, kein Schlüssel, der bereits auf Ihrer Festplatte vorhandenen Code entsperrt. Benutzerkonten sind in der self-hosted Engine unbegrenzt, und alle **vier deny-closed Enforcement Points** sind offen. Die bereichsweise Matrix der offenen, kommerziellen und geplanten Fähigkeiten: [`LICENSING.md`](LICENSING.md) und [Open Core & Lizenzierung](docs-site/src/content/docs/explanation/open-core-and-licensing.md).

## Architektur

<div align="center">
<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/02-architecture-dark.svg">
  <img src=".github/assets/02-architecture-light.svg" width="840" alt="Architektur: Agentenoberflächen, Audit-Quellen, MCP- und A2A-Peers sowie Content-Quellen werden in einer self-hosted Binary erfasst, die Konsole, REST-API, gRPC, CLI und Terraform-Provider bereitstellt; die Control Plane der Cloud (gebaut, nicht bereitgestellt) und das Lizenzportal (bereitgestellt, Verkauf deaktiviert) sind als separate Ebenen dargestellt.">
</picture>
</div>

Eine statische Go-Binary bettet die Konsole ein und stellt vier Oberflächen mit dokumentierter Abdeckung bereit: die REST-API (primär), einen fokussierten gRPC-Spiegel des stabilen Kerns, die `olivares`-CLI und einen Terraform-Provider. Collectors laufen in Ihrer Infrastruktur in drei Modi; der Store ist SQLite oder Postgres mit Row-Level Security, die einmal in der Store-API und erneut durch Postgres durchgesetzt wird. Einzelheiten, darunter die Arbeitsebene im Detail: [`ARCHITECTURE.md`](ARCHITECTURE.md).

## Dokumentation

[docs.olivares.ai](https://docs.olivares.ai) — getestete Installations-Tutorials (Single Node, Docker Compose, Kubernetes/Helm, air-gapped), Connector-Anleitungen mit echten Konsolen-Captures, ein Cookbook (deny-closed Richtlinien, Budgets, Freigaben, Kill-Switch-Übungen, SIEM-Push), API-Referenz und ein Glossar. Beginnen Sie bei [Was ist Olivares AI](docs-site/src/content/docs/start/what-is-olivares-ai.md) und [Ehrlichkeit & Grenzen](docs-site/src/content/docs/start/honesty-and-limits.md).

## Sicherheit

Melden Sie eine Schwachstelle privat über [`SECURITY.md`](SECURITY.md), niemals als öffentliches Issue. Die Engine ist read-first und datensparsam: Die Access Map speichert Kanten, keine Payloads, und das Öffnen der Access Map ist eine aufgezeichnete Aktion. Advisory-Ablauf: [`docs/security-advisories.md`](docs/security-advisories.md); Übersicht der Lieferkettennachweise: [`docs/openssf-badge.md`](docs/openssf-badge.md).

## Community

[`CONTRIBUTING.md`](CONTRIBUTING.md) (Setup, DCO/CLA, SPDX, die Connector-Grenze) · [`CODE_OF_CONDUCT.md`](CODE_OF_CONDUCT.md) (Contributor Covenant 2.1) · [`SUPPORT.md`](SUPPORT.md) · [`GOVERNANCE.md`](GOVERNANCE.md) · [`CHANGELOG.md`](CHANGELOG.md) (Keep a Changelog 1.1, CalVer `vYY.M.PATCH`).

## Lizenz

`core/`, `modules/` und `web/` sind **AGPL-3.0-only**; `sdk/`, `connectors/` und `clients/` sind **Apache-2.0**, und ein Connector importiert niemals die Engine. Die kommerziellen Add-ons sind separat, optional und geschlossen — ausschließlich mit `-tags enterprise` gebaut, niemals in diesem Repository oder der offenen Binary; für kommerzielle Lizenzierung kontaktieren Sie `enterprise@olivares.ai` — [`LICENSING.md`](LICENSING.md). Beiträge erfordern ein DCO-Sign-off (`git commit -s`) und das [CLA](CLA.md).

> **Keine Gewährleistung, keine Haftung.** Die Software wird **wie besehen** bereitgestellt, **ohne Gewährleistung jeglicher Art** und **ohne Haftung für Datenverlust, Betriebsunterbrechung oder entgangenen Gewinn**. Bei einer Control Plane ist das keine Formalie: Eine Fehlkonfiguration kann legitime Arbeit blockieren oder genau das durchlassen, was Sie stoppen wollten. Es gelten AGPL-3.0-only §§15–16, Apache-2.0 §§7–8 sowie die projekteigene Zusatzbestimmung — [`DISCLAIMER.md`](DISCLAIMER.md).

## Das Projekt unterstützen

Der Kern ist frei und bleibt frei; jedes Release signiert, verifiziert und aktuell zu halten, ist dauerhafte Arbeit. Wenn Olivares AI für Sie nützlich ist, können Sie es über GitHub Sponsors unterstützen — [github.com/sponsors/olivaresai](https://github.com/sponsors/olivaresai) oder [github.com/sponsors/fran-olivares](https://github.com/sponsors/fran-olivares) — oder einmalig über Ko-fi. Sponsoring ist kein Support-Vertrag und kauft keine Priorität ([`SUPPORT.md`](SUPPORT.md)); Sponsoren, die namentlich genannt werden möchten, werden in [`SUPPORTERS.md`](SUPPORTERS.md) aufgeführt.

[![ko-fi](https://ko-fi.com/img/githubbutton_sm.svg)](https://ko-fi.com/Z1R625SAD2)

---

<div align="center">

<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/olivares-mark-dark.svg">
  <img src=".github/assets/olivares-mark-light.svg" alt="Olivares AI" width="44">
</picture>

<sub><strong>Ground Truth für KI im Unternehmen.</strong> · <a href="https://olivares.ai">olivares.ai</a> · <a href="LICENSING.md">AGPL-3.0 + commercial</a></sub>

</div>
