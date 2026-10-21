---
title: Session-Runtime-API (offizielle CLIs)
description: >-
  Community-HTTP-Oberfläche zum Listen, Anhängen, Speisen und Stoppen eigener
  Claude-Code-, Codex- und Grok-CLI-Prozesse. Berechtigungen, PTY, Resume und Reconnect.
---

Die Control Plane **startet die Anbieter-CLI**. Sie ersetzt Claude Code, Codex
oder Grok Build nicht. Sitzungen und Terminals sind ein Modul dieses Produkts,
nicht das Produkt.

Diese Seite dokumentiert die Community-Operate-Routen unter
`/v1/m/sessions/runs`. Sie stehen bereits im
[beta-OpenAPI-Dokument](/reference/api-beta/). 26.10 ergänzt den
Driver-Vertrag, einen lokalen PTY-Runner und die Journeys J01–J08 als Tests.

## Editionsgrenze

| Edition | Was sie tut | Was sie nicht tut |
|---|---|---|
| **Community (diese Seite)** | Eigenes lokales Kind: Start, stdin/stdout/stderr, Attach mit Cursor, Resume der exakten Konversation, Reconnect eines lebenden Streams, Stop mit beobachtetem Exit-Status. Sitzungszeilen und Evidenz bleiben in Modul II. | Identity-&-Scale-Engine mit mehreren Panes, mTLS-Listener, Cockpit-Pane-Eingabe, xterm-UI-Chunk |
| **Identity-&-Scale-Overlay** | Kommerzielle session-cockpit-Engine (Listener, Panes, Ledger). Routen unter `/v1/m/session-cockpit/`, wenn das Modul vorliegt. | Ersetzt `/v1/m/sessions/runs` nicht |

Ein Community-Build beantwortet den Overlay-Namespace durch **Abwesenheit**
(404). Es gibt keinen 501-Stub.

Der Claude-Code-Hook bleibt `olivares claude-hook` (PreToolUse-PEP). Das ist
Beobachtung und Durchsetzung, keine Ersatz-CLI.

## Berechtigungen

Bestehende Durchsetzung auf den Modulrouten:

| Berechtigung | Routen |
|---|---|
| `sessions:run:read` | `GET /runs`, `GET /runs/{ref}`, `GET /runs/{ref}/events`, `GET /runs/{ref}/attach`, `GET /runs/{ref}/handoff`, `GET /runs/{ref}/activity`, `GET /runs/{ref}/activity/agents/{agent}`, `GET /runs/{ref}/messages` |
| `sessions:run:write` | `POST /runs`, `POST /runs/{ref}/input`, `POST /runs/{ref}/interrupt`, `POST /runs/{ref}/stop`, `POST /runs/{ref}/resume`, `POST /runs/{ref}/move`, `POST /runs/{ref}/handoff` |
| `sessions:run:admin` | `POST /runs/{ref}/cleanup`, `DELETE /runs/{ref}` |

Ein Viewer darf listen. Create, Input und Stop brauchen write. Der Authorizer
der Route ist der Durchsetzungspunkt; eine fehlende Berechtigung ist 403.

## Routen, die die Konsole liest

Basis: `/v1/m/sessions`. Authentifizieren. `X-Olivares-Tenant` senden.

