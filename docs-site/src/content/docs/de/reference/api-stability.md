---
title: API-Stabilität, Versionierung, Deprecation & Sunset
description: >-
  Das Versionierungsschema, die Stabilitätsstufen, die Deprecation-Signalisierung
  (RFC 9745 / RFC 8594-Header) und die Mindest-Supportfenster für die REST API, das
  gRPC-Spiegelbild, den Live-Ingest-Wire-Vertrag, den Terraform-Provider und die
  Client-SDKs.
---

Diese Seite ist der **Stabilitätsvertrag** für alles, was gegen die Control Plane
programmiert. Sie legt fest, was stabil ist, wie ein Breaking Change signalisiert wird
und wie lange eine als deprecated markierte Schnittstelle weiter funktioniert. Die
Durchsetzung steckt im Code, nicht in Prosa: die Deprecation-Tabelle, die Response-Header,
die OpenAPI-Marker und die Fensterprüfungen unten werden alle aus einer einzigen
In-Code-Deklaration (`core/api/stability.go`) gespeist, und ein Sunset, der früher
angesetzt ist, als die Richtlinie erlaubt, **lässt den Build fehlschlagen**.

:::note[Pre-1.0-Status]
Olivares AI ist pre-1.0 (siehe [Ehrlichkeit und Grenzen](/de/start/honesty-and-limits/)).
Die Signalisierungsmechanismen auf dieser Seite sind bereits aktiv; die **Mindest-Supportfenster
binden ab dem 1.0/GA-Release**. Bis dahin wird die veröffentlichte Schnittstelle in der Praxis
stabil gehalten, aber die formalen Fenster unten sind die Zusage, an der Sie uns ab GA festhalten
können.
:::

## Abgedeckte Schnittstellen und Stufen

