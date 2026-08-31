---
title: "Einen governten Workflow (DAG) erstellen"
description: "Bestehende governte Aktionen zu einem Abhängigkeitsgraphen zusammensetzen, seinen Ausführungsplan ohne Seiteneffekte prüfen und ihn hinter einer menschlichen Genehmigung ausführen, die an genau den geprüften Graphen gebunden ist."
---

Ein **Workflow** verkettet Aktionen, die die Plattform bereits governt — einen
Zeitplan auslösen, andere Module signalisieren, eine Testbenachrichtigung
senden, warten — zu einem Abhängigkeitsgraphen (einem DAG). Seine Ausführung
ist eine einzige privilegierte, von einem Menschen genehmigte Aktion, und jeder
Schritt, der etwas berührt, hinterlässt eine Zeile in demselben
Append-only-Entscheidungs-Ledger wie das einmalige Auslösen eines Zeitplans.

Workflows sind **Komposition, keine neue Macht**. Es gibt bewusst keinen
Schritttyp, der einen Befehl ausführt, eine beliebige URL aufruft oder ein
Payload mitführt: Ein Graph kann nur Verben neu anordnen, die das Estate
bereits bereitstellt, und durchläuft dabei bereits vorhandene Gates. Die
Ausführung eines Workflows ist Admin-Tier *und* von einem Menschen genehmigt;
sie ist daher niemals ein Weg, etwas zu erreichen, das nicht auch direkt
erreichbar wäre.

## Form eines Graphen

Ein Workflow besteht aus **Schritten**. Jeder Schritt hat eine kurze, innerhalb
des Workflows eindeutige `ref`, einen `kind`, seine typisierte `config` und die
Refs, von denen er `depends_on`. Der Graph muss azyklisch sein; der Server
erzwingt dies ebenso wie die Existenz der Referenzen und die Grenzen für
Fan-in/Fan-out, bevor etwas gespeichert wird.

| Typ | Funktion | Durchlaufene Gates |
|---|---|---|
| `schedule-fire` | löst einen vorhandenen governten Zeitplan aus | Kill-Switch, Budget, Dispatcher-Seam |
| `eventing-emit` | veröffentlicht ein `workflow.signal`-Ereignis, das andere Module abonnieren können | — |
| `notify-test` | sendet den synthetischen Test über eine Alert-Route | Notify-Actuator-Seam |
| `wait` | pausiert den Lauf für eine begrenzte Zeit (1 s–24 h) | — |
| `approval-gate` | öffnet **mitten im Graphen** eine menschliche Genehmigung und pausiert bis zur Entscheidung | Approval-Gate |

`eventing-emit` veröffentlicht einen **festen** Ereignistyp. Die Konfiguration
des Schritts trägt nur ein Label bei. Ein Workflow-Autor kann daher niemals ein
First-Party-Ereignis wie `edge.observed` für die Ingestion eines anderen Moduls
fälschen.

## 1. Workflow deklarieren

```bash
curl -sS -X POST "$OLIVARES/v1/m/orchestration/workflows" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  -H 'Content-Type: application/json' -d '{
    "name": "release-train",
    "steps": [
      {"ref":"announce","kind":"eventing-emit","config":{"label":"starting"},"depends_on":[]},
      {"ref":"hold","kind":"approval-gate","config":{"reason":"release window"},"depends_on":["announce"]},
      {"ref":"deploy","kind":"schedule-fire","config":{"schedule_id":"<id>"},"depends_on":["hold"]}
    ]}'
```

Das Erstellen ist **Write-Tier**. Ein abgelehnter Graph kommt als `400` zurück
und nennt den fehlerhaften Schritt:

```json
{"error":{"message":"step deploy: schedule <id> is retired","step_ref":"deploy"}}
```

Die Konsole verankert diese `step_ref` am Knoten auf der Zeichenfläche. Ein
späterer Austausch des Graphen erfolgt durch ein einziges atomares
`PUT .../steps` — der Graph wird als Ganzes geprüft und genehmigt, niemals
Schritt für Schritt.

Jede Änderung hängt einen vollständigen Snapshot an ein Revisions-Ledger an;
jede frühere Revision kann über dieselbe Validierung wiederhergestellt werden,
die auch die Live-Verben verwenden.

## 2. Plan prüfen — ohne Seiteneffekte

```bash
curl -sS -X POST "$OLIVARES/v1/m/orchestration/workflows/$ID/dry-run" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT"
```

Der Dry-Run gibt die Schritte in topologischer Reihenfolge zurück: was jeder
Schritt tun würde, welche Gates er passieren würde und eine Warnung, wenn eine
Referenz seit dem Speichern des Graphen veraltet ist (etwa ein letzte Woche
stillgelegter Zeitplan). Er schreibt und dispatcht nichts und öffnet keine
Genehmigung. Deshalb ist er ein **Read**, verfügbar für alle, die Workflows
lesen dürfen.