| Methode | Pfad | Ergebnis |
|---|---|---|
| `GET` | `/runs` | Seite verwalteter Läufe |
| `GET` | `/runs/{ref}` | Ein Lauf. `state` ist abgeleitet. `exit_code` ist beobachtet. Kein erfundenes Success-Feld |
| `GET` | `/runs/{ref}/events` | Lebenszyklus-Evidenzzeilen |
| `GET` | `/runs/{ref}/attach?from={seq}` | SSE: `output`-Rahmen, `lag` wenn der Ring unter dem Cursor verdrängt hat, `end` oder eine not-live-`notice` |
| `POST` | `/runs/{ref}/input` | stdin. Stream-json nutzt `line`/`message`. Codex/Grok nutzen `text`. 202 `{accepted:true}` |
| `POST` | `/runs/{ref}/stop` | SIGTERM, dann SIGKILL der Prozessgruppe. Beobachteter Exit-Status auf der Zeile |
| `POST` | `/runs/{ref}/resume` | Neue Prozessgeneration. Exakte gespeicherte Konversation |
| `POST` | `/runs/{ref}/interrupt` | Aktiven Turn abbrechen. Der Prozess bleibt |
| `POST` | `/runs/{ref}/move` | Body `{provider_profile_ref}`. Bindet eine gestoppte oder fehlgeschlagene Claude-Code-Sitzung an ein anderes Profil desselben Tools um und überträgt ihre Konversation. Startet nichts. 200 Lauf |
| `GET` | `/runs/{ref}/handoff` | Die Zusammenfassung, mit der ein Handoff beginnen würde: `summary`, `source_driver`, `turns`, `transcript_bytes`. Schreibgeschützt, im Audit-Ledger aufgezeichnet. Eine laufende Sitzung wird so gelesen, wie sie ist; eine, die noch nicht gestartet ist, antwortet 409 |
| `POST` | `/runs/{ref}/handoff` | Body `{provider_profile_ref, summary?, name?}`. Startet in diesem Profil eine neue Sitzung, die mit dieser verknüpft ist. 201 Lauf plus `summary_state` (und `first_turn`, wenn dieser nicht `delivered` ist); 202, solange der Start auf Genehmigung wartet |
| `GET` | `/runs/{ref}/activity` | Was das Tool einer Claude-Code- oder Codex-Sitzung gerade tut, bei jeder Anfrage aus seiner eigenen nativen Aufzeichnung gelesen: `agents`, `tasks` (Befehle und Hintergrundarbeit), `files` (Pfad, `added`, `removed`, `created`), `todos`, `skills`, `mcp_servers`, `model`, `context_tokens`, das nullable `context_window`, wenn das Tool es meldet, und `context_percent` (Prozentwert für Claude Code, null, wenn unbekannt), `compactions` und `transcript` (`read`, `pending`, bevor das Tool es geschrieben hat, `missing`, wenn es vorhanden sein sollte). Agents und Tasks sind `running`, `completed`, `failed`, `stopped` oder `scheduled`. Codex verwendet seine Rollout-Datei, ohne eine weitere App-Server-Instanz zu starten; Kontext-Tokens beschreiben die letzte Anfrage, nicht die kumulierte Nutzung. Schreibgeschützt; einmal je Leser und Sitzung alle 15 Minuten im Audit-Ledger aufgezeichnet. Ein nicht unterstütztes Tool antwortet 422; eine Sitzung, der Vault-Secrets oder ein Repository-Credential gegeben wurden, antwortet 409, ebenso eine Sitzung, die ein Package-Registry-Token verwendet hat, wenn sie gestoppt ist oder nach einem Resume läuft, das auf einen Start folgt, der eines verwendet hat |
| `GET` | `/runs/{ref}/activity/agents/{agent}` | Das Transkript eines Claude-Code-Subagents in einfachen Turns (`role`, `text`). Schreibgeschützt, im Audit-Ledger aufgezeichnet. 404 für einen unbekannten Agent. Codex meldet den Zustand von Kindern in der Activity-Antwort der Wurzel; Lesen von Kind-Transkripten wird nicht angeboten |
| `GET` | `/runs/{ref}/messages` | Die Nachrichten einer Claude-Code- oder Codex-Sitzung, bei jeder Anfrage aus dem eigenen Transkript des Tools gelesen, seitenweise: `items` (`id`, `role` `user` oder `assistant`, `at`, `first_line`), `total`, `next`, `transcript` (`read`, `pending`, `missing`). Standardmäßig nur, was die Person gesendet hat; `role=all` fügt die Antworten des Tools hinzu. `q` behält Nachrichten, die diese Phrase irgendwo enthalten (ohne Beachtung der Groß-/Kleinschreibung, höchstens 256 Bytes); `at` (RFC 3339) behält Nachrichten zu oder nach diesem Zeitpunkt, daher ist das erste Element der Anker; `order` ist `asc` (Standard) oder `desc`, `limit` höchstens 200 und `cursor` die ID, ab der eine Seite fortsetzt (`next`); die Elemente sind immer vom ältesten zum neuesten sortiert. `id` liest eine Nachricht vollständig (`text` und `later_same`, wie viele spätere Nachrichten desselben Absenders ihre erste Zeile wiederholen; kein Filter gilt, und `total` ist die Nachrichtenzahl der Sitzung). Die `id` einer Nachricht ist ihre Position unter den Nachrichten der Sitzung und ändert sich nicht, wenn das Transkript wächst. Schreibgeschützt; einmal je Leser, Sitzung und 15-Minuten-Fenster im Audit-Ledger aufgezeichnet. Ein nicht unterstütztes Tool antwortet 422; eine Sitzung, der Vault-Secrets oder ein Repository-Credential gegeben wurden, antwortet 409; eine unbekannte `id` 404; ein ungültiger Parameter 400 |
| `GET` | `/runs/{ref}/diff` | Worktree-Branch gegenüber seinem Ausgangspunkt: `branch`, `base`, `head` und geänderte `files` (`path`, `status`). 404 ohne Worktree |
| `GET` | `/runs/{ref}/diff/file?path=` | Text eines Pfads an `base` und `head` (`original`, `modified`, jeweils höchstens das Minimum aus `max_read_bytes` des Workspace und 64 KiB) |

