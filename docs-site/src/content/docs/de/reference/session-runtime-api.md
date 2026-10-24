---
title: Session-Runtime-API (offizielle CLIs)
description: >-
  Community-HTTP-Oberfläche zum Auflisten, Anhängen, Speisen und Stoppen eigener
  Claude-Code-, Codex- und Grok-CLI-Prozesse. Berechtigungen, Stdio-E/A, Resume und Reconnect.
---

Die Control Plane **startet die Anbieter-CLI**. Sie ersetzt Claude Code, Codex
oder Grok Build nicht. Sitzungen und Terminals sind ein Modul dieses Produkts,
nicht das Produkt.

Diese Seite dokumentiert die Community-Operate-Routen unter
`/v1/m/sessions/runs`. Sie stehen bereits im
[beta-OpenAPI-Dokument](/reference/api-beta/). 26.10 ergänzt den
Driver-Vertrag, das Transport-Verfahren, das jede Start-Form braucht, und die
Journeys J01–J08 als Tests.

## Editionsgrenze

| Edition | Was sie tut | Was sie nicht tut |
|---|---|---|
| **Community (diese Seite)** | Eigenes lokales Kind: Start, stdin/stdout/stderr, Attach mit Cursor, Resume der exakten Konversation, Reconnect eines lebenden Streams, Stop mit beobachtetem Exit-Status. Sitzungszeilen und Evidenz bleiben in Modul II. | Identity-&-Scale-Engine mit mehreren Panes, mTLS-Listener, Cockpit-Pane-Eingabe, xterm-UI-Chunk |
| **Identity-&-Scale-Overlay** | Kommerzielle session-cockpit-Engine (Listener, Panes, Aufzeichnungs-Ledger). Routen unter `/v1/m/session-cockpit/`, wenn das Modul vorhanden ist. | Ersetzt `/v1/m/sessions/runs` nicht |

Ein Community-Build beantwortet den Overlay-Namespace durch **Abwesenheit**
(404). Es gibt keinen 501-Stub.

Der Claude-Code-Hook-Pfad bleibt `olivares claude-hook` (PreToolUse-PEP). Das ist
Beobachtung und Durchsetzung, keine Ersatz-CLI.

## Berechtigungen

Bestehende Durchsetzung auf den Modulrouten:

| Berechtigung | Routen |
|---|---|
| `sessions:run:read` | `GET /runs`, `GET /runs/{ref}`, `GET /runs/{ref}/events`, `GET /runs/{ref}/attach`, `GET /runs/{ref}/handoff`, `GET /runs/{ref}/activity`, `GET /runs/{ref}/activity/agents/{agent}`, `GET /runs/{ref}/messages`, `POST /runs/{ref}/pin`, `DELETE /runs/{ref}/pin` |
| `sessions:run:write` | `POST /runs`, `POST /runs/{ref}/input`, `POST /runs/{ref}/interrupt`, `POST /runs/{ref}/stop`, `POST /runs/{ref}/resume`, `POST /runs/{ref}/move`, `POST /runs/{ref}/handoff`, `PUT /runs/{ref}/name`, `POST /runs/{ref}/archive`, `DELETE /runs/{ref}/archive` |
| `sessions:run:admin` | `POST /runs/{ref}/cleanup`, `DELETE /runs/{ref}` |

Ein Viewer darf Sitzungen für sich selbst listen und anheften. Create, Input und Stop brauchen write. Der Authorizer
der Route ist der Durchsetzungspunkt; eine fehlende Berechtigung ist 403.

## Routen, die die Konsole liest

Basis: `/v1/m/sessions`. Authentifizieren. `X-Olivares-Tenant` senden.

