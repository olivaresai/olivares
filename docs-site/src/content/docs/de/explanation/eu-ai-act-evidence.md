---
title: EU-AI-Act-Evidenz aus Laufzeitdaten
description: >-
  Wie eine self-hosted Control Plane das Live-Verhalten Ihres AI-Estates in die
  technische Evidenz verwandelt, die eine EU-AI-Act-Akte braucht — Annex-IV-förmig,
  aus Laufzeitdaten generiert und in der von Ihnen selbst betriebenen Control Plane
  gespeichert. Für regulierte EU-Käufer, die keine US-SaaS-Control-Plane in ihren
  Compliance-Pfad setzen können.
---

Die meisten AI-Governance-Werkzeuge produzieren Evidenz so, wie ein Foliensatz
Fakten produziert: Jemand schreibt sie auf, und Sie vertrauen darauf, dass sie
wahr war. Unter der **Verordnung (EU) 2024/1689 (der EU AI Act)** reicht das nicht.
Der Anbieter eines Hochrisiko-Systems muss die **technische Dokumentation nach
Anhang IV** *vor* dem Inverkehrbringen des Systems erstellen und sie über den
Lebenszyklus **aktuell halten** (Art. 11), und der Plan zur Beobachtung nach dem
Inverkehrbringen (Art. 72) muss von dem gespeist werden, was das System
tatsächlich in der Produktion tut.

Diese Seite erklärt, wie Olivares AI Sie diese Evidenz aus dem
**Laufzeitverhalten Ihres Estates** **generieren** lässt, statt sie von Hand zu
kuratieren — und warum eine **self-hosted, AGPL-Control-Plane** die Form ist, die
die Prüfung eines regulierten EU-Käufers übersteht, während eine US-SaaS-Control-
Plane das nicht tut.

:::note[Wer ist hier der „Anbieter“]
Olivares AI ist **Governance-Werkzeug über AI-Systemen, nicht selbst ein
Hochrisiko-System nach Anhang III** im typischen Einsatz. Ob *Ihr* AI-System
hochriskant ist und wer dessen Anbieter oder Betreiber ist, ist eine rechtliche
Bestimmung, die Sie treffen — nicht wir. Was wir tun, ist, die **Pflichten zur
technischen Dokumentation und Beobachtung mit echter Evidenz günstig erfüllbar zu
machen**. Siehe [Ehrlichkeit & Grenzen](/de/start/honesty-and-limits/) dazu, was die
Plattform behauptet und was nicht.
:::

## Warum „aus Laufzeitdaten“ der eigentliche Punkt ist

Die Dokumentationspflichten des EU AI Act sind nicht einmalig. Anhang IV verlangt
die Architektur des Systems, seine **Rechenressourcen**, seine Überwachungs- und
Kontrollmerkmale, seine Leistungsmetriken, sein Risikomanagementsystem und einen
Nachweis von **Lebenszyklusänderungen** — und Art. 72 erfordert einen Plan zur
Beobachtung nach dem Inverkehrbringen, den Sie tatsächlich durchführen. Ein
statisches Word-Dokument veraltet in dem Moment, in dem ein Modell ausgetauscht
wird oder ein Agent ein neues Tool erhält.

