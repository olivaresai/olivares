---
title: Open Core & Lizenzierung
description: >-
  Open Core: das vollständige Produkt steht unter AGPL-3.0-only, das SDK und die
  Connectors stehen unter Apache-2.0, und eine kleine additive Enterprise-Linie ist
  kommerziell. Der AGPL-Build wird nie beschnitten, um zum Kauf zu drängen, ist aber
  nicht identisch mit der kommerziellen Edition. Was das für Self-Hoster und
  Connector-Autoren bedeutet.
---

Olivares AI ist **Open Core**. Das vollständige Community-Produkt wird unter der GNU
Affero General Public License veröffentlicht. Der AGPL-Build ist die gesamte
Governance-Plattform mit unbegrenzten Benutzern und einem aktiven Identitätsanbieter
(IdP). Die kommerzielle `enterprise/`-Linie ergänzt neuen Code, der nur mit
`-tags enterprise` gebaut wird und im öffentlichen Binary fehlt. Business enthält
vier Funktionsfamilien in einem Abonnement. Enterprise deckt vertraglich vereinbarten
Umfang ab. Eine kommerzielle Lizenz bietet die rechtliche Ausnahme vom Copyleft;
öffentlich veröffentlichte Funktionen werden nicht hinter eine Bezahlschranke verschoben.

## Die Lizenzgrenze

Die Lizenzierung folgt dem Quellbaum. Jede Datei trägt einen SPDX-Header, und die
Grenze wird in CI durchgesetzt (ein Connector darf niemals die Engine importieren):

| Pfad | Lizenz | Was es ist |
|---|---|---|
| `core/` | **AGPL-3.0-only** | die Engine: Ingest, Event-Bus, Datenmodell, Modul-Runtime, API, Authz, Audit |
| `modules/` | **AGPL-3.0-only** | die 31 Module (Inventar, die R/RW-Map, FinOps, Evals, Guardrails, …) |
| `web/` | **AGPL-3.0-only** | die React-Oberfläche |
| `sdk/` | **Apache-2.0** | die Connector-/Modul-Schnittstellen, der gRPC-Kontrakt und die gemeinsamen Typen |
| `connectors/` | **Apache-2.0** | die Connectors (Claude, OpenAI, pgAudit, eBPF, Cloud, Slack, SIEM, …) |
| `enterprise/` | **kommerziell** | additive Module, per Build-Tag gated, niemals im öffentlichen Binary: Multi-IdP-Federation, Content-Firewall/DLP, Hook-Hardening, kompilierter Threat-Intel-Katalog, Server-Tool-Egress, CyberArk Conjur, Incident-Close-Loop (`LicenseRef-Olivares-Commercial`) |

Die Dokumentationsseite, die Sie gerade lesen, ist Teil des AGPL-Produkts.

## Was das für Sie bedeutet

- **Self-Hosting des Produkts (AGPL).** Sie können das vollständige Produkt unter der
  AGPL betreiben, untersuchen, modifizieren und weiterverbreiten. Die
  Netzwerknutzungsklausel der AGPL gilt: Wenn Sie anderen eine modifizierte Version
  über ein Netzwerk anbieten, müssen Sie ihnen Ihren modifizierten Quellcode anbieten.
  Beim internen Self-Hosting ist dies selten ein Problem; wenn Sie ein Produkt *auf
  Basis von* Olivares AI ohne diese Verpflichtung bauen möchten, existiert die
  kommerzielle Lizenz genau dafür.
- **Connectors bauen (Apache-2.0).** Das SDK und die Connectors stehen unter
  **Apache-2.0** — permissiv, kein Copyleft. Sie können einen Connector schreiben, ihn
  proprietär halten und ihn ausliefern, wie es Ihnen gefällt. Die architektonische
  Grenze, die dies sicher macht, wird durchgesetzt: ein Apache-2.0-Connector
  **importiert niemals die AGPL-Engine**; er hängt nur vom SDK ab. Das hält das
  Connector-Ökosystem frei von Copyleft-Reibung.
- **Eine kommerzielle Lizenz.** Organisationen, die eine Ausnahme von den
  AGPL-Pflichten benötigen, können **enterprise@olivares.ai** kontaktieren.
  Business enthält die vier unten genannten Funktionsfamilien in einem Abonnement.
  Enterprise-Bedingungen werden vereinbart; die Familien werden nicht separat verkauft.

## Editionen und Preise

| Edition | Preis | Umfang |
| --- | --- | --- |
| Community | Kostenlos, AGPL-3.0-only | Unbegrenzte Benutzer; ein aktiver Identitätsanbieter (IdP). |
| Business | USD 129/Monat oder USD 1,290/Jahr | Unbegrenzte Benutzer; eine juristische Person; bis zu zwei Produktionsdeployments mit je einem Staging-Deployment; insgesamt bis zu fünf aktive IdPs. |
| Enterprise | Kontaktieren Sie uns | Mehrere juristische Personen, weitere Deployments oder IdPs, Air-Gap-Mirrors, individuelles LTS und andere vereinbarte Anforderungen. |

Business enthält **Regulated Operations**, **AI Runtime Security**,
**Compliance Packs** und **Identity & Scale**. Jede Familie behält ihren eigenen Code
und ihre Lizenzberechtigungsgrenze. Sie können jede Familie aktivieren oder deaktivieren;
keine wird separat verkauft. Die private Implementierung wird als kommerzielles
Binary außerhalb des öffentlichen Repositorys verteilt.

