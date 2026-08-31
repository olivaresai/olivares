> Maschinell übersetzt. Maßgeblich ist die englische Fassung.

# ADR-0016: Ökosystem externer Connectors — öffentliches SDK, signierte Admission, Verteilung über Releases/OCI, kuratierter verifizierter Index

- **Status:** accepted
- **Date:** 2026-06-11
- **Deciders:** Fran Olivares (v1 scope decided 2026-06-09)
- **References:** `LICENSING.md` (license boundary), ADR-0007 (go-plugin runtime),
  ADR-0011 (AGPL/Apache/commercial), ADR-0015 (supply chain),
  `docs/contracts/S02-sdk-runtime-eventbus.md`,
  `docs/contracts/S142-external-connector-sdk.md`

## Kontext und Problemstellung

Das Connector-SDK (`sdk`, `sdk/plugin`) wurde von Tag eins an so entworfen, dass ein
Connector niemals die AGPL-Engine einbindet (Apache-2.0, keine Abhängigkeiten, gRPC-
Plugin-Transport — ADR-0007, ADR-0011), und ADR-0007 hat ausdrücklich vorweggenommen,
dass „Dritte Connectors unabhängig ausliefern können". Aber es existierte kein Mechanismus:
Die SDK-Module sind ohne Tags und werden ausschließlich über den Monorepo-Workspace
konsumiert, die Composition Root startet nur **eingebettete First-Party**-Plugin-
Binaries (`go:embed`), `LoadSourcePlugin` führt jeden ihm übergebenen Pfad **ohne Integritäts-
oder Provenance-Prüfung** aus, und der Katalog von Modul XIV kuratiert nur interne Einträge.
„Kann mein Team oder ein Partner einen Connector bauen und veröffentlichen?" hatte keine Antwort.

Das Öffnen des Ökosystems kann nicht bedeuten „der Host lädt jede `.so`-artige Binary, auf die ein
Operator zeigt": Dies ist ein Sicherheitsprodukt; eine unsignierte, nicht attestierte ausführbare
Datei, die in die Beobachtungsebene verdrahtet wird, wäre ein Lieferketten-Loch.

## Entscheidungstreiber

- Der Amplituden-Burggraben **komponiert** nur dann, wenn Dritte sicher Connectors
  beitragen können (`ARCHITECTURE.md`, `LICENSING.md`).
- Die Lizenzgrenze (Connector = Apache, importiert niemals `/core`) muss **vom Dritten**
  verifizierbar sein, nicht nur in unserer CI.
- Signatur- + Admission-Maschinerie existiert bereits und ist bewährt (Model-
  Admission, MCP-Entry-Admission, `core/secure/modelsign`): wiederverwenden, niemals
  neu implementieren.
- Keine gehostete Marketplace-Infrastruktur in v1 (kommerzielle Entscheidung aufgeschoben).

## Betrachtete Optionen

- **Option A — gehosteter Marketplace-Service**: ein von Olivares.AI betriebener
  Registry-Service mit Upload/Review/Serve.
- **Option B — SDK + Zertifizierung + Signierung, Verteilung über GitHub-
  Releases/OCI, kuratierter statischer „verified connectors"-Index in der Docs-Site;
  deny-closed signierte Admission am Host.**
- **Option C — offenes Plugin-Laden** (vom Operator gelieferter Pfad, keine Signatur),
  Zertifizierung nur als Dokumentation.

## Entscheidungsergebnis

Gewählte Option: **Option B** (entschieden 2026-06-09).

1. **Öffentlicher SDK-Vertrag.** `sdk` und `sdk/plugin` werden für Connector-Autoren als
   **stable v1** deklariert, mit einer expliziten Versionierungs-/Deprecation-Policy
   (`sdk/VERSIONING.md`, ausgewiesen auf der Stability-Seite der Docs-Site). Semver-
   Tags (`sdk/v1.*`, `sdk/plugin/v1.*`) landen mit dem ersten öffentlichen Release des
   Repositorys; bis dahin pinnen Autoren einen Commit (das `-sdk-path` des Scaffolds
   deckt die Entwicklungsschleife ab).
2. **Scaffold + Leitfaden.** Ein abhängigkeitsfreier Generator
   (`sdk/scaffold`, CLI `olivares-connector-new`) erzeugt ein vollständiges out-of-tree
   Connector-Repository — vertragskorrektes Source-/Output-Skelett, Lifecycle-
   Test, Plugin-`main`, README und eine **eigenständige Boundary-Prüfung** (dieselbe
   `go list -deps`-Regel, die `scripts/check-boundary.sh` in unserer CI durchsetzt, sodass
   der Dritte die AGPL/Apache-Grenze in *seiner* CI verifiziert).