| Schnittstelle | Versioniert nach | Stufe heute |
|---|---|---|
| REST-Kernvertrag — die Pfade im [ausgelieferten OpenAPI-Dokument](/reference/api/) | URL-Major (`/v1/…`) | **stable** |
| gRPC-Spiegelbild — `ControlPlane` im Proto-Paket `olivares.api.v1` | Proto-Paket-Major | **stable** (eingefrorenes Spiegelbild) |
| Live-Ingest / Connector-Wire — Proto-Paket `olivares.sdk.v1` | Proto-Paket-Major + Plugin-`ProtocolVersion` | **stable** (eingefroren) |
| Connector-SDK (Go) — Module `sdk`, `sdk/plugin` (Autoren-Schnittstelle) | Modul-semver — Tags `sdk/v*`, `sdk/plugin/v*` ab dem ersten öffentlichen Release | **stable v1** (Go-Vertrag; Wire-Zeile oben) |
| [Event-Bus-Vertrag](/de/reference/events/) (AsyncAPI 3.0) — seine Event-Typen sind zugleich das, was die Eventing-Plattform an [externe Webhook-Subscriptions](/de/reference/events/#externe-subscriptions-eventing-plattform) ausliefert; die Routen zur Subscription-Verwaltung sind Modul-Routen (`/v1/m/eventing/`, außerhalb des Vertrags), aber jeder **Event-Typ** trägt aus dem In-Code-Katalog seine eigene Stabilitätsstufe | `info.version` (`1.0.0-preview`) | **beta** (Dokument); Stufen pro Event-Typ |
| Terraform-Provider | eigenes semver (`terraform-provider-v*`-Tags) | **stable**, MAJOR folgt API v1 |
| Client-SDKs (Go / Java / Python / TypeScript) | eigenes semver; MAJOR folgt ab GA dem API-Major | **beta** (pre-1.0-Pakete) |
| Alles nicht Aufgeführte — Modul-Routen `/v1/m/<ns>/`, SCIM, Föderation, Interna | — | **out of contract** |

**Stufen.** Eine *stable*-Schnittstelle ändert sich innerhalb ihrer Major-Version nicht
inkompatibel; sie zu entfernen oder zu ändern erfordert den Deprecation-Prozess unten. Eine
*beta*-Schnittstelle kann sich noch in der Form ändern, erhält aber dieselbe Signalisierung und
ein kürzeres Fenster. Eine *out-of-contract*-Schnittstelle (insbesondere die Modul-Routen, die
bewusst außerhalb des OpenAPI-Dokuments liegen — siehe die
[Referenz-Übersicht](/de/reference/)) trägt keine Kompatibilitätszusage; ihre Verträge leben in den
typisierten Schnittstellen, die mit dem Produkt ausgeliefert werden.

Jede Operation im OpenAPI-Dokument trägt einen maschinenlesbaren `x-stability`-Marker, und das
Dokument selbst verlinkt diese Seite in `info.x-stability-policy`.

## Was als Breaking Change gilt

Für eine stable-Schnittstelle sind all dies Breaking Changes und unterliegen dem Prozess unten:

- Entfernen oder Umbenennen eines Pfads, einer Methode, eines Request-Felds, eines Response-Felds
  oder eines Fehler-`code`;
- Ändern von Typ oder Bedeutung eines Felds oder das Erforderlich-Machen eines optionalen
  Request-Felds;
- Verschärfen von Authentifizierung/Autorisierung derart, dass ein zuvor gültiger Aufruf
  fehlschlägt;
- für gRPC/protobuf: alles, was `buf breaking` (FILE-Ruleset) ablehnt.

Diese sind **keine** Breaking Changes: Endpunkte hinzufügen, optionale Request-Parameter
hinzufügen, Response-Felder hinzufügen, neue Fehlercodes für neue Fehlerfälle hinzufügen und
Response-Header hinzufügen. Clients müssen unbekannte JSON-Felder tolerieren.

## Versionierung

- **REST** wird in der URL versioniert: der gesamte stable-Vertrag liegt unter `/v1/`. Eine
  inkompatible Änderung wird unter `/v2/` ausgeliefert und `/v1/` tritt in die Deprecation ein —
  nie ein In-Place-Bruch.
- **gRPC** wird nach Proto-Paket versioniert: `olivares.api.v1` / `olivares.sdk.v1`. Eine
  inkompatible Änderung erfordert einen neuen Paket-Major (`…v2`); beide Verträge werden durch
  `buf breaking` gegen `main` abgesichert (`task proto:breaking`).
- **Der Terraform-Provider** wird unabhängig veröffentlicht (`terraform-provider-v*`-Tags); sein
  MAJOR folgt dem API-Major, den er spricht.
- **Client-SDKs** betten `API_VERSION` (den Vertrags-Major, aus dem sie generiert wurden) und
  `SPEC_HASH` (den exakten OpenAPI-Snapshot) ein — `APIVersion` und `SpecHash` in Go; ab GA folgt
  ihr MAJOR dem API-Major.
- **Das Connector-SDK** (der Go-Vertrag, gegen den Drittanbieter-Connectors bauen) wird über
  per-Modul-semver-Tags versioniert (`sdk/vX.Y.Z`, `sdk/plugin/vX.Y.Z`) und durch dieselbe
  `buf breaking`-Wand auf seinem Wire abgesichert. Schnittstellen, die ein Autor implementiert,
  erhalten innerhalb eines Major nie neue Methoden; neue Fähigkeit kommt als neue optionale
  Schnittstellen. Die vollständige Richtlinie wird mit dem Modul ausgeliefert
  (`sdk/VERSIONING.md`); der Autoren-Lebenszyklus steht in
  [Einen Connector bauen und ausliefern](/de/how-to/build-a-connector/).

## Deprecation-Prozess und Signalisierung

Eine Deprecation ist ein deklarierter Eintrag in der In-Code-Tabelle plus ein Migrationsleitfaden;
alles andere folgt daraus mechanisch.

1. **Ankündigen.** Der Eintrag landet mit seinem Ankündigungsdatum und der URL des
   Migrationsleitfadens. Ab diesem Moment trägt jede Response der deprecateten Route den
   [RFC 9745](https://www.rfc-editor.org/rfc/rfc9745)-Header und einen Link zum Leitfaden, und die
   OpenAPI-Operation erhält `deprecated: true`, `x-deprecated-at` und `x-migration-guide`:

   ```http
   Deprecation: @1780272000
   Link: <https://olivares.ai/docs/how-to/migrate-example/>; rel="deprecation"
   ```

2. **Den Sunset planen.** Wenn das Abschaltdatum festgelegt ist, fügen Responses den
   [RFC 8594](https://www.rfc-editor.org/rfc/rfc8594)-Header hinzu (und die Spec erhält
   `x-sunset-at`):

   ```http
   Sunset: Thu, 01 Jun 2028 00:00:00 GMT
   Link: <https://olivares.ai/docs/how-to/migrate-example/>; rel="sunset"
   ```

3. **Entfernen** — frühestens am Sunset-Datum, normalerweise mit dem nächsten API-Major.

**Mindest-Supportfenster** (Deprecation-Ankündigung → Sunset):

| Stufe | Mindestfenster |
|---|---|
| stable | **24 Monate** |
| beta | **12 Monate** |

Diese Fenster werden durch Tests gegen die Deklarationstabelle durchgesetzt: ein Eintrag, dessen
Sunset das Fenster seiner Stufe verletzt oder der auf eine nicht existierende Route zeigt, baut
nicht.

Für **gRPC** wird Deprecation mit der protobuf-Option `deprecated` ausgedrückt (die im generierten
Code sichtbar wird) plus denselben Fenstern; die Wire-Verträge sind im Übrigen eingefroren und
`buf breaking` lehnt inkompatible Änderungen rundheraus ab.

## Was Clients sehen

- **Terraform-Provider** — gibt eine `tflog`-WARN aus (Methode, Endpunkt, Daten, Leitfaden),
  einmal pro eindeutiger Methode und Request-Pfad pro Lauf, wenn eine Control-Plane-Response ein
  Deprecation-Signal trägt (eine deprecatete parametrisierte Route warnt einmal pro Ressource, die
  sie berührt), und sendet einen versionierten `User-Agent`, sodass die Nutzung deprecateter
  Clients serverseitig zuordenbar ist.
- **Go-SDK** — zeigt eine `DeprecationNotice` einmal pro Endpunkt (Standard: eine
  `slog`-Warnung; überschreibbar mit `WithDeprecationHandler`). Deprecatete Operationen tragen
  Go-`// Deprecated:`-Marker, sodass Editoren und `staticcheck` sie zur Entwicklungszeit
  markieren.
- **Python-SDK** — eine `DeprecationWarning` pro Endpunkt (oder Ihr `on_deprecation`-Callback);
  deprecatete Operationen sind in den Docstrings markiert.
- **TypeScript-SDK** — eine `console.warn` pro Endpunkt (oder Ihr `onDeprecation`-Callback);
  deprecatete Operationen tragen `@deprecated`-JSDoc.

## Verwandt

- [REST-API-Referenz](/reference/api/) — der stable-Vertrag selbst
- [Die Client-SDKs verwenden](/de/how-to/use-the-client-sdks/)
- [Einen Connector bauen und ausliefern](/de/how-to/build-a-connector/) — der Vertrag und
  Lebenszyklus des Connector-SDK
- [Als Code verwalten (Terraform)](/de/how-to/manage-as-code/)
- [Modul XIX — eigene API + Manage-as-Code](/de/reference/modules/xix-api-manage-as-code/)
- [Event-Bus (AsyncAPI 3.0)](/de/reference/events/)
- [Ehrlichkeit und Grenzen](/de/start/honesty-and-limits/)
