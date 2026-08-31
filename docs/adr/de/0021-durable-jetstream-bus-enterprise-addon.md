> Maschinell übersetzt. Maßgeblich ist die englische Fassung.

# ADR-0021: Dauerhaftes JetStream-Event-Bus-Backend (at-least-once + Deduplizierung an der Bus-Grenze) als geschlossenes Enterprise-Add-on

- **Status:** accepted (extends ADR-0017's "JetStream remains the upgrade path")
- **Date:** 2026-06-24
- **Deciders:** Fran Olivares (scale/reliability lever); design re-anchored against HEAD + a subscriber-idempotency re-census
- **References:** ADR-0017 (the at-most-once Core-NATS bridge), ADR-0020 (enterprise private-repo distribution),
  `LICENSING.md`, `enterprise/durablebus`, `core/eventbus/natsbus`

## Kontext und Problemstellung

ADR-0017 lieferte den verteilten Bus als lokalen In-Process-Fan-out + eine
**Core-NATS-at-most-once**-Bridge aus und **lehnte JetStream für v1 ausdrücklich ab**
(Option C), weil die Subscriber-Bestandsaufnahme vom 2026-06-12 ergab, dass die meisten
Subscriber nicht duplikatsicher waren — at-least-once hätte Duplikate an Handler geliefert,
die sie falsch behandeln. JetStream blieb der „at-least-once-Upgrade-Pfad,
**abhängig von einem Idempotenz-Pass der Subscriber**“.

Eine Governance-Control-Plane darf ein Ereignis, das eine ENTSCHEIDUNG auslöst, nicht
unbemerkt verlieren. Unter der offenen Bridge ist ein zwischen HA-Knoten verlorenes
finding.reported / cost.sampled (Serverneustart, Überlauf des Reconnect-Puffers,
Slow-Consumer-Drop) ein stillschweigend verpasstes Enforcement-Signal. Die
Enterprise-Skalierungs-/Zuverlässigkeitsstufe (Hebel Nr. 4) muss dies für die Klasse der
Enforcement-Ereignisse schließen — ohne den von ADR-0017 vorgesehenen Idempotenz-Pass je
Subscriber (eine erneute Bestandsaufnahme bestätigte, dass Subscriber weiterhin nur
„**hinreichend**“ idempotent sind: `modules/security` dedupliziert Findings beispielsweise
durch einen **begrenzten Best-Effort-Scan**, nicht durch eine harte Garantie —
`observed.go`, `anomaly.go`).

## Entscheidungstreiber

- **Nicht-Idempotenz am BUS lösen, nicht durch Vertrauen in Handler.** ADR-0017 machte
  JetStream davon abhängig, jeden Subscriber idempotent zu machen. Das ist fragil (ein über
  etwa 17 Handler verteiltes Invariant, das jede künftige Änderung erneut brechen kann) und
  wurde nie abgeschlossen. Eine einzige, verantwortete Deduplizierung an der Bus-Grenze ist
  die dauerhafte Lösung: Subscriber erhalten Dauerhaftigkeit, ohne dass jeder für immer
  korrekt bleiben muss.
- **Kein Rug Pull, keine Hot-Path-Regression.** Die tragende Einschränkung von ADR-0017
  bleibt: Der lokale In-Process-Hot-Path und die offene Core-NATS-Bridge müssen im
  Community-Binary Byte für Byte unverändert bleiben. Das Upgrade muss ADDITIV sein.
- **Monetarisierungszeitpunkt (ADR-0020).** Dauerhaftigkeit/HA ist ein Hebel der
  Enterprise-Stufe. Sie wird als geschlossener Code hinter dem Build-Tag `enterprise`
  ausgeliefert, nachdem die Trennung des privaten Repositorys das Tag zu einer realen
  Grenze gemacht hat.

## Betrachtete Optionen

- **A. Die Bridge für ALLE Typen durch JetStream ersetzen.** Abgelehnt: Dies leitet
  verlusttolerante Beobachtungen mit hohem Volumen (edge/metric) durch RAFT-Speicher und
  würde das Verhalten der offenen Bridge ändern (Rug Pull).
- **B. Dauerhaftes JetStream nur für die ENFORCEMENT-Klasse, mit eingebetteter offener
  Bridge für den Rest (GEWÄHLT).**
- **C. Persistente Deduplizierungstabelle je Subscriber im Store.** Für Phase 1 abgelehnt:
  Eine reine Enterprise-Tabelle bricht das open≡enterprise-Schemaparitäts-Gate, und eine
  offene Tabelle wäre eine schwerwiegendere Änderung, als die Garantie erfordert. Der
  Deduplizierungszustand liegt stattdessen in JetStream KV (kein Store, keine
  Schemaänderung).

## Entscheidungsergebnis

Gewählt: **B.** Ein geschlossenes Add-on `enterprise/durablebus`
(`//go:build enterprise`, `LicenseRef-Olivares-Commercial`), das den offenen
`*natsbus.Bus` **einbettet** und für die **Enforcement-Menge** (`finding.reported`,
`cost.sampled`, `guardrail.observed`, `approval.requested`, `policy.changed` — vom Operator
überschreibbar) einen JetStream-Pfad ergänzt. Mechanik:

- **Benachbarte Subject-Namespaces.** Dauerhafte Ereignisse werden an
  `<durable_prefix>.<type>` veröffentlicht (ein JetStream-Stream, RAFT, Replikate ≥ 3),
  DISJUNKT vom `<subject_prefix>.>` der Core-Bridge — ein Typ wird also über genau einen
  Transport und niemals über beide ausgeliefert. Die eingebettete Bridge wird angewiesen,
  die dauerhafte Menge vom Core-Bridging AUSZUSCHLIESSEN
  (`natsbus.Options.BridgeExclude`, im offenen Binary inert). Nicht-Enforcement-Typen
  behalten die at-most-once-Reichweite der offenen Bridge (keine Regression).
- **Publish bestätigt den PubAck** (`Nats-Msg-Id = event.ID`): Ein dauerhaftes Ereignis wird
  entweder dauerhaft gespeichert oder der Fehler wird sichtbar gemacht — niemals
  stillschweigend verworfen; das Duplikatfenster des Streams reduziert einen Retry / eine
  doppelte Failover-Veröffentlichung auf eine gespeicherte Kopie.
- **Leader-gegateter dauerhafter Consumer** (Ack-explicit), bei Promotion gebunden und bei
  Demotion über einen `Active()`-Watcher gestoppt (der Elector stellt kein OnDemote bereit);
  seine serverseitige Position überlebt ein Failover. Enforcement läuft clusterweit einmal.
- **Deduplizierung nach event.ID an der Inject-Grenze**, zweistufig: ein In-Memory-Zeitfenster
  (schnell, gleicher Knoten) und ein **JetStream-KV**-Bucket (RAFT-repliziert, TTL-begrenzt,
  überlebt Absturz/Neustart und dedupliziert über Knoten hinweg). LESEN-vor-Inject (Duplikat
  unterdrücken) + AUFZEICHNEN-nach-Inject (damit ein Absturz erneut injiziert statt verliert).

**Ehrliche Semantik: at-least-once, NIEMALS exactly-once.** Unter normalem und mäßig
degradiertem Betrieb tritt kein VERLUST auf (Aufzeichnung nach Inject; ein bestätigtes
Publish ist dauerhaft; der Consumer setzt an seiner bestätigten Position fort). Der EINE
verbleibende Verlustpfad ist durch die Aufbewahrung begrenzt: Der Stream behält eine
Nachricht höchstens `MaxAge` lang (standardmäßig 72 h, `LimitsPolicy`), sodass ein
gespeichertes Ereignis verworfen wird, wenn KEIN Leader es länger als `MaxAge` abarbeitet —
vollständiger Quorumverlust / mehrtägiger leaderloser oder partitionierter Ausfall. Dieses
Fenster wird durch den SLI `olivares_durablebus_stream_pending` beobachtbar (für einen
Backlog, der sich `MaxAge` nähert, kann alarmiert werden), es ist also niemals ein stiller
Drop; der Operator erhöht `MaxAge` oder stellt einen Leader wieder her, um es bei null zu
halten. Ein DUPLIKAT ist nur in zwei begrenzten Fenstern möglich — der ≤2-s-
Führungsüberschneidung und einem harten Absturz zwischen Inject und Deduplizierungsrecord —,
die beide downstream absorbiert werden (der `(tenant_id, event_id)`-Index der
Eventing-Erfassung und die begrenzte Scan-Deduplizierung von Security). Die offene Bridge
bleibt at-most-once und unverändert.

### Konsequenzen

- **Gut:** Enforcement-Ereignisse überleben die knotenübergreifende Zustellung
  (at-least-once) mit einer verantworteten Deduplizierungsgarantie; das Community-Binary ist
  byte-identisch (das Add-on fehlt; die einzige offene Naht `BridgeExclude` ist inert);
  keine Store-Schemaänderung (Deduplizierung liegt in JetStream KV) ⇒ Schemaparität bleibt
  unberührt; Fail-Boot-Closed (kann ein deklarierter dauerhafter Backend nicht eingerichtet
  werden, bricht der Boot ab; ein nicht lizenziertes Enterprise-Binary degradiert SICHTBAR
  auf die offene Core-NATS-Bridge, niemals still auf einen Einzelknoten).
- **Schlecht / Abwägungen:** Dauerhafte Zustellung kostet beim Publish einen
  JetStream-Roundtrip (PubAck) und beim Inject einen KV-Read — für die Enforcement-Klasse
  mit moderatem Volumen akzeptabel; der Operator kann die dauerhafte Menge einschränken.
  Dauerhafte Ereignisse erreichen Subscriber nur auf dem Leader (über den Consumer), daher
  werden die eigenen dauerhaften Publishes eines Knotens nicht lokal aufgefächert
  (konsistent mit „Enforcement nur auf dem Leader“). Das Bus-Lizenz-Gate ist beim Boot aktiv
  (die Installation einer Lizenz zur Aktivierung der Dauerhaftigkeit erfordert einen
  Neustart, anders als die hot-applied Sitzplatzgrenze).
- **Neutral:** Phase 2+ des Hebels (DR-Leiter, Multi-Region, Tenant-Silo/CMEK) ist eine
  dokumentierte Roadmap (`enterprise/durablebus/doc.go`), NICHT gebaut.

## Warum die Alternativen verworfen wurden

A führt einen Rug Pull an der offenen Bridge durch und belastet den Hot Path; C tauscht ein
kleines KV gegen eine Kernschemaänderung, die das Paritäts-Gate bricht. B beschränkt die
Änderung auf geschlossenen, additiven Code und löst ADR-0017s Duplikatsicherheitsproblem an
der Bus-Grenze statt über den nie abgeschlossenen Pass je Subscriber.