3. **Verteilungskanal.** Ein veröffentlichter Connector wird als **GitHub-Release-
   Asset** (Binary + `sha256` + Sigstore-Attestation-Bundle) und/oder als **OCI-
   Artefakt** (ORAS, Attestation als Referrer) ausgeliefert. Kein gehosteter Marketplace in v1.
4. **Signierte Admission, deny-closed am Host.** Ein externes Plugin läuft nur dann,
   wenn die Sources-Konfiguration des Operators dessen Digest pinnt UND eine Sigstore/DSSE-
   Lieferketten-Attestation (SLSA-Provenance / SBOM-Prädikat) über diesem Digest
   gegen eine vom Operator konfigurierte Trust-Policy verifiziert
   (`connector_trust`), unter Wiederverwendung von `modelsign.VerifyAttestation`. Der
   Loader pinnt zusätzlich die Prüfsumme zur Exec-Zeit (go-plugin
   `SecureConfig`). **Es gibt keinen Observe-Modus und keine Allow-unsigned-Ausweichmöglichkeit
   für externe Binaries** — die Entwicklungsschleife lautet „signiere mit deinem eigenen
   Schlüssel, vertraue deinem eigenen öffentlichen Schlüssel" (Bare-Key-Modus).
5. **Zertifizierungseintrag (Katalog-Overlay).** Modul XIV erhält eine `connector`-
   Eintragsart mit ihrem eigenen Admission-Paar
   (`catalog.connector_admission_policy` / `catalog.connector_admission`):
   verifizierte Provenance-/SBOM-Verdikte pro Eintrag, deny-closed Approve-Gate,
   Observe-Modus als Standard — der mandantenseitige Zertifizierungsnachweis, entkoppelt
   vom Host-Exec-Gate (Defense in Depth, wie das Admit-Route- + Deployment-Gate-Paar
   der Model-Admission).
6. **Verified-Connectors-Index.** Eine **kuratierte statische Seite** in der Docs-Site
   (`reference/verified-connectors`) listet Drittanbieter-Connectors auf, deren
   Release die Maintainer erneut verifiziert haben (Boundary, Signatur, Provenance,
   Minimal-Data-Review). Die Listung erfolgt per Pull Request; der Index ist
   Dokumentation der durchgeführten Verifikation, **kein** Vertrauensanker — Operatoren
   pinnen weiterhin die Identität/den Schlüssel des Publishers in `connector_trust`.

### Konsequenzen

- **Gut:** Dritte bauen, signieren und liefern Connectors aus, ohne die
  AGPL-Engine zu berühren; der Host führt niemals nicht attestierten Code aus; die Zertifizierung
  verwendet bewährte Maschinerie wieder; null neue zu betreibende Services.
- **Schlecht / Kompromisse:** keine Discovery-/Install-UX über Docs + Releases hinaus (ein
  gehosteter Marketplace würde eine bieten); Operatoren verwalten Trust-Anker von Hand;
  externe **Output**-Connectors werden auf dieselbe Weise gebaut und ausgeliefert, aber die
  hostseitige externe Verdrahtung deckt zuerst Beobachtungsquellen ab (die Notify-Composition hat
  noch keinen externen Plugin-Pfad).
- **Neutral / Follow-ups:** OCI-*Pull* durch den Host (heute legt der Operator die
  Binary auf die Platte; der Digest-Pin macht den Transport für das Vertrauen irrelevant);
  out-of-process-Module bleiben unverdrahtet; eine Compliance-Fähigkeit, abgeleitet
  aus Connector-Admissions; npm-Scope `@olivaresai` und Module-Proxy-Tags beim
  öffentlichen Export.

## Warum die Alternativen abgelehnt wurden

- **Option A** — den Betrieb eines Marketplace ist eine kommerzielle Verpflichtung, die
  ausdrücklich aufgeschoben wurde; er fügt einen vertrauenskritischen Service ohne
  v1-Nachfrage hinzu.
- **Option C** — „lade jede Binary" ist genau das Lieferketten-Loch, das dieses
  Produkt zu schließen existiert; Zertifizierung-als-Prosa ohne Durchsetzung wäre
  Design-für-Audit-Theater (`docs/SECURITY-HARDENING.md`).
