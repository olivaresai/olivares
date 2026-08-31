---
title: "Modul XXIII — Modelloperationen"
description: >-
  Das governte Register der Modelle, die Ihnen GEHÖREN — gehostet,
  feinabgestimmt oder importiert — mit Zulassung signierter Modelle und lokalen
  Inferenz-Deployments. Es governt die Lieferkette Ihrer eigenen Modelle; es
  trainiert, serviert oder benchmarked sie nicht.
---

Modul XXIII ist die Seite der **eigenen Modelle** im Modell-Stack. Während
Modul X (Modelle & Anbieter) den *Referenzkatalog* und das *Routing* der von
Ihnen konsumierten Modelle governt, governt dieses Modul die Modelle, die Sie
**besitzen und betreiben**: ein versioniertes Register, das
**Zulassungs-Gate** für signierte Modelle, das entscheidet, welche Versionen
deployed werden dürfen, sowie die lokalen **Inferenz-Deployments**, die sie
bereitstellen. Es verfolgt und governt; es trainiert niemals ein Modell, führt
keinen Fine-Tune-Job aus und führt selbst keine Inferenz aus.

Die Konsolenoberfläche dieses Moduls heißt **Model Operations** (Gruppe
Intelligence) und enthält Tabs für eigene Modelle, Datasets, Fine-Tune-Jobs,
Zulassung, Deployments und das Ledger der AIBOM-Seals. Die GPAI-Posture von
Anbietern (pro Anbieter) befindet sich unter **Models & providers**, und die
Agenten-Lieferkette besitzt eine eigene Ansicht — beide betreffen einzelne
Anbieter bzw. das Estate und nicht einzelne eigene Modelle.

## Was es ist

Drei zusammenarbeitende Oberflächen, alle deny-closed und auditiert:

- **Eigene Modelle & Versionen.** Ein Register der Modelle, die Ihnen gehören
  (`hosted`, `fine_tuned`, `imported`), jeweils mit unveränderlichen
  **Versionen**, die ein Artefakt benennen. Eine Version wird aufgezeichnet,
  danach wird ihr signiertes Artefakt zugelassen — die Versionszeile selbst
  ändert sich nie.
- **Zulassung.** Eine **Trust-Policy** pro Mandant und die aufgezeichnete
  Historie der **Verdicts**. Die Policy benennt die Trust Anchors — CA-Roots
  und/oder öffentliche Schlüssel sowie optional Sigstore-Identitäten und
  -Aussteller — und die Signatur-**Methode wird aus Ihrer Konfiguration
  abgeleitet** (`sigstore-keyless`, `certificate-pki` oder `bare-key`); eine
  leere Policy lässt nichts zu. Bei der Zulassung einer Version wird ein
  Signatur-**Bundle** gegen die Policy geprüft und der Verdict aufgezeichnet.
  Ein Verdict mit fehlgeschlagener Verifikation wird ehrlich aufgezeichnet,
  nicht verborgen.
- **Deployments.** Lokale Inferenz-Deployments (vLLM, Ollama, llama.cpp,
  weitere). Wenn der Mandant signierte Modelle **erzwingt**, prüft das
  Erstellen oder Aktualisieren eines Deployments, das auf eine Version
  verweist, die Zulassung erneut: Besitzt die Version keinen verifizierten
  Verdict oder befindet sich der Trust Root, der sie zugelassen hat, nicht
  mehr in der Policy, wird das Deployment abgelehnt.

## Lineage & Evidence

- **Datasets.** Minimal-Data-Lineage-Komponenten — ein Name, eine optionale
  Inhaltsreferenz und ein Hash, eine Klassifizierung und ein Governance-Label
  — **niemals die Dataset-Inhalte**. Ein Dataset gilt mandantenweit; seine
  optionale Modellreferenz ist ein Lineage-Zeiger, der deny-closed validiert
  wird. `verified` ist eine **Angabe des Operators** zur Herkunft, niemals ein
  kryptografisches Ergebnis, und die Konsole kennzeichnet sie entsprechend.