Er gibt außerdem den `plan_hash` zurück — den Fingerprint des exakten Graphen.
Lesen Sie weiter.

## 3. Ausführen — zwei Phasen, an die menschliche Prüfung gebunden

Die Ausführung ist Admin-Tier **und** gegatet. Phase eins öffnet die
Genehmigung:

```bash
curl -sS -X POST "$OLIVARES/v1/m/orchestration/workflows/$ID/run" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT"
# 202 {"op":"run_request","approval_ref":"…","gate_status":"pending", …}
```

Ein Mensch entscheidet über die Governance-Decision-API. Phase zwei
verbraucht diese Entscheidung, indem die Referenz zurückgegeben wird:

```bash
curl -sS -X POST "$OLIVARES/v1/m/orchestration/workflows/$ID/run" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  -H 'Content-Type: application/json' -d '{"approval_ref":"…"}'
```

Die Genehmigung ist **an den Plan-Hash gebunden**. Wird der Graph zwischen den
beiden Phasen bearbeitet, ändert sich der Hash; die Genehmigung autorisiert
dann nichts mehr und der Lauf wird abgelehnt — das „Ja“ eines Menschen gilt
für den geprüften Graphen, niemals für einen später untergeschobenen. Der Lauf
führt anschließend einen **Snapshot** dieses Graphen aus. Eine Bearbeitung
während des Laufs kann daher nicht verändern, was bereits ausgeführt wird.

Deny-by-default gilt durchgehend: Ist kein Approval-Gate verdrahtet, wird ein
Lauf verweigert und die Governance-Lücke als Finding gemeldet, statt ihn
stillschweigend zuzulassen.

## 4. Lauf beobachten

```bash
curl -sS "$OLIVARES/v1/m/orchestration/workflows/$ID/runs/$RUN" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT"
```

Jeder Schritt meldet seinen eigenen Zustand. Ein Schritt, dessen Upstream
fehlgeschlagen ist, wird `skipped` — der Lauf wird nach einem Fehler nie
fortgesetzt und meldet niemals einen Erfolg, den es nicht gab. Ein `wait`
zeigt, wann er fortgesetzt wird; ein `approval-gate` zeigt die Genehmigung, auf
die es wartet. Bei einem Emergency-Stop **friert** der gesamte Lauf mit einem
sichtbaren `paused_reason` ein und wird fortgesetzt, sobald der Stop aufgehoben
ist; ein Stop wird weder stillschweigend absorbiert noch lässt er den Lauf
sofort fehlschlagen.

Die Schritte schreiten über einen Hintergrundprozess fort. Wartezeiten und
Genehmigungen mitten im Graphen können daher vorankommen, ohne dass jemand
einen Request offen halten muss.

### Was das Ledger aufzeichnet

Jeder aktuierende Schritt hängt eine unveränderliche Zeile an, die dem Menschen
zugeschrieben wird, der den Lauf gestartet hat. Zwei Eigenschaften sind
wichtig:

- Auch ein **abgelehnter** Lauf wird aufgezeichnet. Ablehnungen sind Evidence.
- Trifft das Ergebnis einer Aktuierung ein, nachdem der Runner sie bereits
  aufgegeben hatte, wird das Ergebnis mit der tatsächlichen Dispatch-Referenz
  im Ledger **abgeglichen**. Der Schritt kann „Ergebnis unbekannt“ anzeigen —
  das Ledger behauptet jedoch niemals eine Aktuierung, die nicht stattgefunden
  hat, und verbirgt niemals eine, die stattgefunden hat.

## Bewusst außerhalb des Scopes

- **Automatische Trigger.** Ein Workflow läuft, wenn ein Mensch ihn genehmigt.
  Cron oder ein Ereignis mit dem Start eines Laufs zu verdrahten, fügt einen
  unbeaufsichtigten Aktuierungspfad hinzu und kommt in einer eigenen Änderung
  hinter der vorhandenen Schedule-Rail.
- **Beliebige Schritte mit Seiteneffekten** (HTTP, exec). Sie würden aus einer
  Kompositionsoberfläche eine allgemeine Ausführungs-Engine machen und die
  Eigenschaft aufheben, dass ein Workflow nur bereits governte Verben neu
  anordnen kann.

## Siehe auch

- [Govern und genehmigen](/de/how-to/govern-and-approve/) — die
  Approval-Engine, die der Lauf und das Gate mitten im Graphen durchlaufen.
- [Ereignisreferenz](/de/reference/events/) — `workflow.signal` und die
  Berechtigung, die ein Abonnent zum Empfang benötigt.
