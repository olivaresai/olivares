---
title: Einen Connector bauen und ausliefern
description: >-
  Scaffolden, implementieren, testen, signieren und verteilen Sie einen
  Drittanbieter-Connector mit dem öffentlichen Apache-2.0 Connector-SDK — und
  verdrahten Sie ihn mit deny-closed signierter Zulassung in eine Control Plane.
---

Diese Anleitung führt Sie von null zu einem **signierten Drittanbieter-
Connector**, den ein Operator in die Control Plane verdrahten kann. Das
Connector-SDK ist Apache-2.0 und importiert nichts aus der AGPL-Engine, sodass
Ihr Connector **Ihr** Code unter **Ihrer** Lizenz ist, gebaut in **Ihrem**
Repository.

Was Sie bauen, ist ein normales Go-Programm: ein Typ, der
`sdk.SourceConnector` (sammelt Fakten, emittiert Observations),
`sdk.OutputConnector` (liefert Benachrichtigungen) oder `sdk.ContentSource`
(liefert Dokumente und ACL-Referenzen an Governed Knowledge) implementiert, paketiert als
[go-plugin](https://github.com/hashicorp/go-plugin)-Binary, das die Engine
out-of-process startet und mit dem sie über gRPC spricht (gegenseitig
authentifizierter Loopback, AutoMTLS). Lesen Sie zuerst
[Eine Quelle anbinden](/de/how-to/connect-a-source/) für das Connector-*Modell* —
observe-only, Minimal-Data, die drei Observation-Arten.

:::note[Stabilität]
Der SDK-Vertrag (`Descriptor/Open/Gather/Close`, der Wire, das Plugin-
Handshake) ist **stable v1** — siehe
[API-Stabilität](/de/reference/api-stability/) und `sdk/VERSIONING.md` im
Repository. Bis die ersten öffentlichen Semver-Tags veröffentlicht sind, bauen
Sie gegen einen Checkout des Repositorys (`-sdk-path` unten).
:::

## 1. Scaffolden

Bevorzugte High-Level-CLI:

```sh
# from the repository checkout root
go run ./cmd/olivares connector init acme.widget-audit \
  --dir ~/olivares-connector-widget \
  --module github.com/acme/olivares-connector-widget \
  --template access-edge-source \
  --plugin \
  --sdk-path "$PWD/sdk"
```

Wählen Sie einen der fünf Archetypen. Sie sind Presets für stabile SDK-Schnittstellen,
keine neuen Verträge für Autoren:

| Template | Deklarierte Schnittstellen | Verwendung |
|---|---|---|
| `content-source` | `knowledge.document` | Dokumente für Governed-Knowledge-Ingestion, einschließlich Out-of-process-Content-Sources. |
| `access-edge-source` | `observation.edge` | Fakten zu Zugriffsgraphen, Identitäten, SaaS- und Infrastrukturbeziehungen. |
| `output-sink` | `notify.sink` | Benachrichtigungs- oder Ticketing-Senken. |
| `agent-surface` | `observation.edge`, `observation.finding` | Agent-Runtime-Adapter, die Zugriffskanten und Findings melden. |
| `model-provider` | `observation.cost`, `observation.edge` | Provider-Inventar, Nutzungs- und Kosten-Observations; Model Governance bleibt in der Engine. |

Das ältere eigenständige Scaffold bleibt gültig und erzeugt dieselben stabilen
Autorenverträge:

Führen Sie dies aus einem Checkout des Repositorys aus (bis die ersten
öffentlichen SDK-Tags veröffentlicht sind, wird das Paket über den Workspace
aufgelöst, und `-sdk-path` zeigt auf das `sdk/` dieses Checkouts):

```sh
# from the repository checkout root
go run ./sdk/scaffold/cmd/olivares-connector-new \
  -dir ~/olivares-connector-widget \
  -name acme.widget-audit \
  -module github.com/acme/olivares-connector-widget \
  -kind source -plugin \
  -sdk-path "$PWD/sdk"
```

Sie erhalten ein vollständiges Repository: das Connector-Skelett, einen
Lifecycle-Test, das Plugin-`main`, eine README mit diesem gesamten Lifecycle und
`scripts/check-boundary.sh` — die **gleiche Lizenz-Boundary-Prüfung, die unsere
CI ausführt**, für Ihre. `-name` ist Ihr `Descriptor.Name`: global eindeutig,
mit Punkt getrennt, `<vendor>.<connector>`.

## 2. Implementieren

Der Vertrag in Kürze (der godoc auf `sdk.SourceConnector` ist normativ):

- **`Open`** liest die Konfiguration (deklariert in Ihren
  `Descriptor.ConfigFields`; Secrets sind *Referenzen*, mit `Secret: true`
  markiert, niemals inline eingebettet). Schlagen Sie hier fehl, nicht in
  `Gather`.
- **`Gather`** emittiert Observations an das `Sink` der Engine. Die **Engine
  besitzt das Scheduling**: eine Batch-Quelle erledigt ihre Arbeit und kehrt
  zurück; eine Streaming-Quelle blockiert, bis `ctx` abgebrochen wird. Besitzen
  Sie niemals Ihren eigenen Ticker.
- Die Zustellung ist **at-least-once**; Konsumenten dedupliziert über den
  natürlichen Schlüssel der Observation. Verfolgen Sie keinen
  Zustellungszustand.
- **Minimal-Data**: emittieren Sie Referenzen und Metadaten, niemals Payloads,
  Prompts oder Secret-Werte.
- Bei `content-source` gibt **`List`** Referenzen zurück, die sich kostengünstig
  aufzählen lassen, **`Fetch`** liefert genau einen Dokumentinhalt, und die optionale
  Schnittstelle `DeltaContentSource` ergänzt Live-Deltas sowie ACL-Aktualisierung.
  Content-Source-Plugins, die diese optionale Schnittstelle implementieren, deklarieren
  automatisch `content.delta`; Hosts rufen Delta-Methoden nur auf, wenn diese Capability
  deklariert wurde.

Führen Sie Ihre Tests aus, dann beweisen Sie die Lizenz-Boundary in Ihrer CI:

```sh
go test ./...
./scripts/check-boundary.sh   # fails if anything links github.com/olivaresai/olivares/core
```

## 3. Paketieren und signieren

Bauen Sie das Plugin-Binary, fixieren Sie seinen Digest und hängen Sie eine
Supply-Chain-Attestierung als **Sigstore-Bundle** an. Die Control Plane
verifiziert SLSA-Provenance oder SBOM-Attestierungen (SPDX-/CycloneDX-
Prädikate) — signieren Sie mit Ihrem eigenen Schlüssel (hier gezeigt) oder
keyless mit Ihrer CI-Identität:

```sh
go build -trimpath -o widget-audit ./cmd/acme-widget-audit
sha256sum widget-audit

# keyed (the dev loop: trust your own public key)
cosign generate-key-pair
cosign attest-blob --key cosign.key \
  --type slsaprovenance1 --predicate provenance.json \
  --bundle widget-audit.sigstore.json widget-audit

# keyless alternative (CI): same command with --yes and an OIDC identity,
# or GitHub artifact attestations (gh attestation download produces the bundle).
```

## 4. Verteilen

Veröffentlichen Sie ein **GitHub-Release** mit dem Binary, seinem `sha256` und
dem `.sigstore.json`-Bundle — oder pushen Sie dieselben Artefakte mit
`oras push` in eine OCI-Registry (Attestierung als Referrer). Versionieren Sie
mit Semver; deklarieren Sie in Ihrer README die `ProtocolVersion`, gegen die Sie
gebaut haben (heute v1).

## 5. Betreiben (was Ihre Benutzer tun)

Der Operator legt das Binary und das Bundle auf dem Host ab und fixiert **sowohl
den Digest als auch das Vertrauen** in der Sources-Konfiguration
(`OLIVARES_SOURCES_CONFIG`):

```json
{
  "connector_trust": {
    "trusted_keys": ["-----BEGIN PUBLIC KEY-----\n…acme's cosign.pub…\n-----END PUBLIC KEY-----\n"],
    "allowed_predicates": ["https://slsa.dev/provenance/v1"]
  },
  "sources": [
    {
      "name": "widget-prod",
      "tenant": "<tenant-id>",
      "config": { "endpoint_ref": "…" },
      "plugin": {
        "path": "/opt/olivares/plugins/widget-audit",
        "sha256": "<the released digest>",
        "bundle": "/opt/olivares/plugins/widget-audit.sigstore.json"
      }
    }
  ]
}
```

Die Zulassung ist **deny-closed, ohne Notausstieg**: keine Trust Anchors, kein
Bundle, ein Digest-Mismatch, ein nicht vertrauenswürdiger Signierer oder ein
falscher Prädikat-Typ bedeuten alle, dass die Quelle **nicht verdrahtet** wird
(der Boot sagt warum). Bei Erfolg hasht die Engine das Binary beim Exec neu
(go-plugin `SecureConfig`), sodass die verifizierten Bytes die ausgeführten
Bytes sind, und der Subprozess-Kanal ist AutoMTLS-gepinnt.

Content-Source-Plugins verwenden denselben Root `connector_trust` und dieselbe
`plugin { path, sha256, bundle }`-Form pro Source innerhalb des `documents`-
Konfigurationsblocks. Sie sind erstklassige Out-of-process-Content-Sources für die
Knowledge-Ingestion.

Ein Trust Anchor ist **obligatorisch** — `connector_trust` ohne
`trusted_roots` und ohne `trusted_keys` wird rundheraus abgelehnt. Für
**keyless** Signierung ist der Anchor die Fulcio- (oder Private-CA-)Wurzel,
daher setzt der Operator `trusted_roots` (das Wurzel-PEM, z. B. aus
`cosign initialize`) **plus** `allowed_identities` und `allowed_issuers` (beide,
zusammen — die SAN-Identität und der OIDC-Issuer, den die Signatur tragen muss);
nur `trusted_keys` wird ersetzt. Das Bare-Key-Beispiel oben ist der einfachste
Anchor.

## 6. Zertifizieren lassen (optional, aber empfohlen)

Zwei einander ergänzende Aufzeichnungen:

- **In-Product-Zertifizierung** — Ihre Benutzer kuratieren Ihren Connector als
  Katalogeintrag (Art `connector`, Modul XIV) und zeichnen ein verifiziertes
  Provenance-/SBOM-Zulassungsurteil gegen Ihren freigegebenen Digest auf
  (`POST /entries/{id}/admit`); mit aktivem `require_signed` ist die Genehmigung
  deny-closed auf diesem Urteil. Siehe
  [Modul XIV](/de/reference/modules/xiv-catalog/).
- **Der Index der verifizierten Connectors** — reichen Sie Ihren Connector zur
  Listung unter [Verifizierte Connectors](/de/reference/verified-connectors/) ein:
  die Maintainer verifizieren Ihr Release erneut (Boundary, Signatur,
  Provenance, Minimal-Data-Review) und listen es. Der Index dokumentiert die
  Verifizierung; er ist **kein** Trust Root — Operatoren pinnen weiterhin
  *Ihre* Identität/Ihren Schlüssel selbst.

## Von Grund auf governed

Die Durchsetzung liegt konstruktionsbedingt in der Engine: Connectors linken keinen
Governance-Code und können sich nicht davon abmelden. Die Engine bindet die Kontrollen an
die konfigurierte Source-Identität (`source_type`, `source_ref`), wendet Source-Scoping,
ACL-Intersection, DLP-/Retrieval-Scanning, Admission und Audit an und behandelt
`Descriptor.Surfaces` nur als beratende Metadaten — niemals als Enforcement-Input.

Private Connectors sind erstklassig. Sie können einen Connector innerhalb Ihres
Unternehmens behalten, ihn niemals veröffentlichen und niemals öffentlich listen; er bleibt
governed, wenn der Operator Binary-Digest und Trust Root pinnt. Der Index verifizierter
Connectors dokumentiert die Zertifizierung; er ist kein Trust Root.

## Ehrliche Grenzen (v1)

- Externe Verdrahtung deckt **Observation-Quellen** und **Content-Sources** ab; ein Output-Connector
  wird identisch gebaut und ausgeliefert, aber die Notify-Komposition lädt noch keine
  externen Output-Plugins.
- Out-of-process-**Module** sind nicht verfügbar (das Proto ist eingefroren, der
  Host-Glue bewusst nicht verdrahtet).
- Der Observation-Summentyp ist **versiegelt**: Sie emittieren Edges,
  Cost-Samples und Findings — mit offenen String-Vokabularen — können aber keine
  neuen Observation-Arten definieren.
