> Maschinell übersetzt. Maßgeblich ist die englische Fassung.

# ADR-0025: Das FinOps-Reserve→Commit/Release-Ledger schließt das TOCTOU bei Budget/Spend-Limit

- **Status:** accepted
- **Date:** 2026-07-17
- **Deciders:** Fran Olivares
- **References:** ADR-0023 (per-group window and spend ceilings — its FinOps budget dimensions are what this reservation ledger admits against), ADR-0001 (store abstraction — SQLite + Postgres, one descriptor), ADR-0009 (append-only hash-chained audit).

## Kontext und Problemstellung

`finops.CheckBudget` und `finops.CheckSpendLimit` sind rein lesende
Pre-Flight-Zulassungsprüfungen: Sie aggregieren das Kosten-Read-Model und beantworten „Liegt
diese Anfrage innerhalb der durchsetzenden Budgets/Limits, die sie scopen?“. Zwischen dieser
Antwort und dem Moment, in dem die tatsächlichen Ausgaben zurückgeschrieben werden (der
`CostSampled` → `onCost`-Ingest des Connectors), liegt ein Fenster. **N gleichzeitige Anfragen
lesen alle denselben Zustand vor der Ausgabe, kommen alle durch und sprengen gemeinsam das
Limit** — ein Check→Act-Double-Spend (TOCTOU). Ein früherer Fail-closed-Härtungsdurchgang
schloss die `Truncated`-Degradation und die Verfügbarkeitshaltung, aber die Race Condition
selbst blieb offen.

Eine korrekte Behebung muss „Obergrenze prüfen, dann Headroom verbrauchen“ **atomar** machen,
und sie muss **über Replicas hinweg auf Postgres** atomar sein, nicht nur innerhalb eines
Prozesses — ein Mutex auf Prozessebene ist daher nicht akzeptabel.

## Entscheidungstreiber

- **Die Obergrenze muss bei der Zulassung verbraucht werden, nicht beim Settlement.** N
  gleichzeitige Anfragen können nur dann nicht alle durchkommen, wenn jede Zulassung ihren
  eigenen Headroom dauerhaft abzieht, bevor die nächste liest.
- **Store-übergreifend, ein Vertrag.** Derselbe Mechanismus muss auf SQLite (eingebettet, ein
  einzelner Writer) und auf Postgres HA (mehrere Verbindungen, READ COMMITTED) halten. Die
  Atomizitätsprimitive des Stores selbst nutzen, niemals einen In-Memory-Lock.
- **Die tatsächlichen Kosten sind erst a posteriori bekannt.** Output-Tokens (und damit die
  Kosten) sind vor dem Aufruf unbekannt. Die Zulassung muss eine *Schätzung* reservieren und
  bei Abschluss abgleichen.
- **Ehrlicher Ablauf.** Ein abgestürzter Aufrufer darf Headroom nicht für immer halten, und
  seine Rückgewinnung darf niemals doppelt zählen.
- **Keine neue Schema-Engine.** Den Modul-`ExtensionRegistry`-Deskriptor + die optimistische
  Nebenläufigkeit des generischen Repos wiederverwenden.

## Entscheidungsergebnis

Ein **dynamisches Reserve-Ledger** (`finops.budget_reservation`, Tabelle
`finops_budget_reservation`) mit einem Reserve→Commit/Release-Lebenszyklus. `ReserveBudget` /
`ReserveSpendLimit` reservieren die Schätzung atomar gegen jede durchsetzende Policy, die die
Anfrage scopet; `CommitReservation` rechnet sie mit den tatsächlichen Kosten ab;
`ReleaseReservation` gibt den Headroom bei einem Fehlschlag zurück. Die Obergrenze ist überall
(`CheckBudget`, `budgetStatus`, `evaluateBudgets`) nun
`committed_spend + static ReservedMicroUSD + Σ(active, unexpired reservations)`.

Dies ist **verschieden von** dem bereits vorhandenen **statischen**
`budgetSpec.ReservedMicroUSD` (eine Priority-Tier-Kapazitätszusage, die auf das Limit
angerechnet wird). Beide werden in `effective` summiert; dieser ADR ergänzt die *dynamische
Zeile je Anfrage*.

