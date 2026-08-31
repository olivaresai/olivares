---
title: Vertrauen & Beschaffung
description: >-
  Was das Sicherheitsteam eines Käufers heute überprüfen kann:
  Zertifizierungsbereitschaft (keine Behauptungen), das Pentest-Programm, das
  Support-Reaktionsziel-Modell, die Barrierefreiheitskonformität und maschinenlesbare
  Compliance-Nachweise — und was ehrlicherweise noch nicht vorhanden ist.
---

Diese Seite ist der Einstiegspunkt für Sicherheits-, Compliance- und
Beschaffungsteams, die Olivares AI evaluieren. Die Compliance-Haltung des
Produkts folgt einer Regel, die im Code ebenso durchgesetzt wird wie im Text:
**benenne, was gebaut und überprüfbar ist; behaupte niemals eine Attestierung,
die nicht existiert.** Das Compliance-Modul meldet eine Kontrolle, die nur durch
Design-Nachweise gestützt ist, als `by_design` — niemals als `satisfied` — und
jeder Framework-Eintrag im Katalog trägt seinen eigenen „keine Zertifizierung"-
Hinweis.

:::note[Aktueller Stand, keine Überraschungen]
Olivares AI besitzt **keinen SOC-2-Bericht, kein ISO/IEC-27001- oder
-42001-Zertifikat**, hat sich **noch keinem Penetrationstest durch Dritte
unterzogen** und ist **nicht** im CSA STAR Registry gelistet. Was stattdessen
existiert — und vor Vertragsabschluss wohl nützlicher ist — ist ein überprüfbares
Bereitschaftspaket: Kontrolle-für-Kontrolle-Zuordnungen zu Nachweisen, die Sie
selbst aus einer laufenden Bereitstellung abrufen können, plus die explizite
Liste der Entscheidungen (Zertifizierungsaufträge, Pentest-Beauftragung,
Aktivierung des kommerziellen Supports), die noch offen sind. FedRAMP/ATO ist für
das selbst gehostete Produkt explizit außerhalb des Geltungsbereichs.
:::

## Das Vertrauenspaket

Das vollständige käuferseitige Paket befindet sich im Repository unter
`docs/trust/`:

- **Zertifizierungsbereitschaft** — SOC 2 Type II, ISO/IEC 27001:2022 und
  ISO/IEC 42001:2023: Zuordnungen von jeder Kontrolle zur Produktfähigkeit und
  zum Live-Nachweis-Endpunkt, der sie stützt, einschließlich der KI-spezifischen
  Nachweise, nach denen ein Auditor 2026 fragt (Prompt-/Interaktions-Logging,
  Modellversionierung, Lineage, LLM-Sub-Prozessor-Inventar).
- **Antwortbank für Fragebögen** — vorab verifizierte Anbieterantworten,
  abgestimmt auf die Shared-Assessments-SIG-2026-Domänen und bereit zur
  Übertragung in einen CSA AI-CAIQ für STAR for AI Level 1.
- **Penetrationstest-Programm** — zugesagte Kadenz (festgelegter Drittanbieter-
  Test bei der ersten kommerziellen GA, danach jährlich, ereignisgesteuerte
  Wiederholungstests), Geltungsbereich und ein Behebungsworkflow, der mit dem in
  `SECURITY.md` veröffentlichten CVE-Behebungszielen verdrahtet ist.
- **Referenzarchitektur** — Bereitstellungstopologien (Single-Node, HA
  Active-Passive, Multi-Region, air-gapped), Trust Zones, gemessene
  Dimensionierungs-Baselines, RPO/RTO-Stufen und die
  IdP-/SIEM-/ITSM-/KMS-Integrationsfläche.
- **EU-Beschaffungsartefakte** — eine Vorlage für die technische Dokumentation
  gemäß Anhang IV des EU AI Act, befüllt aus Live-Nachweisen, sowie ein
  klauselweiser Abgleich mit den MCC-AI-Mustervertragsklauseln der Kommission
  (High-Risk- und Light-Varianten).
- **Agent-Safety-Case** — eine vorausschauende, CAE-artige strukturierte
  Argumentationsvorlage mit ehrlichen Restrisiko-Spalten.
