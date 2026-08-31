---
title: Verifizierte Connectors (Drittanbieter)
description: >-
  Das kuratierte Verzeichnis von Drittanbieter-Connectors, deren Releases die
  Maintainer erneut verifiziert haben — Grenze, Signatur, Provenienz und
  Minimal-Data-Prüfung — und wie Sie Ihren einreichen.
---

Diese Seite ist das **kuratierte Verzeichnis von Drittanbieter-Connectors**. Sie
ist das externe Pendant zum [First-Party-Connector-Katalog](/de/reference/connectors/):
First-Party-Connectors werden mit dem Produkt ausgeliefert; die hier aufgeführten
Connectors werden von **ihren Herausgebern** mit dem öffentlichen
[Connector-SDK](/de/how-to/build-a-connector/) gebaut, veröffentlicht und gepflegt.

## Was "verifiziert" bedeutet

Ein gelistetes Release wurde von den Maintainern manuell erneut anhand dieser
Checkliste verifiziert:

1. **Lizenzgrenze** — der Connector baut außerhalb des Baums (out-of-tree) und
   verlinkt nichts aus der AGPL-Engine (`go list -deps` zeigt kein
   `github.com/olivaresai/olivares/core`); er importiert nur das Apache-2.0-SDK.
2. **Signatur & Provenienz** — das veröffentlichte Sigstore-Attestation-Bundle
   verifiziert gegen die angegebene Identität oder den öffentlichen Schlüssel des
   Herausgebers, und sein Subject-Digest stimmt mit dem freigegebenen Binary überein.
3. **Vertragskonformität** — `Descriptor.Name` ist mit Punkten notiert und
   anbieter-namespaced, die deklarierten `ConfigFields` entsprechen dem, was `Open`
   liest, Secrets sind als `Secret: true` deklariert und werden per Referenz übergeben.
4. **Minimal Data** — der Connector emittiert Referenzen und Metadaten, niemals
   Payloads, Prompts oder Secret-Werte (stichprobenartige Prüfung der Emit-Pfade).

**Was es nicht bedeutet:** Verifizierung ist kein Sicherheitsaudit des
Herausgebers oder des beobachteten Systems, keine Empfehlung und **kein Trust
Root** — ein Operator, der einen verifizierten Connector verdrahtet, pinnt
weiterhin den Schlüssel oder die Identität des Herausgebers in `connector_trust`
und den Release-Digest im `plugin`-Block der Quelle. Die Zulassung am Host bleibt
in jedem Fall deny-closed.

Ein privater Connector muss hier nicht gelistet sein, um governed zu sein. Wenn ein
Operator seinen Digest und Trust Anchor in `connector_trust` pinnt, wendet die Engine
dieselbe deny-closed Admission und Runtime-Governance an. Dieser Index ist ein
Zertifizierungsnachweis für Auffindbarkeit und erneute Verifizierung, kein Trust Root.

## Verzeichnis

Noch sind keine Drittanbieter-Connectors gelistet — das Programm startet mit diesem
Release. First-Party-Connectors finden Sie im
[Connector-Katalog](/de/reference/connectors/).

| Connector (`Descriptor.Name`) | Herausgeber | Art | Verifiziertes Release | Signatur | Quelle |
|---|---|---|---|---|---|
| _noch keine_ | | | | | |

## Einen Connector einreichen

Öffnen Sie einen Pull Request gegen diese Seite, der eine Tabellenzeile hinzufügt
und Folgendes verlinkt:

- das Quell-Repository und das Release (Binary + `sha256` + Sigstore-Bundle);
- die Identität, gegen die verifiziert werden soll (OIDC-Identität + Issuer für
  keyless, oder den öffentlichen Schlüssel);
- die Ausgabe von `./scripts/check-boundary.sh` und den Testlauf in Ihrer CI.

Die Maintainer reproduzieren die obige Checkliste anhand der exakten
Release-Artefakte. Ein neues Release eines gelisteten Connectors erfordert eine
Aktualisierung der Zeile (die Re-Verifizierung erfolgt pro Release, weil das Urteil
an den Digest gebunden ist). Veraltete oder zurückgezogene Releases werden entfernt.

## Verwandt

- [Einen Connector bauen und ausliefern](/de/how-to/build-a-connector/) — der komplette Lebenszyklus
- [Modul XIV — interner Katalog & Marketplace](/de/reference/modules/xiv-catalog/) —
  Zertifizierung im Produkt (Connector-Einträge + signierte Zulassung)
- [API-Stabilität](/de/reference/api-stability/) — der SDK-Stabilitätsvertrag
- [Ein Release verifizieren](/de/how-to/verify-a-release/) — dieselbe
  Lieferketten-Disziplin für die eigenen Artefakte des Produkts