### 1. Atomizität: ein je Scope monotoner `seq` unter einem UNIQUE-Index (kein Prozess-Lock)

Jede Reservierung trägt einen `seq`, der je **(policy, period_start, scope_key)** monoton ist,
unter dem UNIQUE-Index
`finops_budget_reservation_seq_uniq (tenant_id, policy_ref, period_start, dim_key, seq)`.
Reservieren = `max(seq)` lesen, aktuelle Ausgaben + aktive Reservierungen lesen und, wenn
Platz vorhanden ist, `INSERT` mit `seq = max+1`.

- Zwei gleichzeitige Reservierer berechnen **denselben** nächsten `seq`; der UNIQUE-Index lässt
  genau **ein** `INSERT` committen und bildet das andere auf `store.ErrConflict`
  (`mapWriteErr`) ab. Der Verlierer **wiederholt die gesamte Transaktion** und liest den nun
  committeten Zustand erneut. Das serialisiert Reserve-Check-Insert **ohne jeden
  Prozess-Lock**.
- **SQLite:** `MaxOpenConns=1` serialisiert bereits jede Transaktion auf dem einzigen Writer,
  daher ist die Reservierung für sich genommen atomar; der Seq-Index ist der doppelt
  abgesicherte Backstop.
- **Postgres READ COMMITTED (der tragende Fall):** Getrennte Verbindungen sehen die nicht
  committeten Zeilen der jeweils anderen nicht, daher erzwingt die Seq-Kollision die
  Wiederholung. **Reihenfolge-Invariante:** Die Reservierung liest `max(seq)` **vor** der
  reservierten Summe und fügt mit *jenem* seq ein — ein erfolgreiches Insert (keine Kollision)
  beweist somit, dass der gelesene seq das wahre committete Maximum war, und dass folglich die
  (strikt später gelesene) Summe jede vorherige Reservierung gesehen hat. Die beiden Reads zu
  vertauschen würde die Race Condition wieder öffnen (eine veraltete Summe zusammen mit einem
  frischen, kollisionsfreien seq würde zu viel zulassen). Per Induktion bewiesen: Das k-te
  erfolgreiche Insert sah alle k-1 vorherigen Reservierungen, sodass genau
  `floor(headroom/estimate)` zugelassen werden.

Anfragen mit mehreren Policies reservieren jedes Ziel in **einer** Transaktion
(Alles-oder-nichts): Die Verweigerung eines späteren Ziels rollt die früheren Inserts zurück;
block hat Vorrang vor throttle.

### 2. Granularität der Reservierung — je durchsetzender Policy, mit dem Scope als Schlüssel

Eine Reservierungszeile **je durchsetzender Policy, auf die die Anfrage passt**, mit
`(policy_ref, period_start, scope_key)` als Schlüssel:

- **Budgets:** `scope_key` = der Dimensionsschlüssel des Budgets (`""` für global) — ein Scope
  je Policy. Reserviert über alle 17 Nicht-Gruppen-Dimensionen, auf die die Anfrage passt (der
  übliche Fall je Anfrage: model/provider/agent/workspace/identity/api_key/…).
- **Spend-Limits je Seat:** `scope_key` = der **Akteur**, sodass eine aus einer
  Org-/Gruppen-Policy stammende Obergrenze den Headroom jedes Seats **unabhängig** reserviert
  — passend zur Semantik je Akteur von `CheckSpendLimit`.
- **Budgets auf Gruppendimension (`user_group`/`agent_group`) werden hier NICHT reserviert.**
  Ihre Ausgaben sind ein Member-Fan-out über `actor`/`agent_ref` ohne Gruppenspalte im
  Read-Model; eine Fan-out-Reservierung ist ein größerer Entwurf. Sie bleiben über den
  bestehenden präventiven Pfad von `CheckBudget` durchgesetzt. (Offenes Follow-up — siehe
  unten.)

### 3. Schätzung — eine Schätzung reservieren, beim Commit abgleichen