| Methode | Pfad | Ergebnis |
|---|---|---|
| `GET` | `/runs` | Seite verwalteter Läufe. Optional `state`, `live_ref`, `claude_session_id` (legacy) |
| `GET` | `/runs/{ref}` | Ein Lauf. `state` ist abgeleitet. `exit_code` ist beobachtet. Kein erfundenes Success-Feld. `archived_at` (RFC 3339, UTC) ist vorhanden, solange die Sitzung archiviert ist |
| `GET` | `/runs/{ref}/events` | Lebenszyklus-Evidenzzeilen |
| `GET` | `/runs/{ref}/attach?from={seq}` | SSE: `output`-Rahmen, `lag`, wenn der Ring unter dem Cursor verdrängt hat, `end` oder eine not-live-`notice`. Eine Grok-, OpenCode- oder Gemini-CLI-Sitzung, die nach einem Start eines Laufs mit Vault-Secrets, einem Repository-Credential, einem verwalteten Credential oder einem Package-Registry-Token fortgesetzt wird, streamt nicht die Konversation, die ihr Tool beim Resume wiedergibt, und meldet dies in einem einzigen `olivares_notice`-Rahmen (`history_withheld`) |
| `POST` | `/runs/{ref}/input` | stdin. Stream-json nutzt `line`/`message`. Codex/Grok nutzen `text`. 202 `{accepted:true}` |
| `POST` | `/runs/{ref}/stop` | SIGTERM, dann SIGKILL der Prozessgruppe. Beobachteter Exit-Status auf der Zeile |
| `POST` | `/runs/{ref}/resume` | Neue Prozessgeneration. Exakte gespeicherte Konversation. Kein stiller Fallback auf eine neue Konversation |
| `POST` | `/runs/{ref}/interrupt` | Aktiven Turn abbrechen. Der Prozess bleibt |
| `POST`, `DELETE` | `/runs/{ref}/pin` | Sitzung für den Aufrufer anheften oder lösen. Der Pin gehört dem Aufrufer und folgt ihm in einen anderen Browser oder auf eine andere Maschine; er ändert nie die Liste einer anderen Person. Beide Aufrufe sind idempotent und antworten 200 `{run_ref, pinned, pinned_at}`. Eine Person behält höchstens 20 Pins: Beim Anheften eines 21. verschwindet der älteste. Das Freigeben oder Löschen einer Sitzung entfernt jeden Pin darauf. `GET /runs` und `GET /runs/{ref}` tragen das eigene `pinned` und `pinned_at` des Aufrufers; `GET /runs` liefert außerdem die angehefteten Sitzungen des Aufrufers, die außerhalb seiner Seite liegen (höchstens 20 Zeilen über `limit` hinaus; `pagination=cursor` fügt keine hinzu, da es jede Sitzung ohnehin einmal erreicht). Ein Pin gehört zu dem Credential, das ihn gesetzt hat: Die Konsole einer Person und ihre `olivares login`-Sitzungen teilen sich die Pins, ein API-Token behält seine eigenen. Eine freigegebene Sitzung antwortet 409 |
| `POST`, `DELETE` | `/runs/{ref}/snooze` | Sitzung für den Aufrufer bis zu einem Zeitpunkt zurückstellen oder die Zurückstellung beenden. `POST` nimmt `{until}`, eine RFC-3339-Zeit in der Zukunft (sonst 400); ein zweites `POST` verschiebt sie. Die Zurückstellung gehört dem Aufrufer, wie der Pin: Die Konsole nimmt die Sitzung bis `until` aus dessen Liste heraus, und keine andere Liste ändert sich. Beide Aufrufe antworten 200 `{run_ref, snoozed_until?, done}`. `GET /runs` und `GET /runs/{ref}` tragen das `snoozed_until` des Aufrufers nur, solange die Zurückstellung läuft. Das Freigeben oder Löschen einer Sitzung entfernt jede Zurückstellung darauf. Eine freigegebene Sitzung antwortet 409 |
| `POST`, `DELETE` | `/runs/{ref}/done` | Sitzung für den Aufrufer als erledigt markieren oder die Markierung entfernen. Die Konsole legt eine erledigte Sitzung in der Liste dieser Person unter Früher ab. Beide Aufrufe sind idempotent und antworten 200 `{run_ref, snoozed_until?, done}`. `GET /runs` und `GET /runs/{ref}` tragen das `done` des Aufrufers. Das Freigeben oder Löschen einer Sitzung entfernt jede Markierung darauf. Eine freigegebene Sitzung antwortet 409 |
| `PUT` | `/runs/{ref}/name` | Body `{name}`. Benennt die Sitzung um. Der Name wird getrimmt; ein leerer Name löscht ihn, und Clients greifen auf ihre eigene Benennung zurück. Mehr als 200 Zeichen, ein Steuer- oder unsichtbares Zeichen (Format-, Zeilen- oder Absatztrenner: Leerzeichen ohne Breite, Richtungsüberschreibungen) oder ein fehlendes `name` ergeben 400. 200 Lauf. Der Name gehört der Sitzung, daher sieht ihn jeder Leser. Im Audit-Ledger als `sessions.run.renamed` mit der Lauf-Referenz, nicht dem Namen, aufgezeichnet; ein Name, den die Sitzung schon trägt, ändert nichts und zeichnet nichts auf |
| `POST`, `DELETE` | `/runs/{ref}/archive` | Archiviert die Sitzung oder hebt die Archivierung auf. Kein Body. 200 Lauf, mit `archived_at`, solange sie archiviert ist. Archivieren stoppt die Sitzung nicht und entfernt sie nicht. Die Markierung gehört der Sitzung, nicht dem Aufrufer: Sie ist für alle archiviert. `GET /runs` wird dadurch nicht eingeschränkt; jeder Client lässt archivierte Sitzungen aus seiner Liste weg. Beide Aufrufe sind idempotent: Das Archivieren einer archivierten Sitzung behält ihr `archived_at` und zeichnet nichts auf. Im Audit-Ledger als `sessions.run.archived` und `sessions.run.unarchived` aufgezeichnet |
| `POST` | `/runs/{ref}/move` | Body `{provider_profile_ref}`. Bindet eine gestoppte oder fehlgeschlagene Claude-Code-Sitzung an ein anderes Profil desselben Tools um und überträgt ihre Konversation. Startet nichts. 200 Lauf |
| `GET` | `/runs/{ref}/handoff` | Die Zusammenfassung, mit der ein Handoff beginnen würde: `summary`, `source_driver`, `turns`, `transcript_bytes`. Schreibgeschützt, im Audit-Ledger aufgezeichnet. Eine laufende Sitzung wird so gelesen, wie sie ist; eine, die noch nicht gestartet ist, antwortet 409 |
| `POST` | `/runs/{ref}/handoff` | Body `{provider_profile_ref, summary?, name?}`. Startet in diesem Profil eine neue Sitzung, die mit dieser verknüpft ist. 201 Lauf plus `summary_state` (und `first_turn`, wenn dieser nicht `delivered` ist); 202, solange der Start auf Genehmigung wartet |
| `GET` | `/runs/{ref}/activity` | Was das Tool einer Claude-Code- oder Codex-Sitzung gerade tut, bei jeder Anfrage aus seiner eigenen nativen Aufzeichnung gelesen: `agents`, `tasks` (Befehle und Hintergrundarbeit), `files` (Pfad, `added`, `removed`, `created`), `todos`, `skills`, `mcp_servers`, `model`, `context_tokens`, das nullbare `context_window`, wenn das Tool es meldet, und `context_percent` (Prozentwert für Claude Code, null, wenn unbekannt), `compactions` und `transcript` (`read`, `pending`, bevor das Tool es geschrieben hat, `missing`, wenn es vorhanden sein sollte). Agents und Tasks sind `running`, `completed`, `failed`, `stopped` oder `scheduled`. Codex verwendet seine Rollout-Datei, ohne eine weitere App-Server-Instanz zu starten; Kontext-Tokens beschreiben die letzte Anfrage, nicht die kumulierte Nutzung. Schreibgeschützt; einmal je Leser und Sitzung alle 15 Minuten im Audit-Ledger aufgezeichnet. Ein nicht unterstütztes Tool antwortet 422; eine Sitzung, der Vault-Secrets, ein Repository-Credential oder ein verwaltetes Credential gegeben wurden, antwortet 409, ebenso eine Sitzung, die ein Package-Registry-Token verwendet hat, wenn sie gestoppt ist oder nach einem Resume läuft, das auf einen Start folgt, der eines verwendet hat |
| `GET` | `/runs/{ref}/activity/agents/{agent}` | Das Transkript eines Claude-Code-Subagents in einfachen Turns (`role`, `text`). Schreibgeschützt, im Audit-Ledger aufgezeichnet. 404 für einen unbekannten Agent. Codex meldet den Zustand von Kindern in der Activity-Antwort der Wurzel; Lesen von Kind-Transkripten wird nicht angeboten |
| `GET` | `/runs/{ref}/messages` | Die Nachrichten einer Claude-Code- oder Codex-Sitzung, bei jeder Anfrage aus dem eigenen Transkript des Tools gelesen, seitenweise: `items` (`id`, `role` `user` oder `assistant`, `at`, `first_line`), `total`, `next`, `transcript` (`read`, `pending`, `missing`). Standardmäßig nur, was die Person gesendet hat; `role=all` fügt die Antworten des Tools hinzu. `q` behält Nachrichten, die diese Phrase irgendwo enthalten (ohne Beachtung der Groß-/Kleinschreibung, höchstens 256 Bytes); `at` (RFC 3339) behält Nachrichten zu oder nach diesem Zeitpunkt, daher ist das erste Element der Anker; `order` ist `asc` (Standard) oder `desc`, `limit` höchstens 200 und `cursor` die ID, ab der eine Seite fortsetzt (`next`); die Elemente sind immer vom ältesten zum neuesten sortiert. `id` liest eine Nachricht vollständig (`text` und `later_same`, wie viele spätere Nachrichten desselben Absenders ihre erste Zeile wiederholen; kein Filter gilt, und `total` ist die Nachrichtenzahl der Sitzung). `text=1` fügt jedem Eintrag der Seite `text` hinzu, auf 64 KiB je Nachricht begrenzt wie ein Lesen per `id`, und kein `later_same`; ein an dieser Grenze gekürzter Text endet auf ` [...]`, und sein Eintrag trägt `clipped: true`, auch beim Lesen per `id`; eine solche Seite enthält höchstens 50 Nachrichten; folgen Sie `next`, um eine ganze Konversation zu kopieren. Die `id` einer Nachricht ist ihre Position unter den Nachrichten der Sitzung und ändert sich nicht, wenn das Transkript wächst. Schreibgeschützt; einmal je Leser, Sitzung und 15-Minuten-Fenster im Audit-Ledger aufgezeichnet, als `sessions.run.messages`, ein Lesen mit `text=1` als `sessions.run.messages.text`. Ein nicht unterstütztes Tool antwortet 422; eine Sitzung, der Vault-Secrets, ein Repository-Credential oder ein verwaltetes Credential gegeben wurden, antwortet 409; eine unbekannte `id` 404; ein ungültiger Parameter 400 |
| `DELETE` | `/runs/{ref}` | Benötigt `sessions:run:admin`. Löscht den Datensatz einer freigegebenen Sitzung. Eine nicht freigegebene Sitzung antwortet 409 `session must be cleaned before delete (state=…)`. Im Audit-Ledger als `sessions.run.deleted` in der Transaktion des Löschens aufgezeichnet |
| `GET` | `/runs/{ref}/diff` | Worktree-Branch gegenüber seinem Ausgangspunkt: `branch`, `base`, `head` und geänderte `files` (`path`, `status`). 404 ohne Worktree |
| `GET` | `/runs/{ref}/diff/file?path=` | Text eines Pfads an `base` und `head` (`original`, `modified`, jeweils höchstens das Minimum aus `max_read_bytes` des Workspace und 64 KiB) |

