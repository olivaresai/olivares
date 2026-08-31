> Maschinell übersetzt. Maßgeblich ist die englische Fassung.

# ADR-0015: Lieferkette — signierte Releases, SBOM, SLSA-Provenance, OpenVEX, distroless

- **Status:** accepted
- **Date:** 2026-06
- **Deciders:** Fran Olivares
- **References:** stack decisions (T6/T7); supply-chain & release-verification design

## Kontext und Problemstellung

Für ein Sicherheitsprodukt ist ein unsigniertes oder nicht verifizierbares Release inakzeptabel.
Käufer müssen verifizieren können, *was sie heruntergeladen haben* — einschließlich vollständig
offline, in air-gapped (netzgetrennten) Umgebungen — und die Provenance sowie den Status bekannter
Schwachstellen jedes Artefakts kennen.

## Entscheidungstreiber

- Kryptografische Verifizierbarkeit jedes Artefakts, online und offline.
- Provenance (wer hat es gebaut, aus welcher Quelle) und eine Software-Stückliste (SBOM).
- Ein minimales, per Digest gepinntes Laufzeit-Image.

## Betrachtete Optionen

- **cosign/sigstore-Signaturen + SBOM (syft) + SLSA Build L3 (SLSA v1.2) Provenance + OpenVEX +
  distroless-Images, per Digest gepinnt**, mit einem Offline-Verifikationspfad und einem
  Air-Gap-Bundle.
- **Nur Prüfsummen / unsignierte Releases.**

## Entscheidungsergebnis

Gewählte Option: das **vollständige Lieferketten-Set**. Releases liefern cosign-Signaturen,
SPDX- und CycloneDX-SBOMs, SLSA Build L3 Provenance und OpenVEX-Attestationen; Container-Images sind
**distroless, per Digest gepinnt**. Ein Verifikationsskript prüft die gesamte Kette, einschließlich
eines **vollständig offline** Modus, und ein **Air-Gap-Bundle** führt einen öffentlichen Schlüssel
mit sich, sodass ein getrennter Standort alles ohne ein Transparency-Log verifizieren kann.

### Konsequenzen

- **Gut:** Jedes Artefakt ist verifizierbar, online oder offline; Provenance und ein SBOM werden
  mit jedem Release ausgeliefert; das Laufzeit-Image ist minimal und unveränderlich (per Digest).
- **Schlecht / Kompromisse:** mehr zu wartende Release-Maschinerie; das Air-Gap-Bundle erfordert,
  dass SBOM/VEX/Provenance an den Bundler übergeben werden.
- **Neutral:** Das Deployment erfolgt immer per Digest, niemals per Tag.

## Warum die Alternativen abgelehnt wurden

- **Nur Prüfsummen / unsigniert** — bietet keine Provenance, keinen Offline-Vertrauensanker und
  keine Schwachstellen-Aussage; in einem Sicherheitsprodukt inakzeptabel.

## Addendum (2026-07-03): Formulierung zu SLSA v1.2 + Bewertung des Source-Tracks

Die SLSA-Formulierung wird auf **SLSA Build L3 (SLSA v1.2)** vereinheitlicht. In SLSA v1.2
endet der Build-Track bei L3, daher beansprucht dieser ADR nur diese Stufe des Build-Tracks.

Die Bewertung des Source-Tracks bleibt davon getrennt. Source L1–L3 würde aufbewahrte
Source-Revisionen sowie Provenance-Attestierungen aus dem Source-Control-System erfordern;
Source L3 setzt zusätzlich eine kontinuierliche, manipulationsnachweisende Durchsetzung
voraus, etwa durch gittuf oder Plattform-Attestierungen.

Aktueller Stand: Branch-Protection ist in `scripts/apply-branch-protection.sh` geskriptet,
aber Source-Provenance-Attestierungen sind nicht bereitgestellt.

Entscheidung: Es wird keine Source-Track-Stufe beansprucht; den Source-Track beobachten und
zum öffentlichen Launch erneut prüfen.
