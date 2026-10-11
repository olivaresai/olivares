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
| `sessions:run:read` | `GET /runs`, `GET /runs/{ref}`, `GET /runs/{ref}/events`, `GET /runs/{ref}/attach` |
| `sessions:run:write` | `POST /runs`, `POST /runs/{ref}/input`, `POST /runs/{ref}/interrupt`, `POST /runs/{ref}/stop`, `POST /runs/{ref}/resume` |
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
| `GET` | `/runs/{ref}/diff` | Worktree-Branch gegenüber seinem Ausgangspunkt: `branch`, `base`, `head` und geänderte `files` (`path`, `status`). 404 ohne Worktree |
| `GET` | `/runs/{ref}/diff/file?path=` | Text eines Pfads an `base` und `head` (`original`, `modified`, jeweils höchstens das Minimum aus `max_read_bytes` des Workspace und 64 KiB) |

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