Für Codex-Code-Mode-Aufrufe, deren Plan-Argumente nicht im Rollout erhalten bleiben, liest Activity zusätzlich die zuletzt von der Live-Bridge empfangene Benachrichtigung `turn/plan/updated`. Ein Neuladen des Browsers verwendet dieselbe serverseitige Sitzung; für eine Aktualisierung wird kein App-Server gestartet. Ist das Live-Handle weg, können nur noch Daten gelesen werden, die Codex selbst aufbewahrt.

Bei Claude Code stammt das Fenster aus dem nativen Result-Frame `modelUsage[model].contextWindow` und bleibt über Stopp und Reconnect erhalten. Eine passende, eindeutige Meldung liefert `context_percent` als `100 × context_tokens / context_window`; andernfalls sind beide Felder `null`, und CLI und Konsole zeigen Tokens mit einem Hinweis auf ein unbekanntes Fenster an. Ein neuer Start des Tools löscht die vorherige Fenstermeldung.

Workflow-Tasks können `workflow` enthalten: `run_id`, `name`, `duration_ms`, `agents_total`, `agents_done`, `agents_failed` und `phases`. Jede Phase hat `index`, `title`, optional `detail` und `state` sowie dieselben Agent-Zähler. Die Zähler beschreiben die bisher beobachteten Agents. Im Live-Betrieb beschreibt ein Phasenzustand beobachtete laufende oder fehlgeschlagene Agents. Der Abschluss eines Agents beweist nicht, dass das Skript seine Phase verlassen hat; deshalb markiert er die Phase nicht als abgeschlossen und kennzeichnet keinen aktuellen Skriptschritt. Die abschließende Workflow-Zusammenfassung liefert die abgeschlossenen Phasenzustände.

