> Maschinell übersetzt. Maßgeblich ist die englische Fassung.

# ADR-0001: Architekturentscheidungen mit MADR festhalten

- **Status:** accepted
- **Datum:** 2026-06-07
- **Entscheider:** Olivares AI
- **Referenzen:** Dokumentations-/Produktsession zur Einrichtung des ADR-Registers

## Kontext und Problemstellung

Die Architekturentscheidungen der Control Plane waren über mehrere Planungs- und
Vertragsdokumente verteilt (Architektur, Stack, Lizenzierung, die Per-Session-Verträge
und die "Boot-Entscheidungen"). Diese Historie ist real und gut getrennt, aber sie liegt
nicht in einer Form vor, in der ein neuer Mitwirkender oder Bewerter sie
Entscheidung-für-Entscheidung lesen kann: *was* entschieden wurde, *warum* und *was
abgelehnt wurde*. Kontext geht zwischen Sessions verloren, wenn die Begründung nur in
langer Planungsprosa lebt.

## Entscheidungstreiber

- Ein dauerhafter, entscheidungsindizierter Datensatz, der zwischen Mitwirkenden bestehen
  bleibt.
- Ein leichtgewichtiges Format, das nicht zu einem eigenen Dokumentationsprojekt wird.
- Veröffentlichbar als Teil der Produktdokumentation.

## Betrachtete Optionen

- **MADR (Markdown Any Decision Records).** Minimal, weit verbreitet, Markdown-nativ.
- **Ein maßgeschneidertes Entscheidungslog.** Mehr Freiheit, aber keine gemeinsame
  Konvention.
- **Keine formalen ADRs.** Begründungen nur in Planungsdokumenten belassen.

## Entscheidungsergebnis

Gewählte Option: **MADR**. Jede bereits getroffene Entscheidung wird als nummeriertes
`docs/adr/NNNN-*.md` mit Kontext, der gewählten Option, Konsequenzen und abgelehnten
Alternativen erfasst und im Abschnitt *Explanation* der Dokumentationsseite
veröffentlicht.

### Konsequenzen

- **Gut:** Entscheidungen sind auffindbar und selbsterklärend; neue Mitwirkende
  verhandeln geklärte Fragen nicht erneut.
- **Schlecht / Trade-offs:** eine kleine fortlaufende Disziplin, einen Datensatz
  hinzuzufügen, wenn eine Entscheidung getroffen wird.
- **Neutral:** Bestehende Planungsdokumente bleiben die Quelle, die die ADRs zitieren,
  nicht etwas, das die ADRs ersetzen.

## Warum die Alternativen abgelehnt wurden

- **Maßgeschneidertes Log** — erfindet eine gelöste Konvention neu; schwieriger für
  externe Mitwirkende.
- **Keine ADRs** — lässt die Begründung in Prosa vergraben, was genau die Art ist, wie
  Kontext verloren ging.
