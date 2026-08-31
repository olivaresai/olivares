> Maschinell übersetzt. Maßgeblich ist die englische Fassung.

# ADR-0006: In-Process-Event-Bus als Standard, transportunabhängig für NATS

- **Status:** accepted
- **Datum:** 2026-06
- **Entscheider:** Olivares AI
- **Referenzen:** SDK-/Runtime-/Event-Bus-Vertrag; Stack-Design

## Kontext und Problemstellung

Connectors heben Beobachtungen auf einen internen Event-Bus; Module und Output-Connectors
abonnieren nach Typ. Das einzelne Binary muss ohne Message-Broker funktionieren, doch ein
Deployment über mehrere Hosts hinweg benötigt einen verteilten Bus.

## Entscheidungstreiber

- Keine Broker-Abhängigkeit im Standardfall des einzelnen Binarys.
- Ein Weg zu einem verteilten Bus, der Abonnenten nicht zu Änderungen zwingt.

## Betrachtete Optionen

- **In-Process-Go-Channels als Standard, hinter einem transportunabhängigen `Bus`-Interface**,
  das eine verteilte Implementierung (NATS) ersetzen kann.
- **Ein Broker (NATS) von Anfang an.**

## Entscheidungsergebnis

Gewählte Option: **In-Process-Go-Channel-Bus als v1-Standard**, wobei das `Bus`-Interface
**keinen Channel** offenlegt, sodass für Deployments über mehrere Hosts eine **NATS**-Implementierung
eingesetzt werden kann, **ohne auch nur einen einzigen Abonnenten zu ändern**. Die Zustellung erfolgt
asynchron und mindestens einmal (at-least-once); Consumer deduplizieren anhand des Natural-Key-Zeitstempels.

> **Geändert durch ADR-0017 (2026-06-12):** Das „at-least-once“ im vorigen Satz war als Beschreibung
> der BUS-Zustellung falsch — die Implementierung und der Vertrag S02 §4 sind at-most-once
> (Handler-Fehler werden geloggt, eingereihte Events werden beim Schließen verworfen);
> at-least-once gilt für die erneute Emission auf Source-Ebene (`Gather`-Re-Runs), was die
> Consumer deduplizieren. ADR-0017 dokumentiert das ausgelieferte NATS-Backend: lokales
> In-Process-Fan-out unverändert + NoEcho-Bridge, knotenübergreifend at-most-once, kein JetStream in v1.

### Konsequenzen

- **Gut:** Das einzelne Binary benötigt keinen Broker; der verteilte Weg ist ein Drop-in.
- **Schlecht / Abwägungen:** At-least-once-Semantik verlagert die Deduplizierung auf die Consumer.
- **Neutral:** NATS ist optional und nur für die verteilte Topologie.

## Warum die Alternativen verworfen wurden

- **Broker von Anfang an** — fügt jeder Installation eine externe Abhängigkeit hinzu und
  untergräbt damit das Ziel von einzelnem Binary / Air-Gap, für einen Wert, den nur die
  verteilte Topologie benötigt.