Live-Workflow-Snapshots stammen aus der Ausgabe `system/task_progress` von Claude Code, die sich den Attach-Stream teilt. Die abgeschlossene `workflows/<runId>.json` des Tools füllt die abschließende Zusammenfassung unter den Home-, Link- und Größenregeln des Transkripts. Lesevorgänge der Zusammenfassung sind auf 8 MiB je Datei und 64 MiB je Anfrage begrenzt; größere native Logs oder Ergebnisse machen die Zusammenfassung unlesbar, bewahren aber den verfügbaren Live-Fortschritt. Unlesbare Zusammenfassungen und Lücken im begrenzten Live-Puffer tragen zu `unreadable` bei.

### Eine Sitzung verschieben und per Handoff übergeben

Eine Sitzung läuft auf zwei Wegen in einem anderen Konto weiter, und die Konsole sowie
`olivares session move` / `olivares session handoff` bieten beide an. Ein Move trägt die gespeicherte
Konversation einer Claude-Code-Sitzung in ein anderes Profil desselben Tools; nur Claude-Code-Sitzungen können
daher verschoben werden. Ein Handoff startet eine neue Sitzung aus der Konversation einer Claude-Code-,
Codex-, Gemini-CLI- oder OpenCode-Sitzung.

Ein Lauf trägt `limit`, wenn sein Tool den letzten Turn an einem Limit beendet hat:
`{kind, resets_at, account_ref}`. `kind` ist `usage` (das Plankontingent des Kontos ist
aufgebraucht) oder `rate` (der Provider drosselt Anfragen). `resets_at` ist RFC 3339, vorhanden,
wenn das Tool meldet, wann das Limit aufgehoben wird, und fehlt andernfalls; `account_ref` ist
das Profil, unter dem der Turn lief. Claude Code liefert die Tatsache in seinem eigenen Ergebnis
(`api_error: usage_limit_reached` oder HTTP 429) und die Rücksetzzeit in seinem
`rate_limit_event`; Codex liefert sie in einem fehlgeschlagenen Turn vom Typ `usageLimitExceeded`
oder `rateLimitExceeded` und die Rücksetzzeit in den Fenstern von `account/rateLimits/updated`;
sind zwei Fenster aufgebraucht, wird das spätere Zurücksetzen aufgezeichnet, und eine
Rücksetzzeit, die mehr als ein Jahr in der Zukunft liegt, wird weggelassen. Das Ledger erhält ein
Event `limit_reached`, wenn die Tatsache erstmals aufgezeichnet wird; dieselbe erneut festgestellte
Tatsache fügt keines hinzu. Der nächste erfolgreiche Turn löscht `limit`, ebenso ein Wechsel zu
einem anderen Konto. Ein Ergebnis anderer Art setzt es nie.

`POST /runs/{ref}/move` behält dieselbe Sitzung bei. Es braucht eine Sitzung im Zustand
`stopped` oder `failed`, deren Konversation in ihrem Konto-Home liegt, und ein Zielprofil
desselben Tools, das ein Start unter ihm akzeptieren würde (aktiv, Umgebung dieses Knotens, eine
autorisierte Quelle). Die Engine kopiert genau diese eine Konversation, das Transkript samt seinen
Subagent- und Tool-Result-Dateien, in das Ziel-Home, niemals eine Anmeldung, eine Einstellung oder
eine andere Konversation, und bindet die Lauf-Zeile, die Live-Zeile und das Ledger in einer
Transaktion um. Der Lauf behält seine `run_ref` und seine Provider-Konversations-ID; das Ledger
erhält ein Event `moved`, dessen Detail beide Profile per Referenz nennt und die übertragenen
Dateien zählt, und eine fehlgeschlagene Sitzung behält ihren `reason`. Das Quell-Home wird nur
gelesen. Eine Ablehnung schreibt nichts und entfernt, was der Move angelegt hat:

- 409 für eine laufende Sitzung, eine Sitzung, der Vault-Secrets, ein Repository-Credential, ein
  verwaltetes Credential oder ein Package-Registry-Token gegeben wurden (was sie ausgegeben hat, lässt sich nicht als frei
  davon nachweisen), eine Zeile, deren Home ihr Profil nicht besitzt, eine Konversations-ID, die
  unter dem Ziel bereits an eine andere Sitzung gebunden ist, ein Ziel, das eine andere Version
  der Konversation enthält, ein Ziel eines anderen Tools oder eine Datei, die sich beim
  Übertragen geändert hat (erneut versuchen);
- 422 für einen Link, einen zweiten Namen für eine Datei oder eine Spezialdatei innerhalb der
  Konversation sowie für die Sitzung eines anderen Tools.

Eine Kopie derselben Konversation, die ein früherer Move auf dem Ziel hinterlassen hat, wird
durch die neuere ersetzt. Nichts startet: Das nächste `POST /runs/{ref}/resume` ist das
gewöhnliche, daher gelten für es die Richtlinie, die Budgets und die Genehmigungen des
Zielprofils. Eine Sitzung, die noch keine Konversation hat, zieht um, ohne dass etwas zu
übertragen ist, und startet auf dem Ziel erneut.

`GET /runs/{ref}/handoff` und `POST /runs/{ref}/handoff` setzen in einem anderen Tool fort, wo es
keinen nativen Resume gibt. Sie lesen die gespeicherte Konversation einer Claude-Code-, Codex-, Gemini-CLI- oder OpenCode-Sitzung
(eine Grok-Sitzung antwortet 422). Die Sitzung darf `stopped`, `failed`, `running` oder `idle` sein; eine,
die noch nicht gestartet ist, antwortet 409. Die Vorschau liest eine laufende Sitzung so, wie sie ist. Das
POST stoppt eine laufende Sitzung zuerst, so wie `POST /runs/{ref}/stop` es tut, und liest ihre Konversation,
nachdem ihr Tool beendet ist; jede Ablehnung, die den Stopp nicht braucht (das Zielprofil, Vault-Secrets,
ein Repository-Credential), kommt vor ihm, und eine Ablehnung danach sagt, dass die Sitzung gestoppt wurde
und fortgesetzt werden kann. Codex wird aus seinem Rollout gelesen, Gemini CLI aus seinem Chat-Log und
OpenCode über sein eigenes `opencode export`, einmal und offline unter dem Profil der Sitzung. Die
Vorschau wird ohne Modell aus der gespeicherten Konversation erzeugt, damit die Person sie prüfen
und bearbeiten kann, und jedes Lesen wird im Audit-Ledger als `sessions.run.handoff_preview`
aufgezeichnet. Das POST startet eine neue Sitzung im gewählten Profil, im selben Workspace und
unter derselben Vorlage, deren erster Turn aus der geprüften `summary` (erzeugt, wenn sie fehlt)
und dem Pfad des Transkripts besteht; das Transkript wird unter dem Konfigurations-Home des
Zielprofils unter einem eigenen Namen geschrieben. Das Transkript enthält Nachrichten vollständig
bis zu je 256 KiB, Tool-Aufrufe und -Ergebnisse gekürzt, und nennt, wie viele Einträge es nicht
lesen konnte. Der neue Lauf meldet `handoff_of`, und das Ledger des Originals erhält ein Event
`handed_off`. Die Antwort trägt `summary_state`:

| `summary_state` | Bedeutung |
|---|---|
| `delivered` | Der erste Turn hat die neue Sitzung erreicht. |
| `not_delivered` | Er ist nicht angekommen. `first_turn` enthält den zu sendenden Text. |
| `unknown` | Er wurde versucht, und das Ergebnis wurde nicht bestätigt; prüfen Sie, bevor Sie `first_turn` erneut senden. |
| `waits_for_approval` | Der Start wird für eine Person zurückgehalten (202); es wird kein erster Turn automatisch gesendet. |

`first_turn` ist nur vorhanden, wenn der Zustand nicht `delivered` ist, weil er den Pfad des
Anhangs nennt. Die Quellkonversation wird mit 409 abgelehnt, wenn ihrer Sitzung Vault-Secrets, ein
Repository-Credential, ein verwaltetes Credential oder ein Package-Registry-Token gegeben wurden, weil ein Handoff sie an einen
anderen Provider sendet, und mit 422 oberhalb von 256 MiB oder wenn sie einen einzelnen Eintrag
enthält, der größer als 32 MiB ist.

### Worktree-Option

`POST /runs` akzeptiert den optionalen Boolean `worktree`. Bei `true` und einem
`workspace_ref`, der auf einen registrierten obersten Ordner eines Git-Repositorys
verweist, arbeitet die Sitzung in einem neuen Git-Worktree und Branch dieses
Repositorys (`workspace_path` ist der Worktree, der Lauf meldet `worktree_branch`).
Fehlend, null oder false behält das bisherige Verhalten. Die Engine antwortet 422
bei einem Workspace, der kein Git-Worktree ist, nicht der oberste Repository-Ordner ist, keinen Commit hat, schreibgeschützt ist oder schreibgeschützte Ordner hat, dessen Git-Konfiguration einen Filter oder ein Include nennt, bei nicht-nativer Isolation und bei einem Start ohne Workspace;
503, wenn dem Node ein Worktree-Verzeichnis fehlt. `POST /runs/{ref}/resume`
kehrt in denselben Worktree zurück.

