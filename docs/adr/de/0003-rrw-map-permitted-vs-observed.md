> Maschinell übersetzt. Maßgeblich ist die englische Fassung.

# ADR-0003: Die R/RW Map mit einem Permitted-vs-Observed-Diff ist eine zentrale differenzierte Fähigkeit

- **Status:** accepted
- **Date:** 2026-06-02
- **Deciders:** Fran Olivares
- **References:** product decisions register (P2); architecture (module III)

## Kontext und Problemstellung

Viele Werkzeuge können Agent-Aktivität *beobachten*, und viele können erteilte
Berechtigungen *aufzählen*. Keines davon allein beantwortet die für Governance
entscheidende Frage: **Ist das, was ein Agent berühren *darf*, dasselbe wie das, wobei er
beim Berühren *beobachtet* wird?** Das Produkt brauchte eine verteidigbare, schwer zu
kommoditisierende Fähigkeit, die das beantwortet — eine von mehreren, die es bietet, nicht
das gesamte Produkt.

## Entscheidungsfaktoren

- Eine Fähigkeit, die schwer zu kommoditisieren und für Security/SOC unmittelbar nützlich
  ist.
- Aufgebaut aus Signalen, die das Produkt tatsächlich beschaffen kann (Audit, Telemetrie,
  Kernel).
- Ehrlich über die Genauigkeit, statt zu überzeichnen.

## Betrachtete Optionen

- **Permitted-vs-Observed-Diff** (Least-Privilege-Drift) über einer Read/Write Access Map.
- **Nur Observed** — zeigen, was Agenten getan haben.
- **Nur Permitted** — erteilte Berechtigungen zeigen.
- **Session-Viewing** — Live-Agent-Sessions zeigen.

## Ergebnis der Entscheidung

Gewählte Option: **die R/RW Access Map (Modul III) mit dem Permitted-vs-Observed-Diff**.
Für jede Origin→Resource-Kante klassifiziert das Produkt Read/Write, erfasst die
Signalquelle und die Konfidenz und vergleicht deklarierte Grants mit beobachteter Nutzung,
um **Least-Privilege-Drift** sichtbar zu machen: unerwartete Zugriffe, ungenutzte Grants
und auf Reconciliation wartende Kanten.

### Konsequenzen

- **Gut:** ein unverwechselbares, sicherheitsrelevantes Artefakt, auf dem die Governance
  der Plattform aufbaut, neben den übrigen Modulen — kein Feature in Isolation.
- **Schlecht / Trade-offs:** hängt für eine belastbare Zuordnung von Per-Agent-Identität
  ab (ein geteiltes Service-Konto kollabiert auf *approximate* Konfidenz); die Abdeckung
  ist je Store **gestaffelt**; es muss ehrlich mit `unknown` und `approximate` umgehen,
  statt Gewissheit zu fingieren.
- **Neutral:** die Access Map ist eine *View* über das allgemeine Datenmodell
  (siehe ADR-0005), kein separates Schema.

## Warum die Alternativen verworfen wurden

- **Nur Observed / Nur Permitted** — jedes ist die halbe Wahrheit; der Wert ist der *Diff*.
- **Session-Viewing** — kommoditisiert (Anbieter liefern „Agent View“ aus); kein
  dauerhafter Moat.
