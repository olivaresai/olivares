---
title: "Fehlerbehebung (Symptom → Diagnose → Behebung)"
description: >-
  Der Leitfaden des Betreibers zu Fehlermodi, destilliert aus den
  produkteigenen Runbooks: Probleme beim Start und ersten Lauf,
  Readiness-Fehler, Ingest-Backpressure, fehlgeschlagene
  Ledger-Verifizierungen und die Warnungen, die die Engine bewusst ausgibt.
---

Jeder Eintrag folgt demselben Aufbau: das Symptom, das Sie sehen, wie Sie bestätigen,
worum es sich handelt, und die Behebung. Die zitierten Log-Zeilen sind die tatsächlichen
Strings der Engine, sodass Sie danach greppen können. Wo ein tiefergehendes Runbook
existiert, verlinkt der Eintrag die relevante Seite, statt sie erneut herzuleiten.

## Erster Start und Setup

### Ich habe den Setup-Token verpasst

Ein Neustart gibt ihn **nicht** erneut aus (nur der Hash des Tokens wird gespeichert, in
`setup.token` im Datenverzeichnis). Solange noch keine Benutzer existieren, ist die
Wiederherstellung sicher: stoppen Sie die Engine, löschen Sie `setup.token`, starten Sie
sie — ein neuer Token wird erzeugt und ausgegeben. Das funktioniert *nur* bei einer
Installation ohne Benutzer, ist also kein Übernahmeweg. Der Token geht **nur an stdout**
(das Journal unter systemd, das Container-Log bei Docker/Kubernetes) — niemals in
Log-Dateien.

### `=== FIRST-BOOT SETUP ===` ist nie erschienen

In diesem Datenverzeichnis existieren bereits Benutzer — Sie befinden sich nicht im ersten
Start. Melden Sie sich entweder mit dem vorhandenen Administrator an oder verwenden Sie für
einen wirklich frischen Start ein frisches `--data-dir`.

### Die Engine warnt beim ersten Start vor Schlüsseln

```text
generated a new audit signing key; back it up path=/var/lib/olivares/audit-signing.key
generated a self-signed TLS certificate; clients must trust it, or pin it with --pin-sha256=<pin_sha256> (that value, verbatim) cert=/var/lib/olivares/tls.crt cert_fingerprint_sha256=d38567e8…378c4e7f pin_sha256=JsdrhrY77Me8miAmobJsqamE3NDWIOSBrDTwbHkyCD0
```