`POST /runs/{ref}/cleanup` akzeptiert optional den Body `{"discard_worktree": true}`;
ohne Body ist es der Aufruf, der er immer war, und der Server nimmt trotzdem jeden Body an:
nur ein explizites `true` bestätigt. Eine Sitzung mit Worktree wird freigegeben, wenn ihr Branch in den aktuellen
Workspace-Branch gemergt ist, der Worktree auf diesem Branch steht und keine
uncommitted Dateien hat. Andernfalls wird der Aufruf mit 409 abgelehnt (ungemergte Arbeit,
detached HEAD, ein Worktree, den der Node nicht erreichen kann); der Lauf bleibt gestoppt, sodass er mit
`discard_worktree` wiederholt werden kann, um Worktree und Branch trotzdem zu entfernen, oder, bei einem Worktree, den der Node nicht erreichen kann, um die Sitzung freizugeben und den Worktree dort zu belassen. Das
Ledger-Event `cleaned` hält fest, was mit dem Worktree geschehen ist, samt Branch-Spitze.

### Den Setup-Befehl eines Ordners genehmigen

Ein Setup-Befehl gehört zum registrierten Ordner, im Speicher der Engine. Repository-Dateien und
Umgebungsvariablen können ihn nicht ändern. Bisher führt nichts den Befehl in diesen Operationen
oder beim Sitzungsstart aus.

- `PUT /workspaces/{ref}/setup-command` nimmt `{"command":"npm ci"}` entgegen. Der exakte
  UTF-8-Text bleibt erhalten, einschließlich Leerraum; höchstens 8192 Bytes, ohne NUL. Eine leere
  Zeichenfolge löscht den Befehl. Erfordert Workspace-Administration.
- `GET /workspaces/{ref}` liefert `setup_command`, `setup_command_sha256` und, nach einer
  Genehmigung, `setup_approval` mit `sha256`, `actor` und `at`. `setup_approved` ist nur wahr,
  solange diese Genehmigung den aktuellen Text abdeckt. Nach einer Änderung bleibt
  `setup_approval` als Evidenz erhalten; prüfen Sie daher `setup_approved`, niemals das
  Vorhandensein von `setup_approval`.
- `POST /workspaces/{ref}/setup-command/approve` nimmt `{"sha256":"<reviewed hash>"}` entgegen.
  Es erfordert Workspace-Administration und lehnt eine Agent-Identität ab. Der Hash muss zum
  aktuell gespeicherten Text passen; eine gleichzeitige Änderung liefert 409. Die Genehmigung und
  ihr Audit-Eintrag `sessions.workspace.approve-setup` werden gemeinsam committet.

Ein Worktree-Start liefert 409, wenn der Befehl keine passende Genehmigung hat, und zwar bevor ein
Worktree angelegt wird. Die Meldung nennt den Befehl und den Genehmigungsschritt. Die Änderung
eines einzigen Zeichens, einschließlich Leerraum, erfordert erneut eine Genehmigung. Die letzte
Genehmigung bleibt als Evidenz erhalten; ihr Hash muss `setup_command_sha256` entsprechen, damit
sie gilt, sodass das Zurücksetzen des Befehls auf einen früher genehmigten Text diese Genehmigung
wieder wirksam macht. Wer den Ordner lesen kann, kann den Befehl lesen; halten Sie daher Secrets
aus ihm heraus. `POST /runs` mit `"no_setup":true` überspringt das Setup nur für diesen Start. Ein
Handoff einer Sitzung mit Worktree startet einen neuen Worktree und braucht dieselbe Genehmigung;
er hat keine Option zum Überspringen. Andere Starts und Resumes behalten ihr bisheriges Verhalten.

Mit der CLI setzen Sie ihn mit `olivares agent workspace setup <ref> --command 'npm ci'`, lesen ihn
mit `olivares agent workspace setup <ref>` und genehmigen den angezeigten Hash mit
`olivares agent workspace approve-setup <ref> --sha256 <hash>`.
`olivares session start --worktree --no-setup` überspringt das Setup einmalig.

### Einen Worktree an einem Commit oder Branch starten

Mit `worktree` akzeptiert `POST /runs` zusätzlich optional `worktree_from`: eine
vollständige Commit-ID (40 oder 64 kleingeschriebene Hex-Zeichen) oder einen lokalen
Branch des Workspace-Repositorys. Der neue Worktree und sein eigener neuer Branch
starten dort statt am aktuellen Workspace-Commit; benannter Branch und Checkout
bleiben unverändert. So öffnet ein Empfänger die im Handoff benannte Arbeit, deren
Inhalt optional `branch` und `sha` tragen kann. Git löst den Wert zu einer vollständigen
Commit-ID auf; nur diese wird verwendet. Die Engine antwortet vor jeder Erstellung
mit 422 für einen Commit, den das Repository nicht hält, einen Branch, den es nicht hat, einen Revisionsausdruck, einen Bereich, eine Option
und ein `worktree_from` ohne `worktree`. Ohne Angabe startet der Lauf wie bisher am
aktuellen Workspace-Commit.