- **Single-Vendor-Risiko** — der Tragfähigkeitseinwand strukturell beantwortet:
  Der AGPL-Core ist die vollständige Governance-Plattform, intern ohne zum Upselling
  funktionsbeschränkte Teile (eine kleine additive kommerzielle Produktlinie wird separat
  gebaut, privat verteilt und fehlt im offenen Binary — sie ergänzt Fähigkeiten, nimmt dem
  offenen Core aber niemals welche). In diesem offenen Binary dient der Lizenzschlüssel nur
  der Attestierung, funktioniert offline und aktiviert nichts. Builds sind reproduzierbar und
  Provenance-attestiert, sodass die Kontinuität nicht von der Existenz des
  Anbieters abhängt.

## Was Sie überprüfen können, ohne uns zu vertrauen

Self-Hosting kehrt das übliche Attestierungsverhältnis um: die meisten
Kontrollen, die ein SOC-2-Bericht attestieren würde, können Sie direkt in Ihrer
eigenen Bereitstellung überprüfen.

- **Releases:** cosign-Signaturen, SBOM, SLSA-Build-L3-Provenance (SLSA v1.2), OpenVEX — siehe
  [Ein Release verifizieren](/de/how-to/verify-a-release/).
- **Sicherheitskontakt & Offenlegung:** der Meldekanal, die Frist für die koordinierte
  Offenlegung und die CVE-Behebungsziele werden in `SECURITY.md` veröffentlicht und
  maschinenlesbar unter [`/.well-known/security.txt`](https://olivares.ai/.well-known/security.txt)
  (RFC 9116) bekannt gegeben, sodass ein Scanner oder Forscher den Kanal ohne Nachfrage findet.
- **Manipulationsnachweis:** das append-only, hash-chained, pro Ereignis
  signierte Audit-Ledger verifiziert offline — siehe das
  [Sicherheitsmodell](/de/explanation/security/security-model/).
- **Live-Compliance-Nachweise:** Framework-Status, Gap-Analyse, versiegelte
  Nachweispakete (JSON/CSV/OSCAL), Modell-AIBOMs (CycloneDX 1.6 / SPDX 3.0.1 AI
  Profile), Model Cards und der Regulierungskalender sind allesamt
  API-Antworten, keine PDFs — das Produkt behandelt Compliance-Daten und
  -Zuordnungen als versionsfixierte Daten.
- **Betriebliche Aussagen:** SLOs, Dimensionierungs- und RPO/RTO-Zahlen in der
  Referenzarchitektur sind auf gemessene, im Repository festgeschriebene
  Baselines zurückführbar.

## Support und Barrierefreiheit

- Das Support-Modell (Stufen, schweregradbasierte Reaktionsziele, Eskalation)
  ist in `SUPPORT.md` veröffentlicht — einschließlich der ehrlichen Offenlegung,
  dass der kommerzielle Support zwar definiert, aber noch nicht erwerbbar ist,
  und dass die Eskalationskette heute eine Person tief ist.
- Der Barrierefreiheits-Konformitätsbericht ist ein vollständiger **VPAT
  2.5Rev INT**-Edition-ACR (WCAG 2.1/2.2 AA + Revised Section 508 + EN 301 549
  V3.2.1) unter `docs/accessibility/VPAT-olivares-admin.md`, wobei der formale
  Test mit assistiver Technologie noch aussteht und als solcher offengelegt ist.
  Die Konsole wird in Englisch und Spanisch ausgeliefert; die i18n-Roadmap über
  EN/ES hinaus ist nachfragegesteuert und im Vertrauenspaket dokumentiert.

## Öffentliches Trust Center

Das [Trust Center](https://olivares.ai/trust) auf der Produktwebsite präsentiert dieselben Lieferkettenartefakte, die oben beschrieben sind, auf einer öffentlichen, eigenständigen Seite: SLSA-Build-L3-Attestierungen, Cosign-Signaturen, SBOM-Downloads, OpenVEX-Hinweise und das Verifizierungsskript. Inhaber kommerzieller Lizenzen können über das [Kundenportal](https://licenses.olivares.ai/portal) auf versionsspezifische Compliance-Artefakte zugreifen.

## Wie es weitergeht

- [Sicherheitsmodell](/de/explanation/security/security-model/) — wie sich die
  Plattform selbst verteidigt.
- [Bedrohungsmodell](/de/explanation/security/threat-model/) — Angreifer und
  Vertrauensgrenzen.
- [Ehrlichkeit und Grenzen](/de/start/honesty-and-limits/) — was heute läuft im
  Vergleich zu dem, was produktweit geplant ist.
- [Trust Center](https://olivares.ai/trust) — öffentliche Lieferkettenverifizierung und Compliance-Status.