Für Codex-Code-Mode-Aufrufe, deren Plan-Argumente nicht im Rollout erhalten bleiben, liest Activity zusätzlich die zuletzt von der Live-Bridge empfangene Benachrichtigung `turn/plan/updated`. Ein Neuladen des Browsers verwendet dieselbe serverseitige Sitzung; für eine Aktualisierung wird kein App-Server gestartet. Ist das Live-Handle weg, können nur noch Daten gelesen werden, die Codex selbst aufbewahrt.

Bei Claude Code stammt das Fenster aus dem nativen Result-Frame `modelUsage[model].contextWindow` und bleibt über Stopp und Reconnect erhalten. Eine passende, eindeutige Meldung liefert `context_percent` als `100 × context_tokens / context_window`; andernfalls sind beide Felder `null`, und CLI und Konsole zeigen Tokens mit einem Hinweis auf ein unbekanntes Fenster an. Ein neuer Start des Tools löscht die vorherige Fenstermeldung.

Workflow-Tasks können `workflow` enthalten: `run_id`, `name`, `duration_ms`, `agents_total`, `agents_done`, `agents_failed` und `phases`. Jede Phase hat `index`, `title`, optional `detail` und `state` sowie dieselben Agent-Zähler. Die Zähler beschreiben die bisher beobachteten Agents. Im Live-Betrieb beschreibt ein Phasenzustand beobachtete laufende oder fehlgeschlagene Agents. Der Abschluss eines Agents beweist nicht, dass das Skript seine Phase verlassen hat; deshalb markiert er die Phase nicht als abgeschlossen und kennzeichnet keinen aktuellen Skriptschritt. Die abschließende Workflow-Zusammenfassung liefert die abgeschlossenen Phasenzustände.

Live-Workflow-Snapshots stammen aus der Ausgabe `system/task_progress` von Claude Code, die mit dem Attach-Stream geteilt wird. Die abgeschlossene `workflows/<runId>.json` des Tools füllt die abschließende Zusammenfassung unter den Home-, Link- und Größenregeln des Transkripts. Lesevorgänge der Zusammenfassung sind auf 8 MiB je Datei und 64 MiB je Anfrage begrenzt; größere native Logs oder Ergebnisse machen die Zusammenfassung unlesbar, bewahren aber den verfügbaren Live-Fortschritt. Unlesbare Zusammenfassungen und Lücken im begrenzten Live-Puffer tragen zu `unreadable` bei.

### Eine Sitzung verschieben und per Handoff übergeben

Eine Sitzung läuft auf zwei Wegen in einem anderen Konto weiter, und die Konsole sowie
`olivares session move` / `olivares session handoff` bieten beide an. `move` trägt die gespeicherte
Konversation einer Claude-Code-Sitzung in ein anderes Profil desselben Tools; nur Claude-Code-Sitzungen können
daher verschoben werden. `handoff` startet eine neue Sitzung aus der Konversation einer Claude-Code-,
Codex-, Gemini-CLI- oder OpenCode-Sitzung.

Ein Lauf trägt `limit`, wenn sein Tool den letzten Turn an einem Limit beendet hat:
`{kind, resets_at, account_ref}`. `kind` ist `usage` (das Plankontingent des Kontos ist
aufgebraucht) oder `rate` (der Provider drosselt Anfragen). `resets_at` ist RFC 3339, vorhanden,
wenn das Tool meldet, wann das Limit aufgehoben wird, und fehlt andernfalls, und `account_ref` ist
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
gelesen. Eine Ablehnung schreibt nichts und entfernt, was der Umzug angelegt hat:

- 409 für eine laufende Sitzung, eine Sitzung, der Vault-Secrets, ein Repository-Credential oder
  ein Package-Registry-Token gegeben wurden (was sie ausgegeben hat, lässt sich nicht als frei
  davon nachweisen), eine Zeile, deren Home ihr Profil nicht besitzt, eine Konversations-ID, die
  unter dem Ziel bereits an eine andere Sitzung gebunden ist, ein Ziel, das eine andere Version
  der Konversation enthält, ein Ziel eines anderen Tools oder eine Datei, die sich beim
  Übertragen geändert hat (erneut versuchen);
- 422 für einen Link, einen zweiten Namen für eine Datei oder eine Spezialdatei innerhalb der
  Konversation sowie für die Sitzung eines anderen Tools.