`GET /runs/{ref}/diff` listet die vom Sitzungs-Worktree-Branch gegenüber dem aktuellen
Workspace-Commit geänderten Pfade (`base`: Merge-Basis; `head`: Branch-Spitze;
beide vollständige Commit-IDs; höchstens 200 Pfade, weitere zeigt `truncated` an).
`GET /runs/{ref}/diff/file?path=` liefert den Text eines Pfads an `base` und `head`,
leer, wenn die Datei dort fehlt. Die API liest committed Git-Objekte über Plumbing-
Befehle, enthält daher keine uncommitted Änderungen und benötigt dieselbe Berechtigung
wie das Lesen des Laufs. Workspace-Dateiregeln bleiben erhalten: Pfade außerhalb
erlaubter Unterpfade fehlen in der Liste und liefern 404; eine verweigernde
DLP-Posture liefert 403 (mit Audit wie bei einem Workspace-Dateizugriff), Dateien
über 16 MiB liefern 413. Eine Sitzung ohne Worktree oder aus einem anderen Mandanten
liefert 404; ein fehlender Branch oder fehlende gemeinsame Historie liefert 409.
`worktree_from` liefert auch 422 für einen Commit, den kein Branch, Tag oder
Remote-Branch des Repositorys hält.

Reconnect nach einem abgebrochenen Attach ist `GET …/attach?from={last+1}` auf
dem **selben** lebenden Prozess. Nach Prozessverlust sagt Attach, dass die
Sitzung nicht live ist. Resume startet eine neue Generation. Reconnect erfindet
keinen Ersatzprozess (SDD R04).

### Von einem Repository mitgeführte Agent-Konfiguration

Ein geklontes Repository kann Agent-Konfiguration mitführen, die ein Programm ausführt oder
Verkehr umleitet. Die Engine behandelt sie als nicht vertrauenswürdige Eingabe, wenn eine Sitzung
in einem Ordner startet. Sie liest die unten genannten Dateien, führt sie nie aus und erfasst nur
die aufgeführten Schlüssel. Ein leerer Wert (`{}`, `[]`, `""`, `false`, `null`) konfiguriert
nichts und wird nicht markiert.

| Tool | Datei | Markiert |
| --- | --- | --- |
| Claude Code | `settings.json` und `settings.local.json` im Ordner `.claude` | `hooks`, `apiKeyHelper`, `statusLine`, `mcpServers`, `enableAllProjectMcpServers`, `enabledMcpjsonServers`, `awsAuthRefresh`, `awsCredentialExport`, `otelHeadersHelper`, `fileSuggestion`, `enabledPlugins`, `extraKnownMarketplaces` und `env`-Einträge mit einem Namen aus der letzten Zeile |
| Claude Code | `.mcp.json` | `mcpServers` |
| Codex | `.codex/config.toml` | `hooks`, `notify`, `mcp_servers`, `model_providers`, `otel`, jedes `*_base_url` auf oberster Ebene und `shell_environment_policy.set`-Einträge mit einem Namen aus der letzten Zeile; dieselben Schlüssel unter `profiles.<name>` |
| Gemini CLI | `.gemini/settings.json` | `hooks`, `mcpServers`, `toolDiscoveryCommand`, `toolCallCommand`, `proxy`, `tools.discoveryCommand`, `tools.callCommand`, `mcp.serverCommand`, `telemetry.otlpEndpoint` |
| Gemini CLI | `.gemini/.env`, `.env` | Einträge mit einem Namen aus der letzten Zeile |
| Umgebungsvariablen-Namen | | `NODE_OPTIONS`, `NODE_EXTRA_CA_CERTS`, `NODE_TLS_REJECT_UNAUTHORIZED`, `LD_PRELOAD`, `LD_LIBRARY_PATH`, `LD_AUDIT`, `DYLD_INSERT_LIBRARIES`, `BASH_ENV`, `PATH`, `PYTHONPATH`, `GIT_SSH_COMMAND`, `GIT_EXTERNAL_DIFF`, `SSL_CERT_FILE`, `CLAUDE_CODE_SHELL_PREFIX`, `ANTHROPIC_CUSTOM_HEADERS`, `AWS_ENDPOINT_URL*`, und jeder Name, der auf `_BASE_URL`, `_API_BASE`, `_ENDPOINT` oder `_PROXY` endet (`NO_PROXY` schränkt einen Proxy nur ein und wird nicht markiert) |

Eine Datei, die existiert, sich aber nicht als ihr Format lesen lässt (kein gültiges JSON oder
TOML, über 1 MiB, tiefer als 64 Ebenen verschachtelt, keine reguläre Datei), gilt als markiert,
weil sich nicht zeigen lässt, dass sie sicher ist. In TOML ist jedes Segment eines Schlüssels mit
Punkten oder eines Tabellen-Headers eine Ebene, wie im geparsten Dokument, und addiert sich zu den
Klammern darum: ein Header mit 30 Segmenten über 35 verschachtelten Inline-Tabellen sind 65 Ebenen.
Sobald eine Datei einen `[[array]]`-Header hat, zählen Header-Segmente doppelt, weil jedes ein
Array und sein Element benennen kann. Eine übergroße Datei wird bei jeder Prüfung vollständig
gehasht. Ein Ordner, in dem nichts markiert ist, startet wie bisher.

