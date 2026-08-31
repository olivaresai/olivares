---
title: "Modul XIV — interner Katalog & Marketplace"
description: >-
  Die interne, kuratierte Registry der für die Organisation freigegebenen Agenten,
  MCP-Server, Skills, Templates, Modelle und Drittanbieter-Connectoren. Wie ein
  Eintrag versioniert, eingefroren, hash-pinned und bei Freigabe signiert wird, wie
  die Self-Service-Instanziierung gesteuert wird und die Grenzen.
---

Modul XIV ist der **interne Katalog** der Organisation — eine kuratierte, gesteuerte Registry
der Agenten, MCP-Server, Skills, Templates, Modelle und Drittanbieter-Connectoren, die
**zur Wiederverwendung freigegeben** wurden, unternehmensweit. Es existiert, damit ein Estate sich
auf geprüfte, versionierte Capabilities standardisiert statt auf Ad-hoc-Kopien, und damit
„freigegeben" etwas Verifizierbares bedeutet statt eines Worts in einem Wiki. Es sitzt in der
Intelligence-Schicht und hat **keine Aktuierungsfläche**: es kuratiert und zeichnet auf, während
das Provisioning anderswo geschieht.

## Was es ist

Der Katalog ist eine **Registry**, kein Dokumentenspeicher. Ein **Eintrag** ist eine kuratierte,
versionierte Definition einer wiederverwendbaren Capability, der Art `agent`, `mcp`, `skill`,
`template`, `model` oder `connector`. Jede `(kind, slug, version)` ist ihr **eigenes unveränderliches
Artefakt** — das Veröffentlichen einer neuen Version erzeugt einen neuen Eintrag, und Freigabe und
Signatur geschehen **pro Version**. Ein Eintrag durchläuft einen festen Lebenszyklus:

`draft → pending → approved → deprecated`

Nur ein **draft** ist veränderbar; **die Freigabe friert ihn ein**. Die Spec eines Eintrags ist
eine betreiberverfasste *Definition* — Transport-, Modell- und Prompt-Referenzen, Scope und
**Referenzen** auf Secrets — niemals ein Credential-Wert. Der Create-/Approve-Pfad weist eine Spec
ab, die Inline-Credentials trägt, sodass das Modul Definitionen, Referenzen und Governance-Metadaten
speichert und niemals Secrets oder Payloads.

## Versionierung, Einfrieren und Signieren

Die Freigabe ist der Punkt, an dem „freigegeben" verifizierbar wird:

- **Content-Hash.** Bei der Freigabe wird der Eintrag durch einen **SHA-256-Content-Hash** über
  sein kanonisches, deterministisch serialisiertes Preimage gepinnt. Jedes betreiberverfasste Feld
  ist abgedeckt, sodass jede spätere Mutation eines freigegebenen Eintrags **erkennbar** ist —
  manipulationserkennbar auch ohne Signatur.
- **Ledger-Attestierung.** Die Freigabe wird im append-only, hash-chained Audit-Ledger
  aufgezeichnet, zugeordnet zum **echten Principal**, der sie freigegeben hat.
- **Ed25519-Signatur.** Wenn ein Katalog-Signaturschlüssel bereitgestellt ist, produziert die
  Freigabe zudem eine **abgetrennte Ed25519-Signatur** über den Content-Hash, mit dem öffentlichen
  Schlüssel und einem kurzen Fingerprint — „freigegeben = verifizierbar". Der Signaturschlüssel wird
  beim Boot unter der fail-closed Key-Naht der Engine geladen oder geprägt, **unabhängig von** dem
  Audit-Ledger-Schlüssel; das Modul besitzt seinen eigenen Katalog-Schlüssel und greift niemals auf
  den internen Audit-Signierer der Engine zu, was die Vertrauensgrenze sauber hält.

