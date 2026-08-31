> Maschinell übersetzt. Maßgeblich ist die englische Fassung.

# ADR-0007: Out-of-Process-Modul-/Connector-Runtime via go-plugin (gRPC)

- **Status:** accepted
- **Datum:** 2026-06
- **Entscheider:** Olivares AI
- **Referenzen:** Stack-Design (Modul-Runtime); License-Boundary-Design

## Kontext und Problemstellung

Die Plattform muss es First-Party- und Drittanbieter-Connectors und -Modulen ermöglichen, sie zu
erweitern, ohne deren Abhängigkeitsbäume in die Engine zu ziehen, und ohne das permissive
Connector-Ökosystem mit der Copyleft-Lizenz der Engine zu kontaminieren.

## Entscheidungstreiber

- Connector-Abhängigkeiten vom Build/SBOM der Engine isolieren.
- Ein stabiler, versionierter Vertrag über die Prozessgrenze hinweg.
- Die Apache-2.0-Connector-Grenze sauber halten (ein Connector linkt niemals die AGPL-Engine).

## Betrachtete Optionen

- **`hashicorp/go-plugin` über gRPC** für Out-of-Process-Module/-Connectors, zuzüglich
  kompilierter Kernmodule In-Process.
- **Nur In-Process-Plugins** (Go-`plugin`-Package oder einkompiliert).

## Entscheidungsergebnis

Gewählte Option: **`hashicorp/go-plugin` (gRPC)** für Out-of-Process-Connectors/-Module,
wobei First-Party-Connectors eingebettet und als isolierte Subprozesse gestartet werden und
Kernmodule einkompiliert sind. Das Connector-SDK ist ein Go-Interface plus ein versionierter
gRPC-/Protobuf-Vertrag.

### Konsequenzen

- **Gut:** Die Abhängigkeiten eines Connectors gelangen nicht in das Binary/SBOM der Engine;
  die Apache-/AGPL-Grenze bleibt sauber und wird in CI durchgesetzt; Dritte können Connectors
  unabhängig ausliefern.
- **Schlecht / Abwägungen:** Ein gRPC-Vertrag, der zu versionieren ist, sowie ein IPC-Hop für
  Out-of-Process-Komponenten.
- **Neutral:** Das einzelne Binary bettet weiterhin First-Party-Connectors ein
  (subprozess-isoliert), sodass es ein Artefakt bleibt.

## Warum die Alternativen verworfen wurden

- **Nur In-Process** — zieht die Abhängigkeiten jedes Connectors in die Engine und macht es
  unmöglich, die Lizenzgrenze mechanisch durchzusetzen.