Olivares AI beobachtet das Estate bereits, um seine
[Read/Write Access Map](/de/explanation/#die-access-map-read-first-minimal-data-permitted-vs-observed)
und sein append-only, hash-chained, Ed25519-signiertes
[Audit-Ledger](/de/reference/glossary/#audit-ledger) zu bauen. Das Compliance-Modul
verwandelt dieselben Beobachtungen in **für Prüfer konsumierbare Evidenzpakete**:
versiegelt, ledger-verankert, exportierbar als JSON, CSV oder **OSCAL**, mit einem
Live-Integritätsnachweis. Das Dokument ist *abgeleitet aus dem, was passiert ist*,
nicht behauptet über das, was beabsichtigt war.

Zwei Ehrlichkeitsregeln sind in das Produkt verdrahtet und gehen direkt in die
Evidenz über:

- Ein Control, dessen einzige Untermauerung architektonisch ist, meldet
  **`by_design`**, niemals `satisfied`. „Satisfied“ erfordert verknüpfte, echte
  Mandanten-Evidenz.
- Der Framework-Katalog ist **version-gepinnt an seine Primärquelle** mit einem
  `verified_on`-Datum, und jedes Framework trägt einen „dies ist ein technisches
  Mapping, keine Zertifizierung“-Disclaimer.

## Der Annex-IV-Crosswalk, kurz gefasst

Das Compliance-Modul bildet die EU-AI-Act-Artikel, die es belegen kann —
**Art. 5, 6, 9, 10, 11, 12, 13, 14, 15, 50 und 72** — auf Fähigkeiten ab, die die
Control Plane bereits produziert. Unten steht die Anhang-IV-Abschnittsansicht; die
vollständige zeilenweise Vorlage (mit genauen Endpunkten und den expliziten Lücken)
wird im Trust- & Procurement-Paket als `eu-ai-act-annex-iv.md` ausgeliefert.

| Anhang-IV-Thema | Was die Control Plane bereitstellt | Gezogen aus |
|---|---|---|
| **1.** Allgemeine Beschreibung (Zweck, Anbieter, Versionen, Bereitstellung) | Modellinventar + Versionen; **Model Card** (JSON/Markdown; unbekannte Felder sind explizit `not_recorded`, niemals erfunden) | `GET /v1/m/models/owned-models/{id}/model-card` |
| **2.** Entwicklungsprozess, Architektur, **Rechenressourcen**, Datenherkunft, Aufsicht, V&V | Referenzarchitektur; **Compute/Cost Accounting** pro Inferenz (die *operative* Seite von 2(c) — Trainings-Zeit-Zahlen werden **nicht** belegt, und der Katalog sagt das); Dataset-Registry + versiegelte **AIBOM** (CycloneDX 1.6) und **SPDX 3.0.1 AI Profile**; Approvals/HITL-Konfiguration; Eval- + Red-Team-Ergebnisse | FinOps-Kostenproben; `GET /v1/m/models/owned-models/{id}/aibom?format=spdx`; Evals-Modul |
| **3.** Überwachung, Funktionsweise & Kontrolle | Live-Betriebsevidenz: Guardrail-/Anomalie-Findings, Access Map + **Permitted-vs-Observed-Drift**, Session-Timelines, Kill-Switch-Status | Findings; `GET /v1/m/accessmap/drift` |
| **4.** Leistungsmetriken | Eval-Methodik + Ergebnisse (LLM-Judge-Kalibrierung, blockierende Regressions-Gates) | Evals-Modul |
| **5.** Risikomanagementsystem (Art. 9) | Risikoklassifizierung pro Agent (EU-Stufe × NIST-Funktion), dual-control gesteuerte Review, Risikoregister-Export | `GET /v1/m/compliance/risk`; `GET /v1/m/compliance/dora` |
| **6.** Lebenszyklusänderungen | Change-/Deploy-Ledger; Model-Admission-Historie; Versionslebenszyklus | Deploy-Datensätze; `GET /v1/m/models/model-admissions` |
| **7.** Angewandte Standards | Der **26-Framework-Katalog**, version-gepinnt, mit `verified_on` | `GET /v1/m/compliance/frameworks` |
| **8.** EU-Konformitätserklärung (Art. 47) | **Nicht generiert** — ein Rechtsakt des Anbieters; die Plattform speichert/verknüpft sie nur | vom Anbieter geliefert |
| **9.** Plan zur Beobachtung nach dem Inverkehrbringen (Art. 72) | Kontinuierliche Evidenz, die der Plan zitieren kann: Findings, SLOs, Incident-Kommunikation, Ledger- + SIEM-Export | Production-Readiness + Status-/Incident-Dokumente |

### Die ehrlichen Lücken, vorab benannt

Diese in die Akte aufzunehmen *stärkt* sie — ein Assessor vertraut einem Dokument,
das seine eigenen Grenzen benennt.

- **Trainings-Zeit-Compute, statistische Qualität/Bias des Datasets und
  Design-Begründung** werden von der Control Plane nicht belegt. Diese sind vom
  Anbieter verfasst.
- **Art.-50-Transparenzpflichten** (Interaktionshinweise, Kennzeichnung von
  AI-Inhalten) sind eine ehrliche Lücke der Plattform selbst, als solche im Katalog
  vermerkt.
- Die Control Plane belegt die **operative** Hälfte von Anhang IV — was Ihr Estate
  tut, zuordenbar und manipulationserkennbar. Sie schreibt **nicht** das
  Design-Narrativ des Anbieters und unterzeichnet nicht die Konformitätserklärung.

### Codieren Sie die Daten nicht fest — liefern Sie sie aus

Die Anwendungszeitpläne für Hochrisiko sind **im Fluss** (die vorläufige
Einigung zum Digital Omnibus vom 2026-05-07 hat mehrere verschoben). Daten in eine
statische Datei zu kopieren, ist der Weg, auf dem Compliance-Dokumente veraltet und
falsch werden. Die Control Plane liefert den regulatorischen Kalender **als Daten**
aus — jeden Eintrag mit seiner Quelle und `verified_on`:

```http
GET /v1/m/compliance/calendar
```

Ihre GRC-Pipeline liest den Live-Kalender; Ihr Evidenzpaket referenziert ihn.
Niemand tippt ein Datum erneut ab.

## Packaging-Workflow

1. Pro AI-System im Geltungsbereich ziehen: Model Card (`?format=md`), AIBOM
   (`?format=spdx`), Risikoklassifizierung, Eval-Zusammenfassungen, der
   Drift-Snapshot und der Kalenderauszug.
2. **Versiegeln** Sie das Bundle als Compliance-Evidenzpaket — append-only,
   ledger-verankert:
   `POST /v1/m/compliance/frameworks/eu_ai_act/evidence` →
   `GET /v1/m/compliance/evidence/{id}/export?format=oscal`.
3. Hängen Sie die vom Anbieter verfassten Abschnitte an (Design-Entscheidungen, das
   Art.-9-Narrativ, die Art.-47-Erklärung). Die Plattform fabriziert nicht, was nur
   der Anbieter weiß.

Das Ergebnis ist eine Annex-IV-Akte, deren operative Abschnitte **aus dem Ledger
reproduzierbar** und **off-box re-verifizierbar** sind — eine Eigenschaft, die ein
von Hand kuratiertes Dokument nicht bieten kann.

## Warum Souveränität der entscheidende Faktor für regulierte EU-Käufer ist

Für eine Bank, ein Krankenhaus, ein Ministerium oder eine Universität unter
EU-Aufsicht ist *wo die Evidenz lebt* kein Detail — es ist oft das Gate.

- **Die Data Plane verlässt niemals Ihre Grenze.** Die Collectors laufen auf
  **Ihrer** Infrastruktur; die Access Map speichert nur die *Relation* (Agent →
  Ressource, Lesen oder Schreiben) mit einer Quelle und einem Konfidenzgrad —
  **keine Payloads, keine Secrets, keine PII**. Die Compliance-Evidenz wird aus
  Daten gebaut, die niemals die Cloud eines Anbieters durchqueren mussten.
- **Die Control Plane kann vollständig self-hosted oder air-gapped** mit null
  Egress und einer Offline-Lizenz betrieben werden. Es gibt keinen Anbieter in
  Ihrem Compliance-Pfad, der als Unterauftragsverarbeiter hinzuzufügen, unter einem
  Übermittlungsmechanismus zu bewerten oder für die Aufbewahrung *Ihrer*
  regulatorischen Evidenz darauf angewiesen ist.
- **AGPL-3.0, source-available.** Ihr Security-Team kann jede Zeile lesen, die die
  Evidenz produziert. Der Integritätsnachweis ist **off-box** mit `audit verify`
  verifizierbar, sodass Sie nicht unserer Behauptung vertrauen, dass das Ledger
  intakt ist — Sie prüfen es. Single-Vendor-Abhängigkeit wird strukturell
  gemildert, nicht versprochen (siehe die Vendor-Viability-Notiz im Trust-Paket).
- **Residenz wird attestiert, nicht angenommen.**
  `GET /v1/m/compliance/residency` erzeugt eine Residenz-Attestation;
  Multi-Region-Deployments sind region-scoped und deny-closed by design.

Eine **US-SaaS-Control-Plane** kehrt all dies um: Die Verhaltensevidenz Ihres
AI-Estates — genau der Nachweis, den ein EU-Regulierer verlangen könnte — wird in
der Cloud eines Dritten generiert, verarbeitet und aufbewahrt, unter einem
Shared-Responsibility-Modell, das Sie nicht kontrollieren, häufig außerhalb der EU.
Das ist genau die Konstellation, die vielen regulierten EU-Käufern gesagt wird,
dass sie sie nicht eingehen dürfen. **Self-hosted ist hier keine Deployment-
Präferenz; es ist die Compliance-Haltung.**

:::caution[Wir entwerfen für Audits; wir zertifizieren nicht]
Nichts vom Obigen macht Sie oder uns „EU-AI-Act-konform“ — Compliance ist eine
rechtliche Schlussfolgerung über ein konkretes System, gezogen von dessen Anbieter
mit Rechtsbeistand. Was die Control Plane Ihnen gibt, ist **Evidenz, hinter der Sie
stehen können**, generiert aus echten Laufzeitdaten, gehalten dort, wo Ihr
Regulierer sie erwartet. Der [Framework-Katalog](/de/reference/modules/xiii-compliance/)
trägt den „keine Zertifizierung“-Disclaimer auf jedem Eintrag, by design.
:::

## Verwandt

- [Maschinenlesbare Evidenz](/de/reference/modules/xiii-compliance/) — die
  Evidenz-API-Oberfläche, KSI-artige kontinuierliche Validierung.
- [Sicherheitsmodell](/de/explanation/security/security-model/) — warum das Ledger
  manipulationserkennbar ist und wie die Off-Box-Verifikation funktioniert.
- [Marktkontext & Quellen](/de/explanation/positioning/market-context-and-sources/)
  — die verifizierten Statistiken hinter dem Governance-Debt-Argument.