Die Verifikation berechnet den Hash neu und behandelt, wenn am Knoten ein Schlüssel konfiguriert ist,
die Signatur als den **Trust-Anchor**: eine entfernte Signatur (Downgrade) oder eine von einem
beliebigen anderen Schlüssel erstellte (Substitution) wird als **not verified** gemeldet. `GET …/pubkey`
meldet, ob Signieren aktiviert ist; der `verified`- / `signed`- / `signed_by`-Status pro Eintrag wird
von den Entry- und Verify-Routen zurückgegeben.

## Verifizierte Drittanbieter-Connectoren

Ein `connector`-Eintrag kuratiert ein **freigegebenes Drittanbieter-Connector-Plugin** — ein
gebautes Binary oder OCI-Artefakt. Seine Spec zeichnet auf, was es kuratiert: den `sha256` des
Artefakts (`artifact_digest`), die Release-/OCI-Referenz, den Publisher und den Descriptor-Namen
des Connectors. Der Eintrag ist der mandantenseitige **Zertifizierungsdatensatz** des
External-Connector-Ökosystems: „freigegeben" kann so gestaltet werden, dass es bedeutet „seine
Supply-Chain-Attestierung wurde verifiziert", nicht nur „jemand hat auf Freigeben geklickt".

Der Ablauf spiegelt das MCP-Eintrag-Admission-Paar, mit eigenen Policy- und Verdict-Datensätzen
(Nachweise werden pro Art gezählt, sodass Connector-Verdicts niemals Tabellen mit MCP-Verdicts
teilen):

- `GET`/`PUT …/connector-admission/policy` — der Trust-Root pro Mandant:
  `require_signed`, optionales `require_subject_digest`, Sigstore-Identitäts-/Issuer-Pins,
  bare öffentliche Schlüssel, CA-Roots und die in-toto-**Predicate-Allow-List**
  (Default ist SLSA-Provenance v1/v0.2 und SPDX/CycloneDX-SBOMs — Provenance- und
  SBOM-Formen, weil ein Connector ein gebautes Artefakt ist, keine Modellgewichte). Keine
  Policy bedeutet **Observe-Modus** — nichts wird gegated, bis der Mandant sich aktiv dafür
  entscheidet, und der Policy-Endpunkt sagt das ehrlich.
- `POST …/entries/{id}/admit` — eine gemeinsame Route, dispatched nach Eintragsart
  (`mcp` oder `connector`): verifiziert ein betreiberbereitgestelltes Sigstore-Attestierungs-Bundle
  und zeichnet ein **claim-vs-verified-Verdict** pro Eintrag auf. Wenn die Anfrage keinen
  `expected_digest` pinnt, **defaultet die Bindung auf den `spec.artifact_digest` des Eintrags** —
  der Eintrag benennt das Artefakt, das er kuratiert, sodass die Admission an dieses Artefakt bindet,
  sofern nicht explizit überschrieben. Ein fehlerhaftes Bundle ist ein `400`; ein wohlgeformtes
  Bundle, das nicht verifiziert, ist ein **aufgezeichnetes negatives Verdict**, kein Fehler.
- `GET …/connector-admissions` — die aufgezeichneten Verdicts, filterbar nach Eintrag
  (`entry_ref`) und einschränkbar auf verifizierte Verdicts (`verified=true`).
- **Deny-closed Approve-Gate.** Mit aktiviertem `require_signed` kann ein Connector-Eintrag nur
  freigegeben (und damit als *verifizierter* Connector gelistet) werden mit einem verifizierten
  Provenance-/SBOM-Admission-Verdict, **gebunden an den Digest, den der Eintrag derzeit kuratiert**
  (`spec.artifact_digest`); mit aktiviertem `require_subject_digest` muss diese Artefakt-Bindung
  selbst bestätigt sein. Das Bearbeiten des kuratierten Digests nach einer Admission invalidiert das
  Gate — eine Re-Admission gegen das neue Artefakt ist erforderlich.

