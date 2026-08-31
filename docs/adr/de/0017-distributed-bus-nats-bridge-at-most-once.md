> Maschinell übersetzt. Maßgeblich ist die englische Fassung.

# ADR-0017: Verteilter Event-Bus = in-proc lokales Fan-out + NATS-Bridge, Core-NATS at-most-once (kein JetStream in v1)

- **Status:** accepted (ergänzt die Zeile zur Zustellsemantik in ADR-0006) — **erweitert
  durch ADR-0021**, die das At-least-once-JetStream-Backend als geschlossenes
  Enterprise-Add-on ausliefert und das Problem der Duplicate-Safety durch Deduplizierung an
  der Bus-Grenze löst (nicht durch den unten erwarteten Idempotenzdurchgang pro Subscriber);
  diese OFFENE Core-NATS-Bridge bleibt at-most-once und unverändert.
- **Date:** 2026-06-12
- **Deciders:** design pressure-tested by a 3-lens adversarial panel before implementation
- **References:** `docs/contracts/S02-sdk-runtime-eventbus.md §4`,
  `core/eventbus/natsbus`, subscriber idempotency census (recon, 2026-06-12)

## Kontext und Problemstellung

ADR-0006 ließ den Bus in-proc mit einem NATS-Slot. HA wurde ausgeliefert, also existiert Multi-Node — und der
Bus überquert keine Knoten: Ein auf einem Standby veröffentlichtes Event (Hintergrundquellen, Identitäts-Sweeps)
erreicht niemals die Verarbeitung des Leaders; die Capture der Eventing-Plattform — die Durability-
Grenze — verfehlt es stillschweigend. Zwei Fragen mussten mit Belegen beantwortet werden, nicht mit Defaults:
**(a)** Ersetzt das verteilte Backend den lokalen Zustellpfad oder überbrückt es ihn, und **(b)**
Core-NATS (at-most-once) oder JetStream (at-least-once)?

ADR-0006 verzeichnete den Bus als „at-least-once; consumers de-duplicate". **Diese Zeile war als
Beschreibung der Implementierung falsch**: Der in-proc-Bus ist at-most-once (Handler-Fehler werden geloggt,
nicht erneut versucht; in der Warteschlange befindliche Events werden beim Schließen verworfen — `core/eventbus/inproc.go`),
der S02-§4-Vertrag dokumentiert blockierenden Backpressure OHNE Redelivery, und `modules/eventing/capture.go` stellt fest:
„the bus itself is at-most-once (S02) and replay starts AT capture". Die at-least-once-Formulierung in
ADR-0006 beschrieb die Wiederaussendung auf Quellebene (`Gather`-Wiederläufe), nicht die Bus-Zustellung.

## Entscheidungstreiber

- Der Subscriber-Zensus vom 2026-06-12: Die meisten der ~17 Bus-Subscriber sind NICHT duplicate-safe
  (Eventing erfasst doppelt, security/notify persistieren oder senden Duplikate, count/aggregate-Folds
  blähen auf). At-least-once-Zustellung HEUTE wäre eine semantische Regression im Gewand eines Upgrades.
- Die schriftliche Garantie von S02 §4 — Publish blockiert unter Sättigung, „losing events silently would be
  worse than throttling a publisher" — ist tragend: `olivares_ingest_duration_seconds` ist
  als DIE Backpressure-SLI dokumentiert (docs/17 §1.4) und das Collector-Backpressure-Runbook besagt
  „no events are lost — the bus blocks rather than drops". Den lokalen Hot-Path durch einen
  Server zu leiten, würde diesen Vertrag bei 100 % des Produktionsverkehrs umkehren (der LB drainiert Standbys).
- Der Verkehr, zu dessen Rettung das Backend existiert (Standby-originierte Events), ist der Pfad mit GERINGEM
  Volumen; der lokale Pfad ist der heiße. Das Design darf den Hot-Path nicht gegen den Cold-Path tauschen.

## Betrachtete Optionen

