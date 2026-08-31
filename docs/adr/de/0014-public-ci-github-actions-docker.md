> Maschinell übersetzt. Maßgeblich ist die englische Fassung.

# ADR-0014: Öffentliche Veröffentlichung & CI über GitHub Actions + Docker

- **Status:** accepted
- **Date:** 2026-06-04
- **Deciders:** Fran Olivares (boot decision)
- **References:** roadmap boot decisions (Release/CI)

## Kontext und Problemstellung

Die Entwicklung findet in einem privaten Repository statt; die öffentliche, verifizierbare
Lieferkette benötigt eine breit vertraute, transparente CI-/Release-Oberfläche (für
schlüssellose Signaturidentitäten, SLSA-Provenance und die öffentliche Verteilung von Artefakten).

## Entscheidungstreiber

- Eine öffentliche, verifizierbare Release-Identität (OIDC) und ein Transparency-Log für das Signieren.
- Standardisierte, breit vertraute Container-Verteilung.
- Die alltägliche Entwicklung privat halten, bis ein Release kuratiert und veröffentlicht ist.

## Betrachtete Optionen

- **GitHub Actions + Docker für alle öffentlichen Artefakte; ein privates Entwicklungs-Repository.**
- **Self-hosted CI auch für öffentliche Releases.**

## Entscheidungsergebnis

Gewählte Option: **GitHub Actions + Docker für alles Öffentliche, immer**; **die Entwicklung
findet in einem privaten Repository statt**. Die GitHub-Actions-OIDC-Identität des Release-Workflows
ist das, was schlüssellose Signaturen und SLSA-Provenance bezeugen, und Images/Charts werden in
eine öffentliche OCI-Registry veröffentlicht.

### Konsequenzen

- **Gut:** Signaturen und Provenance verketten sich zu einer öffentlichen, verifizierbaren Identität;
  standardisierte Verteilung; die Entwicklung bleibt privat, bis sie absichtlich veröffentlicht wird.
- **Schlecht / Kompromisse:** Das öffentliche Repository ist ein kuratierter Export des privaten
  Entwicklungs-Repositorys, kein Live-Spiegel.
- **Neutral:** Das Veröffentlichen des öffentlichen Repositorys ist eine bewusste, kontrollierte Aktion.

## Warum die Alternativen abgelehnt wurden

- **Self-hosted CI für öffentliche Releases** — eine self-hosted Signaturidentität ist für Dritte
  weitaus schwerer zu verifizieren als eine öffentliche GitHub-Actions-OIDC-Identität mit einem
  Transparency-Log.