:::caution[Ehrliche Grenzen]
Der Katalog **zertifiziert**, er führt nicht aus: das host-seitige Gate, das entscheidet, ob ein
Connector-Plugin tatsächlich *laufen* darf, lebt im Control Plane, nicht hier. Attestierungs-Bundles
sind betreiberbereitgestellt (`cosign download attestation` /
`gh attestation download`) — sie von OCI-Referrers zu holen ist ein externer Schritt, und die
Rekor-Transparency-Log-**Inclusion** wird nicht nativ verifiziert (das Verdict zeichnet die Präsenz
des Materials auf und sagt genau, was geprüft wurde).
:::

## Gesteuerte Self-Service-Instanziierung

Eine **Instance** ist eine Self-Service-Anfrage, einen **freigegebenen** Eintrag zu instanziieren —
nur ein freigegebener Eintrag kann instanziiert werden. Das Modul zeichnet die Anfrage, ihre
**Provenance** (von welcher Eintragsversion sie stammt), ihr Ziel und ihren Governance-Status auf und
erzwingt eine sinnvolle State-Machine (`requested → approved`/`rejected → active`). Es entscheidet
**nicht**, wer genehmigen darf, noch provisioniert es: die Genehmigungs-**Entscheidung** gehört zur
Governance und das eigentliche Provisioning zum Deployment. Freigeben, Deprecaten, Signieren und
Instanziieren sind **privilegiert, RBAC-gegated und selbst-auditiert** zum echten Principal.

:::caution[Ehrliche Grenzen]
- **Keine Aktuierung, kein Provisioning.** Modul XIV zeichnet die *Anfrage* auf und steuert sie; es
  stellt niemals eine Capability auf. Die Genehmigungsentscheidung gehört der Governance und die
  Verdrahtung dem Deployment — und ein Live-`apply`/`retire` dort ist selbst eine deny-closed Naht
  (`503`, bis ein Executor bereitgestellt ist). Siehe [Ehrlichkeit & Grenzen](/de/start/honesty-and-limits/).
- **Signieren ist real, aber schlüsselabhängig.** Ed25519-Signieren ist implementiert und der
  Signaturschlüssel wird beim Boot standardmäßig bereitgestellt. Auf einem Knoten mit **keinem
  konfigurierten Schlüssel** (oder einem ungültigen Schlüssel) ist ein freigegebener Eintrag
  **hash-pinned und ledger-attestiert, aber unsigniert** — die API sagt das ehrlich über
  `signing_enabled`/`signed`, statt eine vorhandene Signatur zu implizieren.
- **Kuratiert, nicht beobachtet.** Der Katalog **abonniert** den Event-Bus nicht und gibt nicht auf
  ihm aus; er wird von Menschen über seine API befüllt, nicht aus Live-Beobachtungen abgeleitet. Er
  behauptet, was die Organisation *zur Wiederverwendung freigegeben* hat, nicht was derzeit läuft.
- **Das Modul erzwingt die Genehmigungs-Policy nicht.** Es erzwingt die State-Machine und die
  RBAC-Verb-Stufen; *wer* unter welchen Bedingungen genehmigen darf, wird von der Governance
  entschieden.
:::

## Verwandt

- [Modulkatalog](/de/reference/modules/overview/) — wo Modul XIV sitzt und die Trennung von
  Govern/Observe vs. Actuate.
- [Steuern und genehmigen](/de/how-to/govern-and-approve/) — der Human-in-the-Loop-Genehmigungsablauf.
- [Architekturüberblick](/de/explanation/architecture/overview/) — die Engine, die Schichten und das
  gemeinsame Datenmodell, in das Einträge deklarieren.
- [Event-Bus-Referenz](/de/reference/events/) — der Bus, den dieses Modul bewusst nicht konsumiert.
- [Ehrlichkeit & Grenzen](/de/start/honesty-and-limits/) — die Aktuierungshaltung über das Produkt hinweg.