Beides ist beabsichtigt, und das erste ist das, was Sie später beißt: es gibt **kein
erzwungenes Escrow** — kopieren Sie `audit-signing.key` jetzt vom System weg und pinnen
Sie den öffentlichen Schlüssel (`GET /v1/audit/pubkey`) außerhalb des Systems, sonst lässt
eine künftige Host-Kompromittierung Sie unfähig zurück, Ihr eigenes Ledger zu beweisen
([Backup & Restore](/de/how-to/backup-and-restore/#die-zwei-schlüssel-die-alles-entscheiden)).

Die TLS-Zeile gibt **zwei** Digests aus, und sie sind nicht austauschbar:
`cert_fingerprint_sha256` ist der Zertifikats-Digest, der, den ein Browser
anzeigt; `pin_sha256` ist der SPKI-Digest des Leaf-Zertifikats, und nur diesen
vergleicht `--pin-sha256`. Kopieren Sie diesen Wert wortwörtlich:

```bash
olivares status --server https://127.0.0.1:8443 \
  --pin-sha256 JsdrhrY77Me8miAmobJsqamE3NDWIOSBrDTwbHkyCD0
```

Stattdessen den Zertifikats-Fingerprint zu pinnen scheitert nicht als ungültiger
Flag-Wert — er ist ein wohlgeformter 32-Byte-Digest, also wird die Verbindung
versucht und mit `TLS SPKI pin mismatch` abgelehnt, was den Wert nennt, den Sie
hätten verwenden sollen. Bei `curl --pinnedpubkey sha256//…` ergänzen Sie das
abschließende `=`-Padding: die Engine gibt absichtlich base64 ohne Padding aus,
damit der Wert im Log unquotiert erscheint und ein Copy-Paste übersteht — curl
verlangt jedoch die gepaddete Form.

## Quellen und die access map

### Die Karte ist leer

Prüfen Sie zuerst, ob überhaupt etwas verdrahtet ist. Die Engine sagt es beim Start
ausdrücklich:

```text
ingest: no observation sources configured (OLIVARES_SOURCES_CONFIG.sources is empty); no connector will ingest — the estate runs on no live traffic
```

Eine fehlende, unlesbare oder ungültige Quellendatei **warnt und fährt fort** (der Start
stürzt deswegen nie ab) — eine gesund aussehende Engine mit leerer Karte bedeutet also
meist, dass die Konfiguration nie geladen wurde. Korrigieren Sie Datei/Pfad und starten
Sie neu; Erfolg sieht aus wie `ingest: wired source … kind=…` pro Quelle. Eine Quelle, die
sich nicht konstruieren lässt, loggt `ingest: failed to register in-process source; not wired`
mit dem Grund — sie wird gemeldet, niemals stillschweigend verworfen.

### pgAudit ist verdrahtet, aber es kommen keine Kanten an

Drei Ursachen decken nahezu jeden Fall ab, alle by design
([der pgAudit-Leitfaden](/de/how-to/connectors/pgaudit/)):

1. **Der Server loggt nicht in UTC.** Datensätze mit einer Nicht-UTC-Zonenabkürzung
   werden **übersprungen**, statt falsch zeitgestempelt zu werden — setzen Sie
   `log_timezone = 'UTC'`.
2. **csvlog ist Batch, kein Tail.** `follow` gilt nur für `jsonlog`; eine csvlog-Quelle
   ingestiert bei jedem Durchlauf, nicht kontinuierlich.
3. **Die auditierten Klassen sind aus** — prüfen Sie, dass `pgaudit.log` `read, write`
   enthält.

### Alles erscheint als drift

Bei einer frischen Installation erwartet: ohne deklarierte Grants ist jeder beobachtete
Zugriff ehrlicherweise „unerwartet“. Das ist der Ausgangszustand, kein Bug —
[priorisieren Sie ihn](/de/how-to/cookbook/drift-triage/), indem Sie die Grants deklarieren,
die Sie beabsichtigen.

## Verfügbarkeit

### `/readyz` gibt 503 zurück

Lesen Sie den Body — er unterscheidet die beiden Fälle:

- `{"status":"unavailable","store":"down"}` — der Store ist nicht erreichbar. Bei
  SQLite: Festplatte voll, PVC-Probleme, Dateiberechtigungen. Bei Postgres:
  Erreichbarkeit und Anmeldedaten. **Liveness besteht bewusst weiter** (der Prozess
  lebt), sodass bei einem Store-Ausfall nichts in eine Restart-Schleife gerät; starten
  Sie Pod/Service nach Behebung des Stores manuell neu, falls er festgefahren bleibt.
- `{"status":"standby","leader":false,…}` — ein HA-Standby, der ehrlich antwortet. Kein
  Fehler: der Service routet zum Leader; Standbys drainen by design. Wenn **alle**
  Replikas Standby melden, ist die Leader-Wahl festgefahren — prüfen Sie die
  Konnektivität der Postgres-Advisory-Locks.

### Der Pod ist gestorben und nichts hat übernommen

In der **Standard-Topologie mit einer einzigen Replika** gibt es kein automatisches
Failover — die Wiederherstellung besteht aus dem Reschedule des StatefulSet plus dem
erneuten Anhängen des RWO-Volumes (achten Sie auf Multi-Attach-Fehler; das Volume pinnt
die Wiederherstellung an seine AZ). Automatisches Failover ist eine Eigenschaft der
[HA-Topologie](/de/tutorials/getting-started/kubernetes/#3-active-passive-ha)
(Postgres + Replikas + gemeinsamer Signierschlüssel). Betreiben Sie die Produktion
niemals mit deaktivierter Persistenz: ein `emptyDir` verliert den Signierschlüssel bei
jedem Reschedule.

## Performance

### Ingest-Latenz p99 steigt (Backpressure)

Der Bus **blockiert, statt zu verwerfen** — ein steigender
`olivares_ingest_duration_seconds`-p99 ist das vorgesehene Signal, dass ein Subscriber
gesättigt ist, kein Datenverlust. Benennen Sie den Verursacher direkt:

```promql
olivares_eventbus_queue_depth / olivares_eventbus_queue_capacity > 0.9
```

Die Per-Subscriber-Labels zeigen auf das langsame module;
`olivares_eventbus_publish_blocked_total` zählt die Backpressure-Ereignisse. Die übliche
Grundursache ist der **Schreibdurchsatz des Stores** (die SQLite-Single-Writer-Grenze) —
das ist eine Kapazitätsbehebung (Wechsel zu Postgres oder Reduzierung der
Schreibverstärkung), kein Tuning-Regler. Langsame Output-connectors (ein Webhook, ein
SIEM) dürfen niemals synchrone Subscriber sein.

Mit aktiviertem verteiltem Bus (`OLIVARES_BUS_CONFIG`) denken Sie daran, dass die
knotenübergreifende Bridge **at-most-once** ist: eine gesättigte Bridge füllt
`olivares_eventbus_bridge_pending_messages` und **verwirft dann Remote-Ereignisse**,
gezählt in `olivares_eventbus_bridge_dropped_total` — alarmieren Sie bei jedem Anstieg und
schlagen Sie Alarm, wenn `olivares_eventbus_bridge_connected == 0`.

### Anmeldungen schlagen mit „locked out“ fehl

Ein steigender `olivares_auth_login_attempts_total{outcome="locked_out"}` bedeutet, dass
die Drosselung pro Konto/pro IP nach wiederholten Fehlschlägen gegriffen hat. Sie löst
sich von selbst auf; untersuchen Sie die Ursache der Fehlschläge, statt die Limits
anzuheben.

## Nachweis

### Das Ledger besteht die Verifizierung nicht

Wissen Sie zunächst, was Sie ausgeführt haben: das standardmäßige `audit verify`
**beendet sich mit 0, selbst bei einer fehlgeschlagenen Kette** (das Ergebnis steht im
JSON-Bericht) — Automatisierung muss `--strict` verwenden oder den Bericht parsen:

```bash
olivares audit verify --tenant $TENANT --data-dir /var/lib/olivares --strict \
  --pubkey <BASE64-PINNED-OFF-BOX>
```

Pinnen Sie den **off-box** öffentlichen Schlüssel: ohne Pins vertraut der Verifizierer
den vom (möglicherweise kompromittierten) Host gelesenen Schlüsseln — in Ordnung als
beratende Prüfung, nicht als Manipulationsnachweis. Klassifizieren Sie dann anhand des
Feldes `reason`:

| Grund | Klasse | Reaktion |
|---|---|---|
| `hash-mismatch`, `prev-mismatch`, `head-mismatch`, `tail-truncated` | Manipulation oder Trunkierung | als SEV1 behandeln: das System sichern, gegen den off-box Checkpoint abgleichen |
| `checkpoint-sig-invalid`, `checkpoint-link-mismatch`, `event-sig-invalid` | Manipulation oder falscher Schlüssel | SEV1, es sei denn, Sie können eine Verwechslung der Schlüsselverwahrung beweisen |
| `seq-gap` | Löschung **oder** eine Restore-Inkonsistenz | gegen den off-box Checkpoint vergleichen, bevor Sie Manipulation rufen |
| `event-sig-missing` | möglicherweise Alt-Datensätze von vor Aktivierung der Signierung | mit `--from` an der Aktivierungsgrenze eingrenzen; ein Fehlen vor der Grenze ist erwartet |

Ein wiederhergestelltes Backup, das einen naiven Walk besteht, aber Ihrem gepinnten
off-box Checkpoint widerspricht, ist der Restore-Anomalie-Fall — genau dieser Vergleich
ist der Grund, warum der Pin existiert.

### `olivares_audit_checkpoint_age_seconds` wächst stetig

Es werden keine Checkpoints mehr geschrieben (Standardtakt 1h;
`OlivaresAuditCheckpointStale` feuert bei 2h). Prüfen Sie das Engine-Log auf
Checkpoint-Fehler und die Schreibbarkeit des Stores — während es wächst, altert Ihr
Anker für den Manipulationsnachweis.

## Benachrichtigungen und Sinks

### Ein Ziel empfängt nie etwas

Ein Ziel mit unbekannter Art wird **übersprungen und geloggt**
(`notify: destination has unknown connector kind; skipped` — prüfen Sie die Schreibweise
von `kind`). Für Eventing-Sinks sendet `POST …/subscriptions/{id}/test` eine Zustellung,
die Sie beobachten können, und Endpunkte müssen HTTPS sein
([Push an SIEM](/de/how-to/cookbook/push-to-siem/)).

---

Wenn ein Symptom hier nicht steht und die eigene Meldung der Engine es nicht erklärt, ist
das ein Dokumentationsfehler — bitte melden Sie ihn zusammen mit der Log-Zeile.