- **A. Reiner NATS-Transport** — jedes Publish/Subscribe durchquert den Server; ein Codepfad.
  Abgelehnt: kehrt S02 §4 auf dem lokalen Pfad um (stille Slow-Consumer-Drops dort, wo der Vertrag
  blockierendes, verlustfreies Verhalten verspricht), fügt Server-Restart-/Reconnect-Verlustfenster zur Same-Node-Zustellung hinzu
  und entwertet die Bedeutung der Ingest-SLI.
- **B. Hybrid: in-proc lokales Fan-out + NATS-Bridge mit NoEcho (GEWÄHLT).**
- **C. JetStream (at-least-once)** — für v1 abgelehnt: Der Zensus zeigt, dass Subscriber nicht
  duplicate-safe sind; JetStream wird erst NACH einem Idempotenz-Durchgang über die
  Subscriber zu verfügbarer Arbeit (verfolgt als der explizite Upgrade-Pfad unten).

## Entscheidungsergebnis

Gewählte Option: **B + Core-NATS**. `core/eventbus/natsbus` bettet den in-proc-Bus ein: Publish verteilt
zunächst lokal (jede S02-§4-Garantie intakt — blockierender Backpressure, kein lokaler Verlust, Panic-
Isolation, kein Codec auf dem Hot-Path), dann überbrückt es das Event best-effort zu NATS. Die Bridge-
Verbindung setzt **NoEcho**, sodass ihr einzelnes Wildcard-Subscription nur REMOTE-originierte
Events empfängt, die es rematerialisiert (eingefrorenes Proto-`Event`-oneof für die drei Beobachtungs-Payloads,
JSON + Decoder-Registry für moduldefinierte Typen) und in das lokale Fan-out injiziert — keine doppelte
Zustellung, Per-Publisher-Reihenfolge über Typen hinweg erhalten (eine Verbindung pro Knoten, ein geordnetes
Subscription).

**Cross-Node-Semantik, ehrlich dokumentiert: at-most-once.** Verlustfenster: NATS-Server-Restart
(keine Persistenz), Reconnect-Buffer-Überlauf/nie-wiederverbunden („buffered ≠ delivered") und
Slow-Consumer-Drops, wenn der Pending-Buffer des Bridge-Subscriptions sich füllt — jeder einzelne gezählt
(`olivares_eventbus_bridge_*`) und alarmierbar, niemals still. HA: Remote-Events werden **nur
auf dem Leader injiziert** (`SetInjectGate(store.Leader().Active)`), was die Klasse der Standby-Side-Effects
(doppelte Notifications, doppelte abgeleitete Findings, ErrNotLeader-Log-Stürme) an der Bus-
Grenze ausschaltet; die ≤2s-Failover-Überlappung kann doppelt injizieren, abgefangen durch den
`(tenant_id, event_id)`-Unique-Index der Eventing-Capture. Die Konfiguration (`OLIVARES_BUS_CONFIG`) ist fail-boot-closed: Ein Knoten,
der still auf in-proc zurückfiele, liefe partitioniert.

### Konsequenzen

- **Gut:** Standby-originierte Beobachtungen erreichen den Leader (Schließen der Cross-Node-Lücke); das standardmäßige Single-Node-
  Binary ist Byte für Byte unberührt; die lokale Zustellsemantik unverändert; Cross-Node-Verlust wird
  gezählt, nicht verschwiegen.
- **Schlecht / Kompromisse:** Der Cross-Node-Pfad wird nur durch Standby-originierten Verkehr ausgeübt — sein
  Codec-/Inject-Pfad trägt dedizierte Integrationstests (eingebetteter nats-server) gerade deshalb, weil
  die Produktion ihn selten ausübt; überbrückte Events fügen pro Publish ein Encode auf Knoten MIT
  konfigurierter Bridge hinzu.
- **Neutral:** JetStream bleibt der at-least-once-Upgrade-Pfad, abhängig von einem Subscriber-
  Idempotenz-Durchgang (der Zensus ist die Arbeitsliste); das `Bus`-Interface hat nichts gewonnen — Stats und
  benannte Subscriptions sind optionale Erweiterungs-Interfaces.

## Warum die Alternativen abgelehnt wurden

Siehe Treiber: A kehrt einen schriftlichen Vertrag auf dem Hot-Path um, um den Cold-Path zu vereinfachen; C liefert
Duplikate an Subscriber aus, die sie nachweislich falsch behandeln.