Eine Kopie derselben Konversation, die ein früherer Umzug auf dem Ziel hinterlassen hat, wird
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
Repository-Credential oder ein Package-Registry-Token gegeben wurden, weil ein Handoff sie an einen
anderen Provider sendet, und mit 422 oberhalb von 256 MiB oder wenn sie einen einzelnen Eintrag
enthält, der größer als 32 MiB ist.

### Worktree-Option

`POST /runs` akzeptiert den optionalen Boolean `worktree`. Bei true und einem
`workspace_ref`, der auf den registrierten obersten Ordner eines Git-Repositorys
verweist, arbeitet die Sitzung in einem neuen Git-Worktree und Branch dieses
Repositorys (`workspace_path` ist der Worktree, der Lauf meldet `worktree_branch`).
Fehlend, null oder false behält das bisherige Verhalten. Die Engine antwortet 422
bei einem Workspace ohne Git-Worktree, außerhalb des obersten Repository-Ordners,
ohne Commit, mit Schreibschutz oder schreibgeschützten Ordnern, mit Git-Filtern oder
Datei-Includes, bei nicht-nativer Isolation oder einem Start ohne Workspace;
503, wenn dem Node ein Worktree-Verzeichnis fehlt. `POST /runs/{ref}/resume`
kehrt in denselben Worktree zurück.

`POST /runs/{ref}/cleanup` akzeptiert optional `{"discard_worktree": true}`.
Ohne Body bleibt das bisherige Verhalten; nur explizites true bestätigt.
Eine Sitzung mit Worktree wird freigegeben, wenn ihr Branch in den aktuellen
Workspace-Branch gemergt ist, der Worktree auf diesem Branch steht und keine
uncommitted Dateien hat. Andernfalls antwortet der Aufruf 409 (ungemergte Arbeit,
detached HEAD, unerreichbarer Worktree); der Lauf bleibt gestoppt. Wiederholen mit
`discard_worktree` entfernt Worktree und Branch trotzdem. Bei unerreichbarem
Worktree wird die Sitzung freigegeben und der Worktree bleibt liegen. Das
Ledger-Event `cleaned` hält das Ergebnis samt Branch-Spitze fest.

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
bleiben unverändert. So öffnet ein Empfänger die im Handoff benannte Arbeit, dessen
Inhalt optional `branch` und `sha` tragen kann. Git löst den Wert zu einer vollständigen
Commit-ID auf; nur diese wird verwendet. Die Engine antwortet vor jeder Erstellung
422 für einen fehlenden Commit oder Branch, Revisionsausdrücke, Bereiche, Optionen
oder `worktree_from` ohne `worktree`. Ohne Angabe startet der Lauf wie bisher am
aktuellen Workspace-Commit.

`GET /runs/{ref}/diff` listet die vom Sitzungs-Worktree-Branch gegenüber dem aktuellen
Workspace-Commit geänderten Pfade (`base`: Merge-Basis; `head`: Branch-Spitze;
beide vollständige Commit-IDs; höchstens 200 Pfade, weitere zeigt `truncated` an).
`GET /runs/{ref}/diff/file?path=` liefert den Text eines Pfads an `base` und `head`,
leer, wenn die Datei dort fehlt. Die API liest committed Git-Objekte über Plumbing-
Befehle, enthält keine uncommitted Änderungen und benötigt dieselbe Berechtigung
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
braucht; `cliruntime.LaunchTransport` erklärt welchen. Alle drei Formen sind
heute Stdio-Protokolle, also verdrahtet die Composition Root
`sessions.NewProcRunner()`: stdin, stdout und stderr sind Pipes und bleiben
getrennte Ströme.

**Claude Code verweigert ein Terminal auf stdin.** Die `--print`-Form mit
stream-json antwortet `Error: Input must be provided either through stdin or as
a prompt argument when using --print` und endet mit 1 ohne einen Protokoll-
Frame. Ein Terminal braucht eine interaktive Form; `sessions.NewPTYRunner()`
bleibt dafür unter Linux verfügbar. Container- und Sandbox-Isolation bleiben
bei beiden verweigert.

Der Driver-Vertrag liegt in `modules/sessions/cliruntime`. Arten: `claude`,
`codex`, `grok`. Conformance läuft immer gegen ein In-Process-Fake und gegen
einen lokalen PTY-Peer. Wenn `claude` / `codex` / `grok` auf PATH liegen, besitzt
ein getrennter Test das echte Binary, stoppt es und zeichnet den Exit auf. Er
sendet keinen Modell-Turn.

## Verwandt

- [Eine Provider-Session betreiben](/how-to/operate-provider-sessions/)
- [Modul II — Live-Betrieb](/reference/modules/ii-sessions/)
- [Claude Code verbinden](/how-to/connect-claude-code/)
- [Beta-OpenAPI](/reference/api-beta/)
