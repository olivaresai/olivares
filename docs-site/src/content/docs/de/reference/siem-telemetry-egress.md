---
title: SIEM- & Telemetrie-Egress
description: >-
  Jedes Wire-Format, das die Control Plane ausgibt — CEF, LEEF 2.0, RFC 5424
  Syslog, OTLP-Logs, OCSF 1.8.0, SARIF 2.1.0 —, das Severity-Mapping, auf das
  eine Regel abzielt, die Empfängerlimits je Transport und die zwei Stellen, an
  denen eine Projektion kein vollständiger Envelope ist.
---

Diese Seite ist der **Egress-Vertrag**: was die Control Plane verlässt, in
welchem Dialekt, über welchen Transport, und was ein Empfänger damit macht.
Geschrieben für die Person, die eine ArcSight-Regel, ein QRadar-DSM, eine
Sentinel-DCR oder einen Code-Scanning-Upload beim ersten Versuch zum Laufen
bringen muss.

Alles hier ist gegen die Spezifikationen der Hersteller geprüft, mit dem Datum
der Prüfung. Wo ein Hersteller etwas **nicht** festlegt, sagt die Seite das,
statt zu raten — solche Lücken sind als *vom Hersteller nicht definiert*
markiert, und der Encoder wählt jeweils die konservative Seite.

## Die zwei Feeds

Es gibt zwei unabhängige Quellen von Records, und sie teilen sich einen Encoder,
damit die Dialekte nicht auseinanderlaufen können:

| Feed | Inhalt | Pull | Push |
|---|---|---|---|
| **Audit-Ledger** | Das append-only, hash-verkettete Ledger mit seinen Integritätsfeldern (Sequenz, vorheriger Hash, Hash, Signatur) | `GET /v1/audit/export?format=…` (NDJSON, ein Record pro Zeile) | Der Ledger-Forwarder, über jeden Output-Connector |
| **Notifications & Findings** | Governance-Findings, Policy-Entscheidungen, Health- und Lifecycle-Events | — | Jeder Output-Connector |

Die Integritätsfelder des Ledgers reisen in **jedem** Format wortgetreu mit, damit
ein SOC die Kette aus der Kopie im eigenen SIEM nachprüfen kann und nicht nur aus
dem Produkt.

## Formate

| Format | Standard | Fixierte Version | Wo wählbar |
|---|---|---|---|
| CEF | ArcSight Common Event Format | V27 (Juli 2024) | Ledger-Export, Connectors |
| LEEF | IBM QRadar Log Event Extended Format | 2.0 | Ledger-Export, Connectors |
| Syslog | RFC 5424 (+ RFC 5426 UDP, RFC 6587 TCP-Framing, RFC 5425 TLS) | — | Ledger-Export, Connectors |
| OTLP-Request (`otlp`) | OTLP/HTTP-JSON-Export-Request (`ExportLogsServiceRequest`) | siehe *Projektionen* unten | Ledger-Export, Connectors |
| OTLP-Request (`otlp_envelope`) | Exaktes Byte-für-Byte-Alias von `otlp` | siehe *Projektionen* unten | Ledger-Export, Connectors |
| OTLP-LogRecord (`otlp_log_record`) | OpenTelemetry Logs, ein LogRecord pro Zeile | siehe *Projektionen* unten | Ledger-Export |
| OCSF | Open Cybersecurity Schema Framework, Profil `ai_operation` | 1.8.0 | Ledger-Export, Connectors |
| ASIM | Microsoft Sentinel Advanced SIEM Information Model | — | Connectors |
| ECS | Elastic Common Schema | 9.4.0 | Elastic-Connector |
| UDM | Google SecOps Unified Data Model | — | Chronicle-Connector |
| SARIF | OASIS Static Analysis Results Interchange Format | 2.1.0 Errata 01 | Findings-Export |

Jede Auswahloberfläche akzeptiert ihre eigene geordnete Teilmenge dieser Tokens,
abgeleitet aus einem gemeinsamen Katalog, damit die Listen nicht wieder
auseinanderlaufen können:

| Oberfläche | Akzeptierte Tokens | Default |
|---|---|---|
| Ledger-Export (`GET /v1/audit/export?format=…`) | `cef\|leef\|syslog\|otlp\|otlp_envelope\|otlp_log_record\|ocsf` | `cef` |
| Eventing-Sink (`sink_format` einer Push-Subscription) | `ocsf\|cef\|leef\|syslog\|otlp\|otlp_envelope\|json` | `ocsf` |
| Notification-Connectors (`filelog`, `splunkhec`, `s3archive`, `siem`) | `json\|cef\|leef\|syslog\|otlp\|otlp_envelope\|ocsf\|asim` | `json` |
| Syslog-Connector | `syslog\|cef\|leef` | `syslog` |

Der Ledger-Export hat keinen Roh-JSON-Passthrough — seine JSON-Formen sind die
OTLP-Formen oben. `json` bedeutet zwei verschiedene Lieferungen: der
Eventing-Sink postet den rohen erfassten Event-Envelope (der strukturierte
Passthrough, ohne Dialekt-Transformation), während die Notification-Connectors
nur eine minimale Notification-Projektion rendern — die anzeigbaren Felder,
nicht die ursprüngliche Payload. Alle vier Notification-Connectors akzeptieren
`asim`, `s3archive` eingeschlossen. Ein Format außerhalb der Liste seiner
Oberfläche wird abgelehnt: ein Tippfehler beim Authoring oder in der
Konfiguration erhält einen Fehler, der die akzeptierten Tokens der Oberfläche
nennt; ein korrumpierter gespeicherter Wert wird erst beim Encodieren
zurückgewiesen (genannt wird die korrupte Schreibweise, nicht die Liste); nichts
fällt still auf JSON zurück.

## Severity: die eine Quelle der Wahrheit

Jede Regel, die auf Severity filtert, hängt an dieser Tabelle. Ein Mapping an
einer Stelle — so können die CEF-Zahl, die Syslog-Priorität und die
OTLP-Severity desselben Events einander nie widersprechen.

| Produkt-Severity | CEF (0-10) | Syslog (0-7) | OTLP | ECS | UDM |
|---|---|---|---|---|---|
| info | 1 | 6 (info) | INFO | 1 | INFORMATIONAL |
| low | 3 | 5 (notice) | INFO2 | 3 | LOW |
| medium | 5 | 4 (warning) | WARN | 5 | MEDIUM |
| high | 7 | 3 (error) | ERROR | 7 | HIGH |
| critical | 10 | 2 (critical) | FATAL | 10 | CRITICAL |
| unbestimmt | 0 (Unknown) | 6 (info) | UNSPECIFIED | 0 | *(entfällt)* |

Zwei Eigenschaften sichern Tests ab, weil beide leicht versehentlich verloren
gehen:

- **Die fünf bestimmten Severities teilen sich nie eine Zahl.** Ein
  Collector-Selektor wie `local0.notice`, eine ArcSight-Regel oder eine
  Sentinel-DCR filtert auf die ausgegebene Zahl, und der RFC-5424-Frame trägt
  kein weiteres Severity-Signal — zwei Severities auf einer Priorität würden eine
  Unterscheidung still und unwiederbringlich vernichten.
