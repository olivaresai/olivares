---
title: "FinOps-Zulassung reservieren, abrechnen und abgleichen"
description: >-
  Wie ein kostenpflichtiger Effekt Ausgaben reserviert und ein einziges Handle
  erhält, wie dieses Handle bestätigt oder freigegeben wird, wie sich
  Wiederholungen und späte Bestätigungen verhalten und wie der Abgleich der
  Zulassung zu lesen ist.
sidebar:
  order: 21
---

FinOps-Budgets und Ausgabenanalysen gehören zu **[Business](https://olivares.ai/pricing)**. Community behält die Kostenverfolgung pro Sitzung und den Datenexport. Vor 0.1 gespeicherte Budgets bleiben lesbar und löschbar und werden bei aktivem FinOps-Modul durchgesetzt; Community kann sie nicht erstellen oder ändern. Auswertungen und Testumgebungen bleiben in Community.


Ein kostenpflichtiger Effekt **reserviert** seine geschätzten Ausgaben, bevor er
läuft, und erhält **ein einziges Handle**. Ist der Effekt gelaufen, **bestätigt**
der Aufrufer (commit) die gemessenen Kosten mit diesem Handle. Lief er nicht,
**gibt** er die Vormerkung **frei**. Die eigenen Schranken der Engine (der
Inferenz-Proxy, der Sitzungsstart und geplante Jobs) tun das selbst. Diese Seite
richtet sich an einen Konnektor, der die Routen aufruft, und an den Betrieb, der
das Ergebnis liest.

## Reservieren

```bash
olivares finops admission reserve --data @reserve.json -o json
```

```json
{
  "scope": "model_gateway",
  "idempotency_key": "gateway/req-7f3a",
  "estimate_micro_usd": 2000000,
  "actor_ref": "alice",
  "dims": { "provider_ref": "anthropic", "model_ref": "claude-sonnet-4" }
}
```

Die Schätzung wird gegen jedes durchsetzende Budget vorgemerkt, das die Anfrage
erfasst, und bei gesetztem `actor_ref` gegen die Ausgabenlimits dieses Akteurs.
Die Antwort trägt das eine Handle:

```json
{
  "allowed": true,
  "handle": "0192f6c4-7d2e-7a31-9b7c-4d6f1e2a3b4c",
  "estimate_micro_usd": 2000000
}
```

Eine Schätzung von null merkt nichts vor und liefert kein Handle. Eine Obergrenze,
die ihr Limit bereits überschritten hat, lehnt sie trotzdem ab.

| Status | Bedeutung |
|---|---|
| 200 | Zugelassen. `handle` ist leer, wenn nichts vorgemerkt wurde. Mit `"unreachable": "allow"` wird eine Zulassung, die nicht hergestellt werden konnte, ohne Vormerkung zugelassen, mit dem Grund `admission could not be established; admitted without a hold (unreachable=allow)`. |
| 402 | Ein Block-Urteil: Ein Budget oder Ausgabenlimit mit `action=block` hat keinen Spielraum (`spend_limit` ist true, wenn ein Limit pro Platz abgelehnt hat), die Budgetmenge ist zu groß für eine Bewertung, oder der Mandant steht unter einer Aktivierungsgrenze des Versuchslebenszyklus oder deren Zustand ist nicht lesbar. Der Grund nennt den Fall. |
| 429 | Ein Budget mit `action=throttle` hat keinen Spielraum. |
| 503 | Die Zulassung konnte nicht hergestellt werden: Der Budgetspeicher war nicht lesbar, der Schlüssel blieb über die Wiederholungen hinaus belegt (ein laufender Anspruch eines anderen Aufrufers oder ein Vormerkungspaar, das ein früherer Build veröffentlichte und dessen verbleibende Vormerkung noch vormerkt), der Schlüssel hält einen Anspruch, den ein früherer Build hinterließ, oder die Liste geschuldeter Vormerkungen des Schlüssels lässt sich nicht dekodieren. Der Grund lautet `budget store unreachable (deny-closed)`. |
| 409 | Der Idempotenzschlüssel wurde mit anderem Inhalt verwendet. |
| 500 | Die Zulassungszeile des Schlüssels hat ihre Integritätsprüfung nicht bestanden. Der Schlüssel wird in jeder Haltung abgelehnt, bis die Zeile repariert ist. |
| 400 | Das Dokument ist nicht verwertbar: ein unbekannter Bereich, ein fehlender Schlüssel, eine negative Schätzung oder ein Feld, das das Schema nicht veröffentlicht. |

Ein 402 oder 429 wegen einer Obergrenze ist endgültig: Wiederholen Sie denselben
Effekt nicht vor einer neuen Periode oder einem höheren Limit. Ein 402 wegen
einer Aktivierungsgrenze gilt, solange der Versuchslebenszyklus das Buch des
Mandanten führt. Eine Ablehnung mit 402, 429, 503 oder 500 ist zugleich eine
Audit-Zeile `finops.admission.denied`.

Die Standardhaltung, wenn die Zulassung nicht hergestellt werden kann, ist
**ablehnen**. Setzen Sie `"unreachable": "allow"` im Reservierungsdokument, um
eine solche Anfrage ohne Vormerkung zuzulassen: Die Antwort ist 200 mit dem
obigen Grund, und das Engine-Protokoll hält sie auf ERROR fest, mit dem
Mandanten, dem Bereich, `posture=allow outcome=admitted` und der Fehlerklasse,
nie mit der eigenen Meldung des Speichers. Ein unbekannter Wert bedeutet
ablehnen.

## Wiederholungsregel

- **Reservierung.** Derselbe Idempotenzschlüssel mit demselben Inhalt erhält
  innerhalb des Wiederholungsfensters das Handle des ersten Aufrufs
  (`"replayed": true`). Das Fenster dauert fünf Minuten ab der Antwort, oder ab
  der Bestätigung, sobald die Vormerkung bestätigt ist. Außerhalb des Fensters
  und für einen Aufruf, der nichts vormerkte, wird die Anfrage neu bewertet.
- **Bestätigung.** Nach einer unsicheren Antwort wiederholen Sie `commit` mit
  demselben Handle und demselben Betrag so oft wie nötig. Jede Wiederholung endet
  in denselben Zeilen. Ein anderer Betrag nach einer Bestätigung wird mit **409**
  abgelehnt: Die ersten gemessenen Kosten gelten.
- **Freigabe.** Beliebig wiederholbar. Eine Freigabe macht nie eine Bestätigung
  rückgängig.

## Bestätigen und freigeben

```bash
olivares finops admission commit --data '{"handle":"0192f6c4-7d2e-7a31-9b7c-4d6f1e2a3b4c","actual_micro_usd":1500000}'
olivares finops admission release --data '{"handle":"0192f6c4-7d2e-7a31-9b7c-4d6f1e2a3b4c"}'
```

Erfassen Sie die gemessenen Kosten vor der Bestätigung, damit die Obergrenze nie
zu wenig zählt. Die Dokumente tragen `handle` und nichts anderes, das eine
Vormerkung benennt: Jedes andere Feld wird mit 400 abgelehnt. Ein leeres Handle
rechnet nichts ab.

| Status | Bedeutung |
|---|---|
| 200 | Abgerechnet, oder bereits auf dieselbe Weise abgerechnet. |
| 409 | Ein anderer Betrag nach einer Bestätigung; eine Vormerkung, deren Zulassung noch ein laufender Anspruch ist, sodass niemand dieses Handle erhielt; oder eine Vormerkung, deren Geld jetzt dem Versuchslebenszyklus gehört (`lifecycle_api_required`). |
| 500 | Die Zulassungszeile, die die Vormerkung benennt, hat ihre Integritätsprüfung nicht bestanden. Nichts wurde geschrieben: Behalten Sie die erfassten Kosten und wiederholen Sie denselben Aufruf, sobald die Zeile repariert ist. |
| 400 | Ein Handle, das keine Vormerkungsidentität ist, oder ein negativer Betrag. |

`committed: true` und `released: true` besagen, dass der Aufruf angenommen
wurde, nicht dass sich eine Zeile geändert hat: Die Bestätigung eines leeren
Handles rechnet nichts ab, und die Freigabe einer bestätigten Vormerkung lässt
sie bestätigt.

## Regel der späten Bestätigung

Eine Bestätigung hält fest, dass der Effekt lief, und wird daher angenommen, so
spät sie auch kommt: nach einer Freigabe, nach dem Ablauf der Vormerkung oder
nachdem ein anderer Aufruf den Schlüssel übernommen hat. Die Zeilen der
Vormerkung werden mit dem gemessenen Betrag bestätigt und behalten den Zeitpunkt,
an dem ihre Vormerkung endete. Keine Budgetobergrenze ändert sich, denn eine
freigegebene oder abgelaufene Zeile merkt nichts vor. Eine späte Bestätigung
einer abgelaufenen Vormerkung senkt `expired_unsettled` im nächsten Abgleich.

## Vormerkungen eines früheren Zulassungs-Builds

Eine Datenbank, auf der ein früherer Zulassungs-Build lief, kann Zeilen dieses
Builds enthalten. Ein Schlüssel, den er mit zwei Vormerkungen zuließ, wird mit
einer der beiden abgerechnet, und beide werden gemeinsam abgerechnet. Einen
Anspruch, den er laufend hinterließ, zieht die Wiederherstellung nur unter einem
Stoppzeitpunkt zurück, den der Betrieb angibt:

```bash
OLIVARES_FINOPS_ADMISSION_LEGACY_WRITERS_STOPPED_AT=2026-09-20T09:00:00Z
```

Setzen Sie ihn auf den Zeitpunkt, an dem jeder Schreiber des früheren Builds
stoppte, als RFC-3339-Zeit in UTC mit `Z` am Ende. Er wird einmal beim Start
gelesen. Die Wiederherstellung zieht einen solchen Anspruch erst zurück, wenn
seit diesem Zeitpunkt fünf Minuten vergangen sind, und nur solange keine Zeile
dieser Schreiber später datiert ist. Leer, der Standard, zieht keinen zurück.
Ein Text, der kein solcher Zeitpunkt ist, zieht keinen zurück und protokolliert
beim Start einen Fehler. Der Bericht zeigt den Zustand als `legacy_stop`:
`absent`, `invalid`, `future`, `contradicted`, `waiting` oder `usable`.

## Abgleich

Die Engine führt die Wiederherstellung jede Minute und den Abgleichsjob alle
fünf Minuten aus, für jeden aktiven Mandanten.

```bash
olivares finops admission reconciliation -o json
olivares finops admission reconcile -o json
```

`reconciliation` liest nur, mit Budget-Leserecht: Nichts wird wiederhergestellt,
bereinigt oder gemeldet. `reconcile` ist der Job, mit Budget-Schreibrecht: Er
führt die Wiederherstellung aus, bereinigt die unabgerechnet abgelaufenen
Vormerkungen und meldet einen Befund der Art `finops_reservation_drift`, wenn
`drift` true ist. Die Konsole zeigt denselben Bericht neben den Budgets.

| Feld | Bedeutung |
|---|---|
| `active`, `committed`, `released` | Buchzeilen in jedem Zustand |
| `expired_unsettled` | Zeilen, die niemand abrechnete; die TTL gab den Spielraum zurück |
| `active_lapsed` | Abgelaufene Zeilen, die der Job noch nicht bereinigt hat |
| `idempotency_orphans` | Reservierte Zulassungszeilen, deren Handle keine Buchzeilen hat |
| `owed_remaining` | Vormerkungen, die ein laufender Anspruch oder eine veröffentlichte Zeile noch schuldet |
| `legacy_pending`, `legacy_owes_release` | Ansprüche und Freigaben, die ein früherer Build hinterließ |
| `unresolved` | Schreibvorgänge der Wiederherstellung mit noch unbekanntem Ergebnis; der nächste Durchlauf entscheidet neu |
| `undecodable` | Zulassungszeilen, deren Liste geschuldeter Vormerkungen sich nicht dekodieren lässt |
| `frontier_blocked` | Vormerkungen, die die Wiederherstellung unter einer Aktivierungsgrenze weder abrechnen noch verwerfen kann |
| `corrupt` | Zulassungszeilen, die ihre Integritätsprüfung nicht bestehen; sie werden gezählt und nie geschrieben |
| `drift` | True, wenn `expired_unsettled`, `active_lapsed`, `idempotency_orphans`, `unresolved`, `undecodable` oder `corrupt` nicht null ist |

`unresolved` und `frontier_blocked` zählt nur `reconcile`, dessen
Wiederherstellungsdurchlauf sie füllt; `reconciliation` meldet sie als 0.

Für einen Mandanten bleibt der Wiederherstellung nichts zu tun, wenn die Zähler
von `owed_remaining` bis `corrupt` alle null sind. Behandeln Sie eine
`corrupt`-Zeile als Integritätsfehler des Speichers dieses Mandanten und
vergleichen Sie sie mit der letzten Sicherung. Die Vormerkungen, die sie benennt,
laufen mit ihrer TTL ab; nichts rechnet sie ab, bis die Zeile repariert ist.

## Bereiche

| Bereich | Aufrufer |
|---|---|
| `model_gateway` | Der Inferenz-Proxy und das Modell-Routing: reserviert vor dem Aufruf, bestätigt oder gibt danach frei |
| `session_launch` | Der Start betriebener Sitzungen und das Öffnen von Sprachsitzungen |
| `scheduled_job` | Orchestrierungsauslöser, Bewertungsrichter und MCP-Aufgaben |
