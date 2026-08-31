> Maschinell übersetzt. Maßgeblich ist die englische Fassung.

# ADR-0018: Realtime-Voice-Backend — dokumentierter ruhender Zustand in v1, Integration nach v1

- **Status:** accepted
- **Datum:** 2026-06-12
- **Entscheider:** Fran Olivares
- **Referenzen:** `modules/liveingest/voice.go:28`
  (`PublishVoiceTelemetry`), `modules/voice` (Modul XVI)

## Kontext und Problemstellung

Die Voice-Telemetrie-Sonde ist Ende-zu-Ende gebaut und validiert: `liveingest.PublishVoiceTelemetry`
veröffentlicht eine per Allowlist freigegebene `voice.Telemetry` als `voice.telemetry.observed`, und Modul XVI fügt
sie über einen strikt neu validierenden Consumer in die Session-Metadaten ein. Nichts ruft den Producer auf irgendeinem
Produktionspfad auf — es gibt kein Realtime-Voice-Backend im Build — also ist die Beobachtungshälfte leer.
Es handelt sich um eine reine Naht. Die Frage lautet: jetzt ein Backend integrieren (z. B. LiveKit) oder
den Zustand deklarieren?

## Entscheidungsergebnis

**v1 liefert die Sonde ruhend aus und SAGT das auch.** Der ehrliche Zustand ist bereits im Code erzwungen: Der
Producer lehnt verwerfbare Samples ab und erfindet nichts; das `Start` von liveingest protokolliert "voice
telemetry probe wired but dormant — no realtime voice backend in this build emits turn metadata";
die Beobachtungshälfte bleibt sichtbar leer, statt fälschlich gefüllt zu sein (niemals eine stille Lücke —
und ebenso niemals erfundene Vollständigkeit). Die Integration eines konkreten Realtime-Backends (LiveKit oder
gleichwertig) ist eine **Session nach v1, falls und sobald Bedarf besteht**.

Die Scale-out-Arbeit machte die Naht unterwegs Multi-Node-ehrlich: Ein künftiger Dispatcher, der
die Sonde auf IRGENDEINEM Knoten speist, erreicht nun das Voice-Modul des Leaders über die NATS-Bridge (die
Composition Root registriert den Decoder für die `voice.Telemetry`-Payload), sodass die ruhende Naht unter HA
nicht stillschweigend zu einer Single-Node-only-Naht wurde.

### Konsequenzen

- **Gut:** keine spekulative Abhängigkeit; die Naht ist getestet (Producer + Consumer + NATS-Bridge-
  Decoder), sodass eine künftige Integration Verkabelung ist, nicht Design.
- **Schlecht / Abwägungen:** Die Voice-Beobachtungsfläche bleibt in v1 leer — im UI-Vertrag
  als deklarierte Naht dokumentiert, was der wahrheitsgetreue Zustand ist.
- **Neutral:** Die Entscheidung ist bedarfsgesteuert, nicht architekturgesteuert.