- `GET /workspaces/{ref}/project-config` listet die markierten Schlüssel je Datei mit ihren Werten
  (ein Passwort in einer URL wird maskiert) und den `sha256` genau dieses markierten Teils, über
  die Dateien aller drei Tools, sowie `approved` und die letzte `approval`. Es liest den Ordner
  selbst; ein von einem anderen Commit gestarteter Worktree kann andere versionierte Dateien
  enthalten, und seine Ablehnung nennt seinen eigenen Hash.
- `POST /workspaces/{ref}/project-config/approve` nimmt `{"sha256":"<reviewed hash>"}` entgegen,
  erfordert Workspace-Administration und lehnt eine Agent-Identität ab. Die Genehmigung speichert,
  wer, wann und den Hash, mit einem Audit-Eintrag `sessions.workspace.approve-project-config` in
  derselben Transaktion.

Ein Start oder Resume eines Tools, das eine markierte Datei liest, liefert 409, sofern die
gespeicherte Genehmigung nicht dem aktuellen Hash entspricht. Die Änderung eines einzigen Zeichens
eines markierten Werts oder das Hinzufügen oder Entfernen eines markierten Schlüssels erfordert
erneut eine Genehmigung; die Änderung nicht markierter Schlüssel nicht. Es gibt keine Option, die
Prüfung zu überspringen.

Eine Claude-Code-Sitzung, die die Hooks der Engine trägt, wird mit `--setting-sources ""`
gestartet; unter Claude Code 2.1.299 lädt diese Sitzung keine Projekt- oder lokale Settings-Datei
und keine Projekt-`.mcp.json`, daher wird ein solcher Start nicht abgelehnt. Ein Claude-Code-Start
ohne die Hooks der Engine (Remote Control oder eine Engine ohne Hook-Endpunkt) liest diese Dateien
und wird geprüft. Codex beachtet `.codex/config.toml` nur für ein Projekt, dem in seinem
Konto-Home vertraut wird, und ignoriert die Datei eines nicht vertrauenswürdigen Projekts; die
Engine kann das Tool nicht fragen, daher prüft sie diese Datei, und eine Person genehmigt sie
einmal. Gemini CLI 0.64.0, so gestartet, wie die Engine es startet (`gemini --acp`), ignoriert
`.gemini/settings.json` und `.gemini/.env` eines Ordners, dem sein Konto-Home nicht vertraut, und
führt in einem vertrauenswürdigen Ordner `tools.discoveryCommand`, die `hooks` und die
`mcpServers`-Befehle aus und übernimmt die Basis-URL aus `.gemini/.env`; die Engine prüft diese
Dateien aus demselben Grund. Es lädt außerdem Dateien übergeordneter Ordner, die diese Prüfung
nicht liest.

Mit der CLI lesen Sie sie mit `olivares agent workspace project-config <ref>` und genehmigen mit
`olivares agent workspace approve-project-config <ref> --sha256 <hash>`.

## Transport (Community)

Jede offizielle CLI startet auf dem Transport, den ihre eigene Operate-Form
braucht; `cliruntime.LaunchTransport` erklärt, welcher das ist. Alle drei Formen sind
heute Stdio-Protokolle, also verdrahtet die Composition Root
`sessions.NewProcRunner()`: stdin, stdout und stderr sind Pipes und bleiben
getrennte Ströme.

**Claude Code verweigert ein Terminal auf stdin.** Die `--print`-Form mit
stream-json antwortet `Error: Input must be provided either through stdin or as
a prompt argument when using --print` und endet mit 1 ohne einen Protokoll-
Frame. Ein Terminal braucht eine interaktive Form; `sessions.NewPTYRunner()`
bleibt dafür unter Linux verfügbar, mit stdin/stdout auf einem rohen lokalen PTY
und stderr auf einer Pipe. Keiner der beiden führt Container- oder
Sandbox-Isolation aus. Ein Host mit podman oder Docker ergänzt für `isolation=container`
in der stdio-Form einen Container-Runner; Sandbox bleibt verweigert (siehe
[Topology 4](/how-to/run-claude-code-with-olivares/#topology-4--olivares-native-claude-in-a-per-session-container)).

Der Driver-Vertrag liegt in `modules/sessions/cliruntime`. Arten: `claude`,
`codex`, `grok`. Conformance läuft immer gegen ein In-Process-Fake und gegen
einen lokalen Peer auf beiden Transporten. Wenn `claude` / `codex` / `grok` auf PATH liegen, durchläuft
eine eigene Testbatterie den gesamten Vertrag gegen das echte Binary (Start, Attach, Input, Ausgabe, Reconnect, Stop, Resume) und
sendet keinen Modell-Turn: Jeder Handshake ist ein Protokoll-Handshake, und jeder Start bekommt ein frisches
HOME und Konfigurations-Home.

## Verwandt

- [Eine Provider-Session betreiben](/how-to/operate-provider-sessions/)
- [Modul II — Live-Betrieb](/reference/modules/ii-sessions/)
- [Claude Code verbinden](/how-to/connect-claude-code/)
- [Beta-OpenAPI](/reference/api-beta/)