- **Eine unbestimmte Severity wird nicht erfunden.** CEF V27 hat die `0` von
  *Low* zu *Unknown* umbenannt, und genau das bekommt ein Event ohne bestimmte
  Severity. (LEEF ist die Ausnahme: sein Bereich ist 1-10 ohne Wert für
  „unbekannt", daher greift dort die Untergrenze. Siehe unten.)

:::note[Warum die Syslog-Spalte so aussieht]
Weder CEF noch RFC 5424 definieren ein Mapping von CEF-Severity auf
Syslog-Priorität — gegen beide Spezifikationen geprüft am 2026-07-24. Die
Syslog-Spalte ist deshalb **Produktpolitik**, so gewählt, dass jede Severity
unterscheidbar bleibt und „critical" auf der Priorität landet, die RFC 5424
selbst *critical* nennt. Das einzige existierende Hersteller-Mapping (eine
konfigurierbare Einstellung eines ArcSight-Connectors) legt sein höchstes Band
ebenfalls auf `crit`. Wer sich auf eine andere Bandbreite standardisiert hat,
mappt sie am Collector — diese Zahlen bewegen sich nicht ohne einen
`Changed`-Eintrag im Changelog.
:::

## CEF im Detail

- **Header-Größen** sind auf die V27-Maxima begrenzt: Device Vendor 63, Device
  Product 63, Device Version 31, Event Class Id 1023, Name 512.
- Die Spezifikation nennt diese Zahlen, sagt aber nie, ob sie **Zeichen oder
  Wire-Oktette** zählen, und definiert kein Verhalten für ein zu langes Feld
  (*vom Hersteller nicht definiert*, geprüft am 2026-07-24). Deshalb werden beide
  Lesarten eingehalten: ein Wert wird auf die Zahl in dekodierten Zeichen **und**
  in UTF-8-Oktetten auf dem Wire begrenzt. Ein nicht-ASCII-Gerätename oder
  Event-Name passt also in weniger Zeichen, als die Zahl vermuten lässt — die
  konservative Richtung.
- Gekürzt wird **nur der Header**. Die Extension, die den auditierbaren Inhalt
  trägt, wird nie gekürzt.
- Zeitwertige Extension-Keys (`rt`, `start`, `end`) sind dezimale
  **Epoch-Millisekunden**, wie es das CEF-Dictionary verlangt.

## LEEF im Detail

- `sev` ist ein Integer im von LEEF 2.0 dokumentierten Bereich **1-10**. Ein
  Event, dessen Severity nie bestimmt wurde, geht als `sev=1` raus: LEEF hat
  keinen Wert für „unbekannt", und `sev=0` liegt außerhalb des Bereichs.
- `devTime` ist ein **13-stelliger Epoch-Wert**, den QRadar ohne `devTimeFormat`
  akzeptiert. Für ein Event ohne erfasste Zeit wird es **weggelassen** — nie
  erfunden —, und QRadar fällt dann dokumentiert auf die Empfangszeit zurück.
- `sev`, `devTime` und `devTimeFormat` **gehören dem Encoder**. Trägt ein Event
  ein Feld mit einem dieser Namen (in beliebiger Schreibweise), wird es als
  `olvSev` / `olvDevTime` / `olvDevTimeFormat` umbenannt ausgegeben: der Wert
  erreicht Sie weiterhin, kann aber weder die normalisierte Severity überschreiben
  noch das Event umdatieren. IBM dokumentiert, dass ein erkanntes `devTime` den
  Syslog-Zeitstempel übersteuert — deshalb bleibt das nicht dem Zufall überlassen.

:::caution[Von IBM nicht definiert]
IBM dokumentiert nicht, was QRadar mit `sev=0`, mit einem unparsbaren `devTime`
oder mit der Groß-/Kleinschreibung von Attributschlüsseln macht (geprüft am
2026-07-24). Das Obige ist jeweils die konservative Lesart. Wer Empfänger-Evidenz
für das Gegenteil hat: bitte ein Issue.
:::

## Syslog-Transport und Empfängerlimits

Der Syslog-Connector trägt einen nativen RFC-5424-Record oder einen CEF-/
LEEF-Record als MSG eines spezifikationskonformen RFC-5424-Frames — genau so
nehmen ArcSight und QRadar diese Formate über Syslog auf.

- **TLS auf 6514 (RFC 5425) ist der Default**, mit Octet-Counting-Framing wie vom
  RFC gefordert. Klartext-TCP oder -UDP ist ein ausdrückliches Opt-out des
  Betreibers; kein Codepfad stuft ein TLS-Ziel auf Klartext herunter.
- **Empfänger-Payload-Budget** (`max_payload_bytes`, Default `0` = aus). Ein
  Empfänger, der einen zu großen Record splittet, macht aus einem auditierbaren
  Event zwei unparsbare Hälften. Wenn Sie das Budget des Ziels deklarieren, das
  Sie betreiben, **scheitert** ein Record darüber an der Zustellung — mit Retry
  und danach im DLQ, wo Sie ihn sehen — statt zum Splitten verschickt zu werden.
  Der Record selbst wird nie gekürzt.

Referenzwerte für diese Einstellung, mit dem, was die Quelle tatsächlich sagt
(geprüft am 2026-07-24):

| Empfänger | Bytes | Was die Quelle sagt |
|---|---|---|
| Jeder RFC-5424-Empfänger | 480 | Das Minimum, das ein Empfänger unterstützen **MUSS** (§6.1) |
| Jeder RFC-5424-Empfänger | 2048 | Die Größe, die Implementierungen unterstützen **SOLLTEN** |
| ArcSight-Syslog-Daemon | 1024 | Die Guides sagen, eine längere Nachricht **„might be split"** — ein Deployment-Hinweis, keine Empfängerregel, und nicht auf den Datei- oder Pipe-Pfad anwendbar |
| QRadar TCP | 4096 | Die **Standard**-Maximal-Payload; erhöhbar (IBM dokumentiert 8192, Obergrenze 32000) |

Keine dieser Quellen definiert, ob der Header mitzählt — deshalb wird das Budget
am **vollständigen Record** in UTF-8-Oktetten gemessen.

## OCSF

Events werden als OCSF **1.8.0** mit dem Profil `ai_operation` ausgegeben, in den
drei Klassen, die es registrieren: API Activity (6003, der Default), Process
Activity (1007) und Datastore Activity (6005). Die Ausgabe wird in der Testsuite
gegen die offiziellen 1.8.0-Klassenschemata validiert, die unbekannte Felder
verbieten — ein profilfremdes Attribut oder ein unvollständiges Profilobjekt
bricht damit den Build, statt bei Ihnen anzukommen.

:::caution[AWS Security Lake akzeptiert OCSF ≤ 1.3]
Eine Security-Lake-Custom-Source ist auf **OCSF 1.3 in Parquet** begrenzt, daher
landen 1.8.0-`ai_operation`-Events dort nicht unverändert (geprüft am
2026-07-24). Bis ein 1.3-Downgrade-Emitter existiert, routen Sie über eine eigene
Transformation oder nutzen ein anderes Ziel. Das ist eine deklarierte Lücke, kein
Versehen.
:::

## Projektionen, die keine Envelopes sind

Zwei ehrliche Einschränkungen, beide wichtig, bevor Sie einen Collector
daraufrichten:

- **`otlp` ist auf jeder Oberfläche der sendefähige Request; `otlp_log_record` ist
  die nackte Projektion.** Seit dem Format-Katalog-Remap ist eine
  `otlp`-EREIGNIS-Zeile überall dort, wo das Token akzeptiert wird — Ledger-Export,
  Output-Connectors, Eventing-Push —, ein vollständiger OTLP/HTTP-JSON-Export-Request
  (`ExportLogsServiceRequest`), mit der Resource-Identität und dem Instrumentation
  Scope, den ein Collector braucht. `otlp_envelope` ist auf jeder Oberfläche ein
  exaktes Byte-für-Byte-Alias von `otlp`, beibehalten, weil diese Schreibweise den
  Envelope zuerst ausgeliefert hat — die beiden unterscheiden sich nie. Die
  Projektion mit einem LogRecord pro Zeile — ein JSON-Objekt pro Zeile, für Datei-
  und NDJSON-Konsum — existiert weiter, unter ihrem ehrlichen Namen
  `otlp_log_record` und nur im Pull-Export des Ledgers: eine einzelne LogRecord-Zeile
  ist kein sendefähiger `/v1/logs`-Body, also bieten die Push-Oberflächen sie bewusst
  nicht an. Drei Details, weil sie sonst einen Nachmittag kosten: die LETZTE Zeile
  des Pull-Exports ist Olivares' `{"export_complete":true,…}`-Marker und **kein**
  Request — eine Schleife, die jede Zeile postet, muss sie überspringen, und zwar
  STRUKTURELL, z. B. `jq -c 'select(has("resourceLogs"))'`, nie per Substring: ein
  Ereignis, dessen Akteur oder Ziel `export_complete` enthält, fiele einem `grep -v`
  zum Opfer — das wäre gelöschte Evidenz, kein übersprungener Marker; ein Push-Sink
  muss auf die exakte `/v1/logs`-URL des Collectors zeigen, da der Endpunkt
  wortgetreu gepostet wird; und der generische HTTPS-Sink meldet jedes 2xx als
  zugestellt, ohne die Partial-Success-Antwort des Collectors zu lesen — der
  dedizierte **OTLP-Logs-Connector** liest sie. `otlp_log_record` behält die exakten
  Bytes, die das Token `otlp` vor dem Remap erzeugte, im normalen
  Zeitstempel-Bereich — die Nullzeit und jeder Zeitpunkt von der Epoche bis
  `2262-04-11T23:47:16.854775807Z`. Außerhalb ist Byte-Kompatibilität NICHT
  garantiert, und wo die Bytes abweichen, ist das eine Korrektur: ein Datum vor der
  Epoche wurde vorher zu einem negativen Wert in einem Feld, das OTLP als unsigned
  deklariert; ein Datum zwischen der signed- und der unsigned-Obergrenze trägt jetzt
  seinen echten unsigned-Wert; und ein Datum nach `2554-07-21T23:34:33.709551615Z`
  wird jetzt als unbekannt (`0`) kodiert statt als übergelaufener Wert — darunter
  die kleinen positiven, die als Anfang 1970 gelesen werden. Bei einzelnen Eingaben,
  die auf null überlaufen, stimmen alte und neue Bytes überein. Zwei
  Upgrade-Hinweise, klar gesagt: die Pull-*Datei* bleibt NDJSON (ein Request pro
  Zeile plus Abschlussmarker), nicht ein Request; und eine gespeicherte
  Eventing-Subscription, deren Format vor dem Remap exakt `otlp` buchstabiert war,
  liefert jetzt den Envelope, wo sie früher eine einzelne Zeile lieferte — die
  Engine loggt eine strukturierte Warnung pro solcher Subscription, und
  Audit-Metadaten von vor dem Remap lesen sich mit der alten Bedeutung des Tokens.
- **Die Trace-Extension der OWASP Agentic AI Security** liegt im
  `unmapped`-Container von OCSF — die Platzierung, die ihre Spezifikation (v0.1,
  Public Preview) vorschreibt. Sie ist kein erstklassiger OCSF-Attributsatz, und
  die Schemavalidierung deckt nur ihre Platzierung ab.

## Findings als SARIF

Governance-Findings werden als **SARIF 2.1.0 Errata 01** für einen
Code-Scanning-Konsumenten exportiert:

- `GET /v1/m/security/findings/export?format=sarif` — dieselben Filter wie die
  Findings-Liste, mit einer Ergebnisobergrenze und einem ehrlichen
  Truncation-Header, wenn sie erreicht wird.
- `olivares findings export` — derselbe Export über die CLI, atomar geschrieben
  mit `0600`-Rechten.

Der Run deklariert die URI-Basis, gegen die seine Result-Locations auflösen, führt
pro Finding einen stabilen `partialFingerprints.primaryLocationLineHash` mit,
damit ein Konsument dedupliziert statt neu zu alarmieren, und weigert sich, ein
Result mit leerer Rule-Id oder einem Level außerhalb des Enums auszugeben — das
sind die beiden Dinge, für die ein Konsument die ganze Datei ablehnt, und das beim
Upload zu erfahren ist schlechter als hier.

Findings, deren Subjekt keine versionierte Datei ist, bekommen eine synthetische
Location-URI. Der Run bleibt gültig und ingestierbar, aber GitHub rendert Alerts
nur für URIs, die zu einer Datei im Checkout passen — ein Detektor, der
GitHub-Anchoring will, sollte die Artefakt-URI explizit setzen.