### Kann ich eine Funktionsfamilie separat kaufen?

Nein. Die vier genannten Familien sind im Business-Abonnement enthalten.
Wählen Sie monatliche oder jährliche Abrechnung unter [Preise](https://olivares.ai/pricing).

### Was gilt, wenn ich mehr Deployments oder Identitätsanbieter benötige?

Kontaktieren Sie **enterprise@olivares.ai** für Enterprise. Business erlaubt bis zu
zwei Produktionsdeployments und insgesamt fünf aktive IdPs, nicht fünf pro Deployment.


## Was offen ist gegenüber Enterprise

Das offene Binary ist die gesamte Governance-Plattform; die `enterprise/`-Linie ist
**additiv**. Zwei Grenzen sind besonders hervorzuheben, weil der offene Build sie
ehrlich beantwortet, statt sie vorzutäuschen:

- **SSO** — Single-IdP-Login (OIDC + SAML 2.0) ist im Standard-Binary **offen**:
  echtes Login, kein `-tags enterprise`. Mehrere aktive IdPs (pro Tenant / nach
  Domain), SSO-Enforcement und managed SCIM sind die reservierte Enterprise-Linie;
  das Aktivieren eines zweiten aktiven IdP gibt `multi_idp_requires_enterprise`
  zurück.
- **Benutzerkonten** — **unbegrenzt in jeder Edition**. Der Community-Build hat kein
  Benutzerlimit, der Enterprise-Build ebenso wenig: kein Lizenzzustand (gültig,
  abgelaufen, keine) kann begrenzen, wie viele Konten eine Bereitstellung betreibt. Das
  Limit von drei aktiven Konten, das vor dem 2026-07-27 galt, wurde vollständig
  entfernt; die Seat-Naht bleibt als Kompatibilitäts-No-op im Code, die nichts
  ablehnt, und ein Lizenzablauf begrenzt, deaktiviert oder löscht nie ein Konto.

Siehe [Ehrlichkeit & Grenzen](/de/start/honesty-and-limits/) für das vollständige Bild
von offen gegenüber Enterprise.

## Der Lizenzschlüssel beschränkt niemals das offene Produkt

Dies ist wichtig und beabsichtigt: Im offenen (AGPL-)Binary ist die
Lizenzvalidierung **reine Attestierung**. Die Engine erfasst, wer eine Lizenz hält
und welchen Status sie hat; sie **deaktiviert, degradiert oder blockiert niemals**
eine Anfrage, ein Modul oder den Boot-Vorgang aufgrund einer Lizenzprüfung, und sie
läuft **offline** (eine Ed25519-Signatur, kein Lizenzserver), weshalb das offene
Produkt air-gapped (vom Netz getrennt) funktioniert. Die einzige Stelle, an der die
Lizenz *konsumiert* statt nur angezeigt wird, ist der geschlossene Enterprise-Build,
und nur um die von der kommerziellen Vereinbarung abgedeckten Module zu
berechtigen, ausgewertet pro Modul — eine lokale Entscheidung in der
kommerziellen Edition, niemals eine Prüfung im offenen Binary. Benutzer werden nie
gedeckelt: Konten sind in jeder Edition unbegrenzt. So ist der offene Build wirklich
vollständig und nicht per Lizenz gedeckelt; was sich in der kommerziellen Edition
unterscheidet, sind die additiven `enterprise/`-Module, nicht ein Lizenzschlüssel,
der Funktionen innerhalb desselben Binary umlegt.

## Warum dieses Modell

Das Beschneiden des Core wurde verworfen: die offene Edition erledigt die gesamte
Aufgabe — die vollständige Governance-Schleife auf einem Node — sie zu deckeln würde
sie zu einem schlechteren Produkt machen und das Vertrauen untergraben.
Permissiv-für-alles (MIT/Apache auf dem Core) würde den Core ohne kommerzielle
Grundlage verschenken. Source-available, nicht-OSS-Lizenzen (BSL, SSPL und ähnliche)
würden die Open-Source-Adoption abwürgen, die der ganze Sinn eines erweiterbaren
Connector-Ökosystems ist. Das Modell ist also **Open Core**: ein Copyleft-Produkt,
das für sich genommen vollständig und glaubwürdig ist, ein permissives SDK, das das
Connector-Ökosystem reibungslos hält, und eine kleine **additive** kommerzielle
Linie aus neuem Code, der nie im offenen Build war — plus eine saubere kommerzielle
Ausnahme — *ohne jemals das zu degradieren, was Sie selbst hosten können*.

## Mitwirken

Beiträge werden unter den Beitragsbedingungen des Projekts angenommen (das Repository
liefert sowohl ein DCO als auch ein CLA, plus eine Markenrichtlinie). Den aktuellen
Prozess finden Sie im `CONTRIBUTING`-Leitfaden des Repositorys.

## Verwandt

- [Eine Lizenz installieren und zu Enterprise wechseln](/de/how-to/install-a-license/) —
  wohin eine erworbene Lizenz gehört und wie der Community-→-Enterprise-Austausch in-place
  erfolgt. Diese Seite erklärt das Modell; die andere beschreibt die Schritte.
- [Sicherheitsmodell](/de/explanation/security/security-model/) — warum
  attestierungsbasierte Lizenzierung für ein air-gapped Sicherheitsprodukt wichtig ist.