- **Fine-Tune-Jobs.** Aufzeichnungen extern ausgeführter
  Feinabstimmungsarbeiten und der Modell-**Version**, die jeweils daraus
  hervorging. Die Kontrollebene startet, storniert oder führt niemals Training
  aus und speichert weder Gewichte noch Dataset-Inhalte — dies sind
  Inventareinträge, kein Trainings-Launcher.
- **AIBOM & Model Card.** Von einem eigenen Modell können Sie eine aktuelle
  CycloneDX-AIBOM (oder eine SPDX-3.0.1-Serialisierung) und eine Model Card
  (JSON oder Markdown) **generieren**; alle sind read-only. Ein generiertes
  Dokument ist erst Evidence, wenn Sie es **versiegeln**: Das Versiegeln
  verankert ein kanonisches Content-Hash-Commitment im Audit-Ledger (immer
  CycloneDX — SPDX kann niemals versiegelt werden). Das Ledger speichert nur
  den Hash, daher ist der Seal-Beleg die einzige Gelegenheit, das versiegelte
  Dokument zu speichern. Der modellübergreifende Tab **AIBOM seals** ist das
  dauerhafte Append-only-Ledger dieser Commitments.

## Was es erzwingt

Wenn `require_signed` aktiviert ist, wird ein Deployment, das auf eine
Modellversion verweist, **nur dann** zugelassen, wenn diese Version einen
verifizierten Zulassungs-Verdict besitzt und dessen verankernder Trust Root
weiterhin konfiguriert ist. Wird ein Root aus der Policy rotiert, wirkt die
Änderung rückwirkend auf vorhandene Verdicts: Künftige
Erstellungen/Aktualisierungen von Deployments für Versionen, die nur dieser
Root zugelassen hat, werden abgelehnt — sie müssen zuerst unter den aktuellen
Anchors **erneut zugelassen** werden. Dies ist derselbe Anchor-Pin,
den die Engine in jedem Verdict (`signer_roots`) aufzeichnet und offenlegt,
damit ein Operator genau erkennen kann, welcher Root für eine Version
eingestanden ist.

## Was es nicht ist

- Es führt **keine** Trainings- oder Fine-Tune-Jobs aus — es zeichnet ihren
  Status für die Lineage auf.
- Es serviert **keine** Inferenz — es governt die Deployment-Einträge, die das
  tun.
- Es entscheidet nicht anhand eines gespeicherten Verdicts, ob etwas
  **„derzeit deploybar“** ist — nur die erneute Prüfung der Engine zum
  Deployment-Zeitpunkt ist maßgeblich. Deshalb kennzeichnet die Konsole eine
  Version niemals allein aufgrund der Historie als vertrauenswürdig oder
  deploybar.

## Agenten-Lieferkette

Die separate Konsolenansicht **Agent Artifacts** registriert vier
Artefaktklassen im Estate eines Mandanten: Agent Skills, `.mcpb`-Erweiterungen,
MCP-App-`ui://`-Templates und `AGENTS.md`-Anweisungsdateien. Das Register
speichert Identität, Herkunft, Content-Fingerprints und Posture-Metadaten —
niemals Skill-Bodies, Manifeste oder Anweisungstext. Eine Posture-Bewertung ist
ein **aufgezeichnetes Scan-Ergebnis** eines Connector-Scanners oder Operators,
kein Scan, den die Konsole ausführt; eine fehlende Bewertung wird neutral als
nicht gescannt angezeigt.

Die CycloneDX-1.6-BOM der Agenten-Lieferkette unterscheidet sich von der
Lineage-AIBOM eines einzelnen Modells. Seals fügen dem separaten Ledger
`models.agent_aibom` ein kanonisches Content-Hash-Commitment hinzu, während der
zurückgegebene Beleg die einzige Kopie des versiegelten Dokuments bleibt. Die
Abdeckung umfasst nur registrierte Artefakte: Ein nie registriertes Artefakt
ist nicht enthalten.
