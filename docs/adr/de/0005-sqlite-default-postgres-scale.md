> Maschinell übersetzt. Maßgeblich ist die englische Fassung.

# ADR-0005: Eingebettetes SQLite als Default, Postgres + RLS für Skalierung

- **Status:** accepted
- **Date:** 2026-06
- **Deciders:** Fran Olivares
- **References:** stack decisions register (T4); data-model design

## Kontext und Problemstellung

Die Control Plane speichert ein mandantenfähiges Datenmodell (der Access Graph ist eine
*View* darüber). Sie muss als abhängigkeitsfreies einzelnes Binary für kleine/air-gapped
Installationen laufen und dennoch auf Multi-Host-, mandantenfähige Deployments skalieren.

## Entscheidungsfaktoren

- Keine externen Abhängigkeiten für den Single-Binary-/Air-Gap-Pfad.
- Starke Mandantenisolation im Skalierungsfall.
- Kein CGO, um ein reines Go-Static-Binary zu erhalten.

## Betrachtete Optionen

- **SQLite (pure-Go) → Postgres + Row-Level Security.**
- **Eine Graphdatenbank** (Neo4j, Dgraph) für den Access Graph.

## Ergebnis der Entscheidung

Gewählte Option: **eingebettetes SQLite** (`modernc.org/sqlite`, pure-Go, kein CGO) für
Single-Node und Air-Gap; **Postgres** (über `pgx`) mit **Row-Level Security** auf Basis
von `tenant_id` für Multi-Host, Skalierung und Mandantenfähigkeit. Der Access Graph wird
als **View über das allgemeine Datenmodell** modelliert, nicht als separater Store.

### Konsequenzen

- **Gut:** das einzelne Binary hat keine zu installierende Datenbank; dasselbe Modell
  skaliert auf Postgres mit Per-Tenant-RLS-Isolation.
- **Schlecht / Trade-offs:** zwei Storage-Backends zu unterstützen; die Korrektheit von
  RLS muss getestet werden (wird sie — unter erzwungenem RLS in CI).
- **Neutral:** der Access Graph braucht keine spezielle Graph-Engine, weil er eine View
  ist.

## Warum die Alternativen verworfen wurden

- **Graphdatenbank** — aufwendig im Self-Hosting und überdimensioniert: der Access Graph
  ist eine View über das relationale Modell, keine Workload, die eine dedizierte
  Graph-Engine erfordert.