Die Zulassung reserviert `estimateMicroUSD` (die A-priori-Schätzung der Naht — z. B. aus
`count_tokens` über den Prompt plus einem `max_tokens`-Output-Kontingent). Bei Abschluss
stempelt `CommitReservation(handle, actualMicroUSD)` den Ist-Wert und kippt die Zeile auf
`committed`, was sie aus der aktiven Summe entfernt; die echten Ausgaben landen separat über
`onCost`. War die Schätzung **zu niedrig**, kann das Budget für diese eine Anfrage
vorübergehend um `actual − estimate` überschritten werden — begrenzt und selbstkorrigierend,
sobald die tatsächlichen Ausgaben erfasst sind. **Die Standard-Schätzungs-Policy ist eine
Produktentscheidung (siehe unten); der Mechanismus ist schätzungsagnostisch.**

**Reihenfolge:** die tatsächlichen Ausgaben ingestieren, *dann* die Reservierung committen,
damit die Obergrenze während des Settlements niemals vorübergehend unterzählt.

### 4. Ablauf — ein Prädikat, niemals ein Dekrement

Die Summe der aktiven Reservierungen filtert `state = active AND expires_at > now`. Eine
abgelaufene Reservierung **hört daher in dem Moment auf zu zählen, in dem sie verfällt** — es
gibt keinen Zähler zu dekrementieren, sodass **Doppelzählung strukturell unmöglich ist**.
`SweepExpiredReservations` stempelt nur den terminalen Zustand `expired` für Observability/GC;
die Korrektheit hängt nicht davon ab, dass es läuft. Die TTL (`reservationTTL`, standardmäßig
**5 min**) ist der Crash-Backstop für einen Aufrufer, der zwischen Reservieren und
Commit/Release gestorben ist; sie muss die langsamste governte Aktuierung überschreiten, damit
eine noch laufende Anfrage niemals verworfen wird.

### Konsequenzen

- **Positiv:** Der Double-Spend ist auf beiden Engines atomar geschlossen; die Behebung ist
  additiv (eine neue Deskriptor-Tabelle — `applyModuleTables` legt sie auf frischen wie auf
  in-place aktualisierten DBs an; keine bestehende Migration angefasst);
  `CheckBudget`/Status/Alerts spiegeln nun Reservierungen in Flight wider, sodass
  Pre-Flight-Verweigerung, Hard-Cap-Signal und das Status-DTO übereinstimmen.
- **Kosten:** Eine Reservierung sind zwei Writes (Reserve + Settle) gegenüber einer rein
  lesenden Prüfung; auf dem Hot Path sind das ein paar zusätzliche kleine Transaktionen, die
  der abgesicherte Inference-Aufruf in den Schatten stellt.
- **Latent bis zur Verdrahtung:** Das Ledger greift erst, wenn die Aktuierungsnähte
  `ReserveBudget`/`Commit`/`Release` (mit einer Schätzung) statt des rein lesenden
  `CheckBudget` aufrufen. Bis dahin ist die dynamische Reservierung 0 und das Verhalten
  unverändert. Die Verdrahtung von Inference-Proxy / HITL-Gate + die Wahl der
  Standardschätzung sind die verbleibende Integration.

## Offene Fragen (Produkt)

1. **Standardschätzung.** Wie lautet die A-priori-Schätzung, wenn die Naht keine hat?
   Optionen: `count_tokens(prompt)` + konfiguriertes `max_tokens`-Output-Kontingent zum Tarif
   des Modells; ein pauschaler Mindestwert je Anfrage; oder historische p95-Kosten je Modell.
   Unterschätzen schwächt die Garantie; Überschätzen drosselt zu früh.
2. **TTL.** Sind 5 min der richtige Crash-Backstop, oder sollte sie der maximalen
   Completion-Zeit des Modells folgen / je Surface gelten?
3. **Reservierung von Gruppenbudgets.** Sollten `user_group`/`agent_group`-Budgets ebenfalls
   reserviert werden (Member-Fan-out), oder ist rein präventives Enforcement für
   Gruppenobergrenzen akzeptabel?
4. **Haltung bei erschöpften Retries.** Bei Erschöpfung von `maxReserveRetries` (64) schlägt
   die Reservierung **fail-open** fehl (gemäß dem Vertrag von `CheckBudget`). Sollte extreme
   Contention bei einem harten `block`-Budget stattdessen **fail-closed** fehlschlagen?
